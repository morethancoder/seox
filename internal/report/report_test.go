package report

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/mine"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/rank"
	"github.com/morethancoder/seox/internal/signals"
	"github.com/morethancoder/seox/internal/site"
)

func pageWith(url string, terms ...string) *site.Page {
	p := &site.Page{URL: url, Status: 200, Parsed: true, Title: "T " + url, Description: "d", Lang: "en", WordCount: 300,
		Headings: []site.Heading{{Level: 1, Text: "h"}}}
	for i, t := range terms {
		p.Terms = append(p.Terms, site.Term{Term: t, Score: float64(100 - i)})
	}
	return p
}

func fakeCrawl(sites map[string][]string) func(context.Context, string, site.CrawlOptions) (*site.CrawlResult, error) {
	return func(_ context.Context, s string, _ site.CrawlOptions) (*site.CrawlResult, error) {
		d := signals.NormalizeDomain(s)
		terms, ok := sites[d]
		if !ok {
			return nil, apperr.New(apperr.NetworkError, "cannot reach %s", d)
		}
		return &site.CrawlResult{Site: "https://" + d + "/", Start: "https://" + d + "/", Host: d, Max: 10, HTTPSHome: true,
			Pages: []*site.Page{pageWith("https://"+d+"/", terms...)}, CrawledAt: time.Now()}, nil
	}
}

func intp(n int) *int       { return &n }
func fp(f float64) *float64 { return &f }

// setup swaps every dependency for a fake and restores them after the test.
func setup(t *testing.T) *deps {
	t.Helper()
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	old := dep
	d := &deps{
		crawl: fakeCrawl(map[string][]string{
			"mine.test":  {"seo audit", "site speed", "shared term"},
			"rival.test": {"shared term", "link building", "site speed", "rival only"},
			"other.test": {"link building", "other only"},
		}),
		speed: func(_ context.Context, u string, _ signals.SpeedOptions) (*signals.SpeedResult, error) {
			return &signals.SpeedResult{URL: u, Mobile: &signals.PSIResult{Strategy: "mobile", Score: intp(55), Lab: signals.Lab{LCPms: fp(3200), CLS: fp(0.1)},
				Opportunities: []signals.Opportunity{{ID: "x", Title: "Reduce unused JavaScript", SavingsMs: 900}}},
				Desktop: &signals.PSIResult{Strategy: "desktop", Score: intp(90)}}, nil
		},
		authority: func(_ context.Context, domains []string, _ signals.AuthorityOptions) (*signals.AuthorityResult, error) {
			r := &signals.AuthorityResult{Note: "note"}
			for _, d := range domains {
				r.Domains = append(r.Domains, signals.DomainAuthority{Domain: d, Score: fp(3.5), FirstSeen: "2015-01-02", SnapshotsApprox: intp(1200)})
			}
			return r, nil
		},
		ideas: func(_ context.Context, kw string, _ signals.IdeasOptions) (*signals.IdeasResult, error) {
			return &signals.IdeasResult{Keyword: kw, Questions: []signals.Idea{{Text: "what is " + kw}}}, nil
		},
		openRank: func(context.Context, *config.Config) (*rank.Instance, error) {
			return &rank.Instance{URL: "http://fake"}, nil
		},
		positions: func(_ context.Context, s string, kws []string, opt rank.Options) (*rank.Result, error) {
			if opt.Instance == nil {
				return nil, errors.New("positions must share the opened instance")
			}
			r := &rank.Result{Site: s}
			for _, k := range kws {
				r.Keywords = append(r.Keywords, rank.KeywordResult{Keyword: k, BestPosition: intp(14), Label: "not Google"})
			}
			return r, nil
		},
		mine: func(context.Context, string, mine.Options) (*mine.SummaryData, error) {
			return nil, mine.ErrNotSignedIn
		},
		loadConfig: func() (*config.Config, error) { return &config.Config{}, nil },
		now:        func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) },
	}
	dep = d
	t.Cleanup(func() { dep = old })
	return d
}

func input(op string, vals map[string]any) ops.Input {
	return ops.Input{Op: ops.Find(op), Values: vals, Given: map[string]bool{}}
}

func TestSlugHostile(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":          "example.com",
		"../../etc/passwd":     "etc-passwd",
		"a/b\\c:d":             "a-b-c-d",
		"..":                   "site",
		"":                     "site",
		"EXAMPLE.com\x00.evil": "example.com-.evil",
		"héllo.com":            "h-llo.com",
	} {
		got := slug(in)
		if got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, "/\\") || strings.Contains(got, "..") {
			t.Errorf("slug(%q) = %q is unsafe", in, got)
		}
	}
}

func TestWriteNewNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i, c := range []string{"one", "two", "three"} {
		p, err := writeNew(dir, "example.com", "2026-11-12", c)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Base(p))
		b, _ := os.ReadFile(p)
		if string(b) != c {
			t.Errorf("file %d holds %q", i, b)
		}
	}
	want := []string{"example.com-seo-2026-11-12.md", "example.com-seo-2026-11-12-2.md", "example.com-seo-2026-11-12-3.md"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("names = %v", paths)
	}
	// A hostile domain stays inside the folder.
	p, err := writeNew(dir, "../../evil", "2026-11-12", "x")
	if err != nil || filepath.Dir(p) != dir {
		t.Fatalf("hostile path %q err %v", p, err)
	}
}

func TestReportWritesNoteAndSkipsMine(t *testing.T) {
	setup(t)
	out := t.TempDir()
	res, err := handleReport(context.Background(), input("report", map[string]any{
		"site": "mine.test", "competitor": []string{"rival.test"}, "kw": []string{"seo audit"}, "out": out}))
	if err != nil {
		t.Fatal(err)
	}
	data := res.Data.(*ReportResult)
	if filepath.Base(data.Path) != "mine.test-seo-2026-11-12.md" || filepath.Dir(data.Path) != out {
		t.Fatalf("path %s", data.Path)
	}
	b, _ := os.ReadFile(data.Path)
	note := string(b)
	for _, want := range []string{"type: seo-report", "site: \"mine.test\"", "competitors:\n  - \"rival.test\"", "## Top 5 actions",
		"## Competitors", "**not Google**", "no search volumes", "Skipped: not signed in", "seox login", "## Ideas for the top 3"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q", want)
		}
	}
	if strings.Contains(note, "<") && strings.Contains(note, "<table") {
		t.Error("note has HTML")
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "real rankings skipped") {
		t.Errorf("warnings %v", res.Warnings)
	}
	// A second run gets -2.
	res2, err := handleReport(context.Background(), input("report", map[string]any{"site": "mine.test", "out": out}))
	if err != nil || filepath.Base(res2.Data.(*ReportResult).Path) != "mine.test-seo-2026-11-12-2.md" {
		t.Fatalf("second run: %v %+v", err, res2)
	}
}

func TestMineNotVerifiedSkipped(t *testing.T) {
	d := setup(t)
	d.mine = func(context.Context, string, mine.Options) (*mine.SummaryData, error) {
		return nil, apperr.Wrap(apperr.NotFound, mine.ErrNotVerified, "no property")
	}
	g, err := gather(context.Background(), gatherOpts{main: "mine.test", mine: true})
	if err != nil {
		t.Fatal(err)
	}
	if g.mine == nil || !strings.Contains(g.mine.Skipped, "seox setup verify mine.test") {
		t.Fatalf("mine = %+v", g.mine)
	}
}

func TestFailingSpeedIsAWarning(t *testing.T) {
	d := setup(t)
	d.speed = func(context.Context, string, signals.SpeedOptions) (*signals.SpeedResult, error) {
		return nil, apperr.New(apperr.QuotaExceeded, "PageSpeed Insights refused a keyless call (429)")
	}
	res, err := handleCompare(context.Background(), input("compare", map[string]any{"site": "mine.test", "competitor": []string{"rival.test"}}))
	if err != nil {
		t.Fatalf("a speed failure must not fail the command: %v", err)
	}
	cr := res.Data.(*CompareResult)
	if cr.Sites[0].Speed.NoData == "" || !strings.Contains(cr.Sites[0].Speed.NoData, "429") {
		t.Errorf("speed = %+v", cr.Sites[0].Speed)
	}
	if cr.Sites[0].Score == nil || cr.Sites[0].Authority.OPRScore == nil {
		t.Error("other sections must still be filled")
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "speed mine.test") {
		t.Errorf("warnings %v", res.Warnings)
	}
}

