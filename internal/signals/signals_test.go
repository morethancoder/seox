package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/quota"
	"github.com/morethancoder/seox/internal/web"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	t.Setenv("SEOX_GOOGLE_API_KEY", "")
	t.Setenv("SEOX_OPENPAGERANK_KEY", "")
	t.Cleanup(web.SetMinGap(0))
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/signals/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeLighthouse installs a script that records its args and prints the fixture (or sleeps).
func fakeLighthouse(t *testing.T, body string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = dir + "/args"
	script := dir + "/lighthouse"
	src := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\n" + body + "\n"
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEOX_LIGHTHOUSE", script)
	t.Setenv("CHROME_PATH", script)
	return argsFile
}

func catFixture(t *testing.T) string {
	p, _ := filepath.Abs("../../testdata/signals/lighthouse.json")
	return "cat " + p
}

func TestSpeedNoKeyUsesLocalLighthouseNeverPSI(t *testing.T) {
	setup(t)
	args := fakeLighthouse(t, catFixture(t))
	var psiCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&psiCalls, 1) }))
	defer srv.Close()
	defer web.OverrideBase("psi", srv.URL)()
	res, err := Speed(context.Background(), "example.com", SpeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if psiCalls != 0 || res.QuotaUsed != 0 {
		t.Fatalf("PSI called %d times, quota %d", psiCalls, res.QuotaUsed)
	}
	if res.Mobile == nil || res.Desktop == nil || res.Crux != nil || res.Mobile.Source != "lighthouse-local" {
		t.Fatalf("unexpected result %+v", res)
	}
	m := res.Mobile
	if *m.Score != 87 || *m.Lab.LCPms != 2345.6 || m.Interaction.Metric != "TBT" {
		t.Errorf("bad parse: score %d lab %+v int %+v", *m.Score, m.Lab, m.Interaction)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != noKeyWarning {
		t.Errorf("warnings %v", res.Warnings)
	}
	// M3: insight audit ranked by LCP savings first; a passing insight is ignored.
	if m.Opportunities[0].ID != "render-blocking-insight" || m.Opportunities[0].SavingsMs != 1500 || m.Opportunities[1].ID != "b" {
		t.Errorf("opportunities %+v", m.Opportunities)
	}
	for _, o := range m.Opportunities {
		if o.ID == "ok-insight" {
			t.Error("passing insight listed")
		}
	}
	b, _ := os.ReadFile(args)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "https://example.com ") || strings.Contains(lines[0], "preset") || !strings.Contains(lines[1], "--preset=desktop") ||
		!strings.Contains(lines[0], "--output=json --output-path=stdout --quiet --chrome-flags=--headless=new") || !strings.Contains(lines[0], "--only-categories=performance") {
		t.Errorf("argv %q", lines)
	}
	if _, err := Speed(context.Background(), "example.com", SpeedOptions{}); err != nil {
		t.Error(err)
	}
	if b2, _ := os.ReadFile(args); string(b2) != string(b) {
		t.Error("second run was not cached")
	}
}

func TestSpeedLighthouseTimeout(t *testing.T) {
	setup(t)
	fakeLighthouse(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Speed(ctx, "https://example.com", SpeedOptions{Strategy: "mobile"})
	if apperr.From(err).Code != apperr.Timeout || time.Since(start) > 10*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

func TestSpeedToolMissing(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_LIGHTHOUSE", "/bin/true")
	t.Setenv("CHROME_PATH", "/nonexistent/chrome")
	_, err := Speed(context.Background(), "https://example.com", SpeedOptions{Strategy: "mobile"})
	if apperr.From(err).Code != apperr.ToolMissing {
		t.Fatalf("got %v", err)
	}
}

func TestSpeedKeyedPSI(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_GOOGLE_API_KEY", "K")
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(fixture(t, "psi.json"))
	}))
	defer srv.Close()
	crux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer crux.Close()
	defer web.OverrideBase("psi", srv.URL)()
	defer web.OverrideBase("crux", crux.URL)()
	res, err := Speed(context.Background(), "example.com", SpeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Mobile
	if m.Source != "psi" || *m.Score != 87 || m.Interaction.Metric != "INP" || m.Field["cls"].P75 != 0.12 || m.FieldScope != "origin" {
		t.Errorf("bad parse %+v", m)
	}
	if calls != 2 || res.QuotaUsed != 2 {
		t.Errorf("calls %d quota %d", calls, res.QuotaUsed)
	}
}

