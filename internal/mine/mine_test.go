package mine

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/web"
)

func code(err error) string {
	if err == nil {
		return ""
	}
	return apperr.From(err).Code
}

func rowsOf(t *testing.T, v any) []Row {
	t.Helper()
	b, _ := json.Marshal(v)
	var r []Row
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func dropsOf(t *testing.T, v any) []Drop {
	t.Helper()
	b, _ := json.Marshal(v)
	var r []Drop
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMatchProperty(t *testing.T) {
	sites := []Site{
		{"https://other.org/", "siteOwner"}, {"http://www.example.com/", "siteOwner"},
		{"https://www.example.com/", "siteFullUser"}, {"sc-domain:example.com", "siteOwner"},
		{"https://pending.net/", "siteUnverifiedUser"}, {"https://example.com/docs/", "siteOwner"},
	}
	for _, c := range []struct{ host, want string }{
		{"example.com", "sc-domain:example.com"},
		{"blog.example.com", "sc-domain:example.com"}, // the Domain property covers subdomains
		{"other.org", "https://other.org/"},
		{"nothing.io", ""},
		{"pending.net", ""},
	} {
		got, _ := matchProperty(sites, c.host)
		if (got == nil && c.want != "") || (got != nil && got.URL != c.want) {
			t.Errorf("%s: got %v want %q", c.host, got, c.want)
		}
	}
	// With no Domain property the https root beats http and sub-paths.
	got, _ := matchProperty(sites[1:2:2], "example.com")
	if got == nil || got.URL != "http://www.example.com/" {
		t.Errorf("url-prefix fallback: %v", got)
	}
	got, _ = matchProperty([]Site{sites[1], sites[2], sites[5]}, "example.com")
	if got == nil || got.URL != "https://www.example.com/" {
		t.Errorf("prefers https root: %v", got)
	}
	if _, un := matchProperty(sites, "pending.net"); un == nil {
		t.Error("an unverified property should be reported")
	}
}

func TestSiteHost(t *testing.T) {
	for in, want := range map[string]string{"https://www.Example.com/a?b=1": "example.com", "example.com": "example.com", "www.example.com:8080": "example.com", "": ""} {
		if got := siteHost(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestPeriodEndsTwoDaysAgo(t *testing.T) {
	newEnv(t)
	p := makePeriod(28)
	if p.End != "2026-11-10" || p.Start != "2026-10-14" || p.PrevEnd != "2026-10-13" || p.PrevStart != "2026-09-16" {
		t.Errorf("%+v", p)
	}
}

func TestQueriesGSCOnlyRefreshesAndSavesToken(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	rep, err := Mine(context.Background(), "queries", "https://www.example.com/", Options{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != "gsc" || rep.SourceReason == "" {
		t.Errorf("source %q reason %q", rep.Source, rep.SourceReason)
	}
	if rep.Data["property"] != "sc-domain:example.com" {
		t.Errorf("property %v", rep.Data["property"])
	}
	rows := rowsOf(t, rep.Data["queries"])
	if len(rows) != 3 || rows[0].Key != "seo tools" || rows[0].Clicks != 120 || rows[0].Impressions != 3000 || rows[0].Position != 4.2 {
		t.Errorf("rows %+v", rows)
	}
	if _, has := rep.Data["bing"]; has {
		t.Error("no Bing section expected without a key")
	}
	q := f.queryBody[0]
	if q["dataState"] != "final" || q["startDate"] != "2026-10-14" || q["endDate"] != "2026-11-10" || q["_property"] != "sc-domain:example.com" {
		t.Errorf("request %+v", q)
	}
	for _, h := range f.authHeader {
		if h != "Bearer new-access" {
			t.Errorf("auth header %q", h)
		}
	}
	// The refresh happened once and the new token was saved for the next command.
	if f.refreshes != 1 {
		t.Errorf("refreshes %d", f.refreshes)
	}
	s, _ := f.mem.Get(config.SecretOAuthToken)
	var tok oauth2.Token
	_ = json.Unmarshal([]byte(s), &tok)
	if tok.AccessToken != "new-access" || tok.RefreshToken != "refresh-1" || tok.Expiry.IsZero() {
		t.Errorf("saved token %+v", tok)
	}
	if _, err := Mine(context.Background(), "pages", "example.com", Options{NoCache: true}); err != nil {
		t.Fatal(err)
	}
	if f.refreshes != 1 {
		t.Errorf("second command refreshed again: %d", f.refreshes)
	}
}

func TestRevokedSignInIsLoginRequired(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.tokenFail = true
	_, err := Mine(context.Background(), "queries", "example.com", Options{})
	if code(err) != apperr.LoginRequired || !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "seox login") {
		t.Errorf("got %v", err)
	}
}

func TestNotSignedInHasSteps(t *testing.T) {
	newEnv(t)
	_, err := Mine(context.Background(), "queries", "example.com", Options{})
	if code(err) != apperr.LoginRequired || !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("got %v", err)
	}
	for _, want := range []string{"Desktop app", "seox login --client client_secret.json", "setup verify"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %s", want, err)
		}
	}
	if d, _ := apperr.From(err).Detail.(map[string]string); d["command"] != LoginFix {
		t.Errorf("detail %v", apperr.From(err).Detail)
	}
}

func TestNoMatchingPropertyIsNotFound(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	_, err := Mine(context.Background(), "queries", "unverified-site.dev", Options{})
	if code(err) != apperr.NotFound || !errors.Is(err, ErrNotVerified) || !strings.Contains(err.Error(), "setup verify unverified-site.dev") {
		t.Errorf("got %v", err)
	}
}

func TestGSCForbiddenAndQuota(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.gscStatus = 403
	if _, err := Mine(context.Background(), "queries", "example.com", Options{}); code(err) != apperr.Forbidden {
		t.Errorf("403: %v", err)
	}
	f.gscStatus = 429
	if _, err := Mine(context.Background(), "queries", "example.com", Options{NoCache: true}); code(err) != apperr.QuotaExceeded {
		t.Errorf("429: %v", err)
	}
}

func TestPagingToTheLimit(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	maxGSCRows = 2
	rep, err := Mine(context.Background(), "queries", "example.com", Options{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rowsOf(t, rep.Data["queries"])); n != 5 {
		t.Errorf("rows %d", n)
	}
	if len(f.queryBody) != 3 {
		t.Fatalf("calls %d", len(f.queryBody))
	}
	for i, want := range []float64{0, 2, 4} {
		if f.queryBody[i]["startRow"] != want {
			t.Errorf("call %d startRow %v", i, f.queryBody[i]["startRow"])
		}
	}
	if f.queryBody[2]["rowLimit"] != float64(1) {
		t.Errorf("last page asks %v rows", f.queryBody[2]["rowLimit"])
	}
}

func TestDropsSortedByLostClicks(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	rep, err := Mine(context.Background(), "drops", "example.com", Options{})
	if err != nil {
		t.Fatal(err)
	}
	q := dropsOf(t, rep.Data["queries"])
	var keys []string
	for _, d := range q {
		keys = append(keys, d.Key)
	}
	// old topic -40 (gone), seo tools -30, site audit checklist -10; "tiny" is under the threshold and "free seo audit" gained.
	if strings.Join(keys, "|") != "old topic|seo tools|site audit checklist" {
		t.Fatalf("queries %v", keys)
	}
	if q[0].ClicksDelta != -40 || q[0].PositionDelta != nil {
		t.Errorf("gone query %+v", q[0])
	}
	if q[1].ClicksDelta != -30 || q[1].PositionDelta == nil || *q[1].PositionDelta != 0.3 {
		t.Errorf("seo tools %+v", q[1])
	}
	if q[2].PositionDelta == nil || *q[2].PositionDelta != 6.8 {
		t.Errorf("checklist %+v", q[2])
	}
	p := dropsOf(t, rep.Data["pages"])
	if len(p) != 3 || p[0].Key != "https://www.example.com/blog/checklist" || p[0].ClicksDelta != -22 || p[1].Key != "https://www.example.com/old" || p[2].ClicksDelta != -5 {
		t.Errorf("pages %+v", p)
	}
	// A higher threshold removes small keys.
	rep, _ = Mine(context.Background(), "drops", "example.com", Options{MinDropImpressions: 600, NoCache: true})
	if d := dropsOf(t, rep.Data["queries"]); len(d) != 2 {
		t.Errorf("with threshold 600: %+v", d)
	}
}

func TestAlmostPositions8To20(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	rep, err := Mine(context.Background(), "almost", "example.com", Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := rowsOf(t, rep.Data["queries"])
	var keys []string
	for _, x := range r {
		keys = append(keys, x.Key)
	}
	// "rare query" has 8 impressions, "deep query" sits at 35, "seo tools" is on page one.
	if strings.Join(keys, "|") != "free seo audit|site audit checklist|seox cli" {
		t.Errorf("almost %v", keys)
	}
}

func TestBingOnlyWhenNotSignedIn(t *testing.T) {
	f := newEnv(t)
	f.bingKey(t)
	rep, err := Mine(context.Background(), "queries", "example.com", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != "bing" || !strings.Contains(rep.SourceReason, "seox login") {
		t.Errorf("source %q / %q", rep.Source, rep.SourceReason)
	}
	for _, k := range f.bingKeys {
		if k != "bing-test-key" {
			t.Errorf("apikey param %q", k)
		}
	}
	b := rep.Data["bing"].(map[string]any)
	rows := rowsOf(t, b["queries"])
	// seo tools: 16 clicks and 300 impressions over two weekly points; position weighted by impressions.
	if len(rows) != 2 || rows[0].Key != "seo tools" || rows[0].Clicks != 16 || rows[0].Impressions != 300 || rows[0].Position != 4.67 {
		t.Errorf("rows %+v", rows)
	}
	if b["granularity"] != "weekly" || b["site"] != "https://www.example.com/" {
		t.Errorf("bing section %v", b)
	}
	links := b["links"].(*BingLinks)
	if links.TotalLinks != 29 || links.Top[0].URL != "https://www.example.com/" || links.TotalPages != 2 || links.Note == "" {
		t.Errorf("links %+v", links)
	}
	if _, has := rep.Data["property"]; has {
		t.Error("no property without a Search Console sign-in")
	}
	if f.count("GetQueryStats") != 1 || f.count("GetLinkCounts") != 1 {
		t.Errorf("calls %v", f.bingCalls)
	}
}

func TestBothMerged(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.bingKey(t)
	rep, err := Mine(context.Background(), "pages", "example.com", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != "gsc+bing" || len(rep.Warnings) != 0 {
		t.Errorf("source %q warnings %v", rep.Source, rep.Warnings)
	}
	if n := len(rowsOf(t, rep.Data["pages"])); n != 3 {
		t.Errorf("gsc pages %d", n)
	}
	b := rep.Data["bing"].(map[string]any)
	if r := rowsOf(t, b["pages"]); len(r) != 1 || r[0].Key != "https://www.example.com/" {
		t.Errorf("bing pages %+v", r)
	}
	// drops and almost carry Bing too.
	rep, _ = Mine(context.Background(), "drops", "example.com", Options{})
	bd := dropsOf(t, rep.Data["bing"].(map[string]any)["queries"])
	if len(bd) != 2 || bd[0].Key != "seo tools" || bd[0].ClicksDelta != -14 || bd[1].Key != "bing only phrase" {
		t.Errorf("bing drops %+v", bd)
	}
	rep, _ = Mine(context.Background(), "almost", "example.com", Options{MinAlmostImpressions: 100})
	ba := rowsOf(t, rep.Data["bing"].(map[string]any)["queries"])
	if len(ba) != 1 || ba[0].Key != "bing only phrase" {
		t.Errorf("bing almost %+v", ba)
	}
}

func TestBingErrorIsWarningWhenGSCWorked(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.bingKey(t)
	f.bingFail = true
	rep, err := Mine(context.Background(), "queries", "example.com", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != "gsc" || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "Bing") {
		t.Errorf("source %q warnings %v", rep.Source, rep.Warnings)
	}
	if _, has := rep.Data["bing"]; has || rep.Data["queries"] == nil {
		t.Errorf("data %v", rep.Data)
	}
}

func TestGSCFailureIsWarningWhenBingWorked(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.bingKey(t)
	f.gscStatus = 403
	rep, err := Mine(context.Background(), "queries", "example.com", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != "bing" || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "Search Console") {
		t.Errorf("source %q warnings %v", rep.Source, rep.Warnings)
	}
}

func TestBothFailing(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.bingKey(t)
	f.bingFail = true
	f.gscStatus = 403
	if _, err := Mine(context.Background(), "queries", "example.com", Options{}); code(err) != apperr.Forbidden {
		t.Errorf("got %v", err)
	}
	// Neither side knows the site: not_found that mentions both.
	f.gscStatus, f.bingFail = 0, false
	_, err := Mine(context.Background(), "queries", "unverified-site.dev", Options{})
	if code(err) != apperr.NotFound || !errors.Is(err, ErrNotVerified) || !strings.Contains(err.Error(), "Bing") {
		t.Errorf("got %v", err)
	}
	// Not signed in and Bing failing: the Bing error, not a login request.
	f.mem.Delete(config.SecretOAuthToken)
	f.bingFail = true
	if _, err := Mine(context.Background(), "queries", "example.com", Options{NoCache: true}); code(err) != apperr.Forbidden {
		t.Errorf("got %v", err)
	}
}

func TestCacheReuse(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	if _, err := Mine(context.Background(), "queries", "example.com", Options{}); err != nil {
		t.Fatal(err)
	}
	n := len(f.queryBody)
	rep, err := Mine(context.Background(), "queries", "example.com", Options{})
	if err != nil || !rep.Cached || len(f.queryBody) != n {
		t.Errorf("cached=%v calls %d->%d err %v", rep != nil && rep.Cached, n, len(f.queryBody), err)
	}
}

func TestInvalidOptions(t *testing.T) {
	newEnv(t)
	for _, o := range []Options{{Days: 500}, {Limit: -1}, {Days: -3}} {
		if _, err := Mine(context.Background(), "queries", "example.com", o); code(err) != apperr.InvalidInput {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestSummary(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.bingKey(t)
	s, err := Summary(context.Background(), "https://www.example.com/", 28)
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "gsc+bing" || s.Property != "sc-domain:example.com" || len(s.Queries) != 6 || len(s.Pages) != 3 ||
		len(s.Almost) != 3 || len(s.Drops.Queries) != 3 || s.Bing == nil || s.Bing.Links == nil || s.Bing.Links.TotalLinks != 29 {
		t.Errorf("%+v", s)
	}
	// Four Search Console calls feed everything: query and page, both windows.
	if len(f.queryBody) != 4 {
		t.Errorf("gsc calls %d", len(f.queryBody))
	}
}

func TestSummarySkipsCleanly(t *testing.T) {
	f := newEnv(t)
	if _, err := Summary(context.Background(), "example.com", 28); !errors.Is(err, ErrNotSignedIn) || code(err) != apperr.LoginRequired {
		t.Errorf("signed out: %v", err)
	}
	f.signIn()
	if _, err := Summary(context.Background(), "unverified-site.dev", 28); !errors.Is(err, ErrNotVerified) || code(err) != apperr.NotFound {
		t.Errorf("no property: %v", err)
	}
}

func TestContextCancelIsTimeout(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Mine(ctx, "queries", "example.com", Options{}); code(err) != apperr.Timeout {
		t.Errorf("got %v", err)
	}
}

func TestBingKeywordStats(t *testing.T) {
	f := newEnv(t)
	if _, err := BingKeywordStats(context.Background(), "seo tools", "", ""); code(err) != apperr.NeedsAPIKey {
		t.Errorf("no key: %v", err)
	}
	f.bingKey(t)
	ks, err := BingKeywordStats(context.Background(), "seo tools", "us", "en-US")
	if err != nil || len(ks.Points) != 2 || ks.From != "2026-09-22" || ks.To != "2026-10-20" || ks.Points[1].Impressions != 400 || ks.Points[1].Broad != 900 || ks.Note == "" {
		t.Errorf("%+v %v", ks, err)
	}
	q := f.bingQuery[len(f.bingQuery)-1]
	if q.Get("q") != "seo tools" || q.Has("startDate") || q.Get("country") != "us" {
		t.Errorf("query %v", q)
	}
}

func TestVerify(t *testing.T) {
	f := newEnv(t)
	v, err := Verify(context.Background(), "https://www.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if v.Google.SignedIn || v.Next != LoginFix || len(v.Steps) != 4 || !strings.Contains(v.Steps[0].How[0], "Domain") {
		t.Errorf("%+v", v)
	}
	joined, _ := json.Marshal(v.Steps)
	for _, want := range []string{"TXT", "HTML file", "HTML tag", "Import", "seox key set bing", LoginFix} {
		if !strings.Contains(string(joined), want) {
			t.Errorf("steps lack %q", want)
		}
	}
	f.signIn()
	f.bingKey(t)
	v, _ = Verify(context.Background(), "example.com")
	if !v.Google.IsProperty || v.Google.Property != "sc-domain:example.com" || v.Google.Permission != "siteOwner" ||
		!v.Bing.IsSite || !v.Bing.IsVerified || v.Next != "seox mine queries example.com" {
		t.Errorf("%+v", v)
	}
	v, _ = Verify(context.Background(), "pending.net")
	if v.Google.IsProperty || v.Google.Permission != "siteUnverifiedUser" || !strings.Contains(v.Next, "step 1") {
		t.Errorf("%+v", v)
	}
	f.gscStatus = 403
	v, err = Verify(context.Background(), "example.com")
	if err != nil || len(v.Warnings) == 0 {
		t.Errorf("a failed lookup is a warning: %v %+v", err, v)
	}
}

func TestHandlersRegistered(t *testing.T) {
	for _, n := range []string{"login", "logout", "mine.queries", "mine.pages", "mine.drops", "mine.almost", "setup.verify"} {
		if !ops.Implemented(n) {
			t.Errorf("%s has no handler", n)
		}
	}
	f := newEnv(t)
	f.signIn()
	res, err := ops.HandlerFor("mine.queries")(context.Background(), ops.Input{Op: ops.Find("mine.queries"),
		Values: map[string]any{"site": "example.com", "days": 28, "limit": 2}})
	if err != nil || res.Meta.Source != "gsc" {
		t.Fatalf("%v %+v", err, res)
	}
}

// Nothing secret may land on disk: only the (in-memory) Keychain holds it.
func TestNoSecretsOnDisk(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	f.bingKey(t)
	if _, err := Mine(context.Background(), "queries", "example.com", Options{}); err != nil {
		t.Fatal(err)
	}
	_ = filepath.WalkDir(config.Home(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, secret := range []string{"test-secret", "new-access", "refresh-1", "bing-test-key"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s contains %q", p, secret)
			}
		}
		return nil
	})
}

func TestBingKeyNeverInErrors(t *testing.T) {
	newEnv(t)
	t.Setenv("SEOX_BING_KEY", "LEAKME-123")
	restore := web.OverrideBase("bing", "http://127.0.0.1:1/json")
	defer restore()
	_, err := Mine(context.Background(), "queries", "example.com", Options{NoCache: true})
	if err == nil || strings.Contains(err.Error(), "LEAKME") {
		t.Errorf("got %v", err)
	}
	v, _ := Verify(context.Background(), "example.com")
	if b, _ := json.Marshal(v); strings.Contains(string(b), "LEAKME") {
		t.Errorf("verify leaks: %s", b)
	}
}

func TestLinkCountsReadLinksField(t *testing.T) {
	f := newEnv(t)
	f.bingKey(t)
	b, err := newBing(context.Background(), "example.com", 28)
	if err != nil {
		t.Fatal(err)
	}
	l, err := b.links(context.Background(), 10)
	if err != nil || l.TotalLinks != 29 || l.PagesWithLinks != 2 {
		t.Errorf("%+v %v", l, err)
	}
}