func TestCompetitorCrawlFailureIsAWarningMainFailureIsAnError(t *testing.T) {
	setup(t)
	res, err := handleCompare(context.Background(), input("compare", map[string]any{"site": "mine.test", "competitor": []string{"rival.test", "down.test"}}))
	if err != nil {
		t.Fatal(err)
	}
	cr := res.Data.(*CompareResult)
	if cr.Sites[2].Crawled || cr.Sites[2].Note == "" || !strings.Contains(strings.Join(res.Warnings, "\n"), "crawl down.test") {
		t.Errorf("down site: %+v warnings %v", cr.Sites[2], res.Warnings)
	}
	if _, err = handleCompare(context.Background(), input("compare", map[string]any{"site": "down.test", "competitor": []string{"rival.test"}})); err == nil {
		t.Fatal("main crawl failure must fail the command")
	}
}

func TestPositionsOpenOncePerCommand(t *testing.T) {
	d := setup(t)
	opens, stops := 0, 0
	d.openRank = func(context.Context, *config.Config) (*rank.Instance, error) {
		opens++
		return &rank.Instance{URL: "http://fake"}, nil
	}
	_ = stops
	res, err := handleCompare(context.Background(), input("compare", map[string]any{"site": "mine.test", "competitor": []string{"rival.test", "other.test"}, "kw": []string{"a", "b"}}))
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Errorf("SearXNG opened %d times", opens)
	}
	for _, sd := range res.Data.(*CompareResult).Sites {
		if len(sd.Positions) != 2 {
			t.Errorf("%s positions %+v", sd.Domain, sd.Positions)
		}
	}
	if !strings.Contains(res.Meta.Source, "searxng") || !strings.Contains(res.Meta.Source, "crawl") {
		t.Errorf("source %q", res.Meta.Source)
	}
}

func TestNoSearxngIsAWarning(t *testing.T) {
	d := setup(t)
	d.openRank = func(context.Context, *config.Config) (*rank.Instance, error) {
		return nil, apperr.New(apperr.ToolMissing, "no SearXNG found")
	}
	res, err := handleCompare(context.Background(), input("compare", map[string]any{"site": "mine.test", "competitor": []string{"rival.test"}, "kw": []string{"a"}}))
	if err != nil || !strings.Contains(strings.Join(res.Warnings, "\n"), "positions skipped") {
		t.Fatalf("%v %v", err, res)
	}
}