func TestSpeed429KeylessOrBurstDoesNotPoisonQuota(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_GOOGLE_API_KEY", "K")
	fakeLighthouse(t, catFixture(t))
	body := `{"error":{"code":429,"message":"Quota exceeded"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429); fmt.Fprint(w, body) }))
	defer srv.Close()
	crux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer crux.Close()
	defer web.OverrideBase("psi", srv.URL)()
	defer web.OverrideBase("crux", crux.URL)()
	res, err := Speed(context.Background(), "https://example.com", SpeedOptions{Strategy: "mobile"})
	if err != nil || res.Mobile.Source != "lighthouse-local" || res.QuotaLeft < psiDailyCap-1 {
		t.Fatalf("%v %+v left %d", err, res, res.QuotaLeft)
	}
	// falls back with a warning
	if !strings.Contains(strings.Join(res.Warnings, " "), "PageSpeed Insights failed") {
		t.Errorf("warnings %v", res.Warnings)
	}
	body = `{"error":{"details":[{"violations":[{"quotaId":"defaultPerDayPerProject"}]}]}}`
	if _, err := Speed(context.Background(), "https://example.com/x", SpeedOptions{Strategy: "mobile"}); err != nil {
		t.Fatal(err)
	}
	if left := quota.New(psiQuotaName, psiDailyCap).Left(); left != 0 {
		t.Errorf("keyed per-day 429 should exhaust the bucket, left %d", left)
	}
}

func TestSpeedCancelDuringCruxNotCached(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_GOOGLE_API_KEY", "K")
	psi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t, "psi.json")) }))
	defer psi.Close()
	ctx, cancel := context.WithCancel(context.Background())
	crux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cancel(); time.Sleep(200 * time.Millisecond) }))
	defer crux.Close()
	defer web.OverrideBase("psi", psi.URL)()
	defer web.OverrideBase("crux", crux.URL)()
	_, err := Speed(ctx, "https://example.com", SpeedOptions{Strategy: "mobile"})
	if apperr.From(err).Code != apperr.Timeout {
		t.Fatalf("got %v", err)
	}
	// nothing cached: a fresh call reaches PSI again
	var calls int32
	psi2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(fixture(t, "psi.json"))
	}))
	defer psi2.Close()
	defer web.OverrideBase("psi", psi2.URL)()
	crux2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer crux2.Close()
	defer web.OverrideBase("crux", crux2.URL)()
	if _, err := Speed(context.Background(), "https://example.com", SpeedOptions{Strategy: "mobile"}); err != nil || calls != 1 {
		t.Fatalf("%v calls %d", err, calls)
	}
}

func TestCrux400IsNotAKeyProblem(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	defer web.OverrideBase("crux", srv.URL)()
	_, _, err := cruxQuery(context.Background(), "queryRecord", "k", map[string]string{"url": "x"})
	if apperr.From(err).Code != apperr.InvalidInput || strings.Contains(err.Error(), "key") {
		t.Fatalf("%v", err)
	}
}

func TestSpeedTBTWhenNoINP(t *testing.T) {
	raw := psiResponse{}
	if err := json.Unmarshal(fixture(t, "psi.json"), &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw.OriginLoadingExp.Metrics, "INTERACTION_TO_NEXT_PAINT")
	r := parsePSI(&raw, "mobile")
	if r.Interaction.Metric != "TBT" || r.Interaction.Value != 120 || r.Interaction.Source != "lab" {
		t.Errorf("%+v", r.Interaction)
	}
}

func TestSpeedBadStrategy(t *testing.T) {
	setup(t)
	if _, err := Speed(context.Background(), "example.com", SpeedOptions{Strategy: "tablet"}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("got %v", err)
	}
}

func TestSpeedCruxFallbackToOriginAndTrend(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_GOOGLE_API_KEY", "SECRETKEY")
	psi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "SECRETKEY" {
			t.Error("key not sent to PSI")
		}
		w.Write(fixture(t, "psi.json"))
	}))
	defer psi.Close()
	crux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if _, page := body["url"]; page {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":404}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "queryHistoryRecord") {
			w.Write(fixture(t, "crux_history.json"))
			return
		}
		w.Write(fixture(t, "crux.json"))
	}))
	defer crux.Close()
	defer web.OverrideBase("psi", psi.URL)()
	defer web.OverrideBase("crux", crux.URL)()
	res, err := Speed(context.Background(), "https://example.com/page", SpeedOptions{Strategy: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Crux
	if c == nil || c.Scope != "origin" || c.Metrics["lcp"].P75 != 2800 || c.Metrics["lcp"].Rating != "needs-improvement" || c.Metrics["cls"].P75 != 0.05 || c.Metrics["cls"].Good != 0.9 {
		t.Fatalf("crux %+v", c)
	}
	if c.Trend["lcp"].Direction != "improving" || c.Trend["cls"].Direction != "stable" {
		t.Errorf("trend %+v", c.Trend)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "SECRETKEY") {
		t.Error("key leaked")
	}
}

func TestAuthorityNoKeyWayback(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("showNumPages") != "":
			fmt.Fprint(w, "4\n")
		case q.Get("limit") == "1":
			fmt.Fprint(w, "[[\"timestamp\"],\n[\"20020120142510\"]]\n")
		default:
			fmt.Fprint(w, "20020120142510\n20030120142510\n20040120142510\n")
		}
	}))
	defer srv.Close()
	defer web.OverrideBase("wayback", srv.URL)()
	res, err := Authority(context.Background(), []string{"https://www.Example.com/x", "example.com"}, AuthorityOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Domains) != 1 {
		t.Fatalf("dedupe: %+v", res.Domains)
	}
	d := res.Domains[0]
	if d.Domain != "example.com" || d.Score != nil || d.FirstSeen != "2002-01-20" || *d.SnapshotsApprox != 12 || d.WaybackPages != 4 {
		t.Errorf("%+v", d)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "openpagerank") {
		t.Errorf("warnings %v", res.Warnings)
	}
}

func TestAuthorityOPRBatchesOf100(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_OPENPAGERANK_KEY", "k")
	var calls int32
	opr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Error("no bearer")
		}
		var b struct{ Domains []string }
		json.NewDecoder(r.Body).Decode(&b)
		if len(b.Domains) > 100 {
			t.Errorf("batch of %d", len(b.Domains))
		}
		var rs []string
		for _, d := range b.Domains {
			rs = append(rs, fmt.Sprintf(`{"domain":%q,"found":true,"open_page_rank":4.5,"rank":99}`, d))
		}
		fmt.Fprintf(w, `{"count":%d,"results":[%s]}`, len(rs), strings.Join(rs, ","))
	}))
	defer opr.Close()
	wb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") }))
	defer wb.Close()
	defer web.OverrideBase("openpagerank", opr.URL)()
	defer web.OverrideBase("wayback", wb.URL)()
	var ds []string
	for i := 0; i < 150; i++ {
		ds = append(ds, fmt.Sprintf("site%d.com", i))
	}
	res, err := Authority(context.Background(), ds, AuthorityOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || res.Domains[149].Score == nil || *res.Domains[149].Score != 4.5 || res.Domains[0].Archived {
		t.Errorf("calls %d, %+v", calls, res.Domains[149])
	}
}

func TestIdeasGroupsAndBounds(t *testing.T) {
	setup(t)
	var gcalls, bcalls int32
	g := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gcalls, 1)
		q := r.URL.Query()
		if q.Get("hl") != "de" || q.Get("gl") != "at" || q.Get("client") != "firefox" {
			t.Errorf("params %v", q)
		}
		fmt.Fprintf(w, `["%s",["%s tool","how to %s","%s vs sem","best %s free","%s tools","%s tools"]]`, q.Get("q"), q.Get("q"), q.Get("q"), q.Get("q"), q.Get("q"), q.Get("q"), q.Get("q"))
	}))
	defer g.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bcalls, 1)
		if r.URL.Query().Get("mkt") != "de-AT" {
			t.Errorf("mkt %v", r.URL.Query())
		}
		fmt.Fprint(w, `["x",["seo audit checklist","seo audit meaning"]]`)
	}))
	defer b.Close()
	defer web.OverrideBase("google-suggest", g.URL)()
	defer web.OverrideBase("bing-suggest", b.URL)()
	res, err := Ideas(context.Background(), "SEO  Audit", IdeasOptions{Lang: "de", Country: "AT", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if gcalls+bcalls > 60 || gcalls != 40 || bcalls != 14 {
		t.Errorf("calls g=%d b=%d", gcalls, bcalls)
	}
	if res.Note != "No search volumes: these are suggestions people type, ranked by the engines' own order." {
		t.Error(res.Note)
	}
	if len(res.Questions) != 3 || len(res.Comparisons) == 0 || len(res.Modifiers) != 3 || res.Total < 10 {
		t.Errorf("groups q%d c%d m%d r%d total %d", len(res.Questions), len(res.Comparisons), len(res.Modifiers), len(res.Related), res.Total)
	}
	seen := map[string]bool{}
	for _, grp := range [][]Idea{res.Questions, res.Comparisons, res.Modifiers, res.Related} {
		for _, i := range grp {
			if seen[i.Text] {
				t.Errorf("duplicate %q", i.Text)
			}
			seen[i.Text] = true
		}
	}
	// cached
	if _, err := Ideas(context.Background(), "seo audit", IdeasOptions{Lang: "de", Country: "at"}); err != nil || gcalls != 40 {
		t.Errorf("cache miss: %v calls %d", err, gcalls)
	}
}

func TestIdeasOneEngineDown(t *testing.T) {
	setup(t)
	g := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `["q",["seo audit tool"]]`) }))
	defer g.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer b.Close()
	defer web.OverrideBase("google-suggest", g.URL)()
	defer web.OverrideBase("bing-suggest", b.URL)()
	res, err := Ideas(context.Background(), "seo audit", IdeasOptions{})
	if err != nil || len(res.Warnings) != 1 || res.Engines["bing"].Requests != 1 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestAuthorityMissingOPRResultNotCached(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_OPENPAGERANK_KEY", "k")
	var calls int32
	opr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, `{"count":1,"results":[{"domain":"a.com","found":true,"open_page_rank":3,"rank":5}],"invalid":["b.com"]}`)
	}))
	defer opr.Close()
	wb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") }))
	defer wb.Close()
	defer web.OverrideBase("openpagerank", opr.URL)()
	defer web.OverrideBase("wayback", wb.URL)()
	for i := 0; i < 2; i++ {
		if _, err := Authority(context.Background(), []string{"a.com", "b.com"}, AuthorityOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 { // b.com was never cached, so the second run asks again (a.com is cached)
		t.Errorf("calls %d", calls)
	}
}

func TestSuggestLatin1(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=ISO-8859-1")
		w.Write([]byte("[\"q\",[\"caf\xe9 \xfcber\"]]"))
	}))
	defer srv.Close()
	got, err := suggest(context.Background(), srv.URL)
	if err != nil || len(got) != 1 || got[0] != "café über" {
		t.Fatalf("%v %q", err, got)
	}
}