func TestTopActionsOrdering(t *testing.T) {
	a := &site.AuditResult{Issues: []site.Issue{
		{ID: "low", Severity: site.Low, Summary: "low", Fix: "f", Count: 9, Points: 1},
		{ID: "big", Severity: site.High, Summary: "big", Fix: "f", Count: 4, Points: 14},
		{ID: "mid", Severity: site.Medium, Summary: "mid", Fix: "f", Count: 2, Points: 6},
		{ID: "mid2", Severity: site.Medium, Summary: "mid2", Fix: "f", Count: 3, Points: 6},
		{ID: "tiny", Severity: site.Low, Summary: "tiny", Fix: "f", Count: 1, Points: 0.5},
	}}
	sp := &signals.SpeedResult{Mobile: &signals.PSIResult{Score: intp(40), Opportunities: []signals.Opportunity{{Title: "Cut JS", SavingsMs: 1000}}}} // 15
	wins := []almostWin{{"q1", 12}, {"q2", 15}, {"q3", 18}}                                                                                           // 7
	got := topActions(a, sp, wins, 5)
	var ids []string
	for _, x := range got {
		ids = append(ids, x.ID)
	}
	want := []string{"speed-mobile", "big", "almost-page-1", "mid", "mid2"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
	if got[0].Rank != 1 || got[4].Rank != 5 || got[1].Affected != 4 || got[2].Affected != 3 || got[1].Fix == "" {
		t.Errorf("fields: %+v", got)
	}
	// A fast mobile page and no wins add nothing.
	fast := &signals.SpeedResult{Mobile: &signals.PSIResult{Score: intp(95)}}
	if g := topActions(a, fast, nil, 5); len(g) != 5 || g[0].ID != "big" {
		t.Errorf("fast: %+v", g)
	}
}

func TestCompareKeywords(t *testing.T) {
	mk := func(name string, terms ...string) namedTerms {
		n := namedTerms{name: name}
		for i, t := range terms {
			n.terms = append(n.terms, site.SiteKeyword{Term: t, Score: float64(100 - i)})
		}
		return n
	}
	kc := compareKeywords([]namedTerms{
		mk("me", "alpha", "beta", "gamma"),
		mk("r1", "beta", "delta", "eps", "zeta"),
		mk("r2", "delta", "gamma", "eta"),
	})
	by := map[string]SiteKeywords{}
	for _, s := range kc.Sites {
		by[s.Site] = s
	}
	check := func(name string, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("me shared", by["me"].Shared, []string{"beta", "gamma"})
	check("me unique", by["me"].Unique, []string{"alpha"})
	check("r1 shared", by["r1"].Shared, []string{"beta"})
	check("r1 unique", by["r1"].Unique, []string{"eps", "zeta"})
	check("r2 shared", by["r2"].Shared, []string{"gamma"})
	check("r2 unique", by["r2"].Unique, []string{"eta"})
	// delta is held by both competitors, so it leads the gaps.
	var gaps []string
	for _, g := range kc.Gaps {
		gaps = append(gaps, g.Term)
	}
	check("gaps", gaps, []string{"delta", "eps", "eta", "zeta"})
	if !reflect.DeepEqual(kc.Gaps[0].Sites, []string{"r1", "r2"}) {
		t.Errorf("delta sites %v", kc.Gaps[0].Sites)
	}
}

func TestCompareRespectsCancel(t *testing.T) {
	setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := dep
	d.crawl = func(ctx context.Context, _ string, _ site.CrawlOptions) (*site.CrawlResult, error) {
		return nil, apperr.From(ctx.Err())
	}
	_, err := handleCompare(ctx, input("compare", map[string]any{"site": "mine.test", "competitor": []string{"rival.test"}}))
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.Timeout {
		t.Fatalf("err = %v", err)
	}
}

func TestSpeedSourceLabel(t *testing.T) {
	local := &signals.PSIResult{Strategy: "mobile", Source: "lighthouse-local"}
	psi := &signals.PSIResult{Strategy: "desktop", Source: "psi"}
	cases := []struct {
		sp   *signals.SpeedResult
		want string
	}{
		{&signals.SpeedResult{Mobile: local, Desktop: &signals.PSIResult{Source: "lighthouse-local"}}, "local Lighthouse on this machine, no real-user data"},
		{&signals.SpeedResult{Mobile: &signals.PSIResult{Source: "psi"}, Desktop: psi}, "PageSpeed Insights lab run on Google's servers"},
		{&signals.SpeedResult{Mobile: local, Desktop: psi}, "for one strategy, local Lighthouse for the other"},
	}
	for _, c := range cases {
		if got := summariseSpeed(c.sp).Source; !strings.Contains(got, c.want) {
			t.Errorf("source %q, want it to contain %q", got, c.want)
		}
	}
}

func TestPickIdeasTerms(t *testing.T) {
	mk := func(ts ...string) []site.SiteKeyword {
		var out []site.SiteKeyword
		for _, s := range ts {
			out = append(out, site.SiteKeyword{Term: s})
		}
		return out
	}
	got := pickIdeasTerms(mk("bot", "powertelebot", "telegram bot", "power tele bot", "auto reply", "one two three", "x y z w", "16 октября 2026", "menu"), "powertelebot")
	want := []string{"telegram bot", "auto reply", "one two three"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("phrases first, brand skipped, 3 max: got %v", got)
	}
	got = pickIdeasTerms(mk("bot", "telegram bot", "menu", "chat"), "x")
	if !reflect.DeepEqual(got, []string{"telegram bot", "bot", "menu"}) {
		t.Errorf("singles fill the gap: got %v", got)
	}
}

func TestIdeasLang(t *testing.T) {
	for term, want := range map[string]string{"تليجرام": "ar", "телеграм бот": "ru", "telegram bot": "", "bot ботик": "ru"} {
		if got := ideasLang(term); got != want {
			t.Errorf("ideasLang(%q) = %q, want %q", term, got, want)
		}
	}
}

func TestRunIdeasPassesLang(t *testing.T) {
	d := setup(t)
	var mu sync.Mutex
	langs := map[string]string{}
	d.ideas = func(_ context.Context, kw string, o signals.IdeasOptions) (*signals.IdeasResult, error) {
		mu.Lock()
		langs[kw] = o.Lang
		mu.Unlock()
		return &signals.IdeasResult{Keyword: kw}, nil
	}
	var wg sync.WaitGroup
	kw := &site.KeywordsResult{Keywords: []site.SiteKeyword{{Term: "تليجرام بوت"}, {Term: "телеграм бот"}, {Term: "telegram bot"}}}
	runIdeas(context.Background(), kw, "powertelebot.com", false, &wg)
	wg.Wait()
	if langs["تليجرام بوت"] != "ar" || langs["телеграм бот"] != "ru" || langs["telegram bot"] != "" {
		t.Errorf("langs %v", langs)
	}
}
