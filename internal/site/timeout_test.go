package site

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/web"
)

func TestSVGTitleNotCounted(t *testing.T) {
	p := &Page{}
	parsePage(p, readFixture(t, "svgtitle.html"), "https://t.test/")
	if p.TitleCount != 1 || p.Title != "Icons page with svg titles" {
		t.Errorf("title_count %d title %q", p.TitleCount, p.Title)
	}
	// A real second HTML title is still counted.
	p = &Page{}
	parsePage(p, []byte("<html><head><title>One</title></head><body><title>Two</title></body></html>"), "https://t.test/")
	if p.TitleCount != 2 {
		t.Errorf("two html titles counted as %d", p.TitleCount)
	}
}

// slowSite serves a page with svg titles, a link to a slow page and links to a
// slow and a missing outside host. Slow answers outlast the shortened timeouts.
func slowSite(t *testing.T) (slowHits *atomic.Int32) {
	t.Helper()
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	slowHits = new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]
		switch {
		case r.URL.Path == "/robots.txt":
			w.WriteHeader(404)
		case host == "slow-ext.test" || r.URL.Path == "/slow":
			slowHits.Add(1)
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
			w.WriteHeader(200)
		case host == "ext.test":
			w.WriteHeader(404)
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			w.Write(readFixture(t, "svgtitle.html"))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	hp := strings.TrimPrefix(srv.URL, "http://")
	for _, h := range []string{"t.test", "slow-ext.test", "ext.test"} {
		t.Cleanup(web.RewriteHost(h, hp))
	}
	t.Cleanup(web.SetMinGap(0))
	oldL, oldP, oldR := linkTimeout, pageTimeout, retryPause
	linkTimeout, pageTimeout, retryPause = 150*time.Millisecond, 150*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { linkTimeout, pageTimeout, retryPause = oldL, oldP, oldR })
	return slowHits
}

func TestTimeoutsAreNotBroken(t *testing.T) {
	hits := slowSite(t)
	r, err := Audit(context.Background(), "t.test", AuditOptions{Max: 10, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if is := issueByID(r, "title-multiple"); is != nil {
		t.Errorf("svg titles flagged: %+v", is)
	}
	if is := issueByID(r, "broken-link-internal"); is != nil {
		t.Errorf("a timed-out internal link counted as broken: %+v", is)
	}
	if is := issueByID(r, "page-unreachable"); is != nil {
		t.Errorf("a timed-out page counted as unreachable: %+v", is)
	}
	pt := issueByID(r, "page-timeout")
	if pt == nil || pt.Severity != Low {
		t.Errorf("page-timeout %+v", pt)
	}
	lt := issueByID(r, "link-unchecked-timeout")
	if lt == nil || lt.Severity != Low {
		t.Fatalf("link-unchecked-timeout missing: %+v", r.Issues)
	}
	// The home page holds the slow internal and outside links.
	if len(lt.Examples) < 2 {
		t.Errorf("examples %v", lt.Examples)
	}
	be := issueByID(r, "broken-link-external")
	if be == nil || len(be.Examples) != 1 || !strings.Contains(be.Examples[0], "ext.test/gone") {
		t.Errorf("only the 404 outside link is broken: %+v", be)
	}
	warned := false
	for _, w := range r.Warnings {
		warned = warned || strings.Contains(w, "timed out")
	}
	if !warned {
		t.Errorf("no timeout warning: %v", r.Warnings)
	}
	if hits.Load() < 4 {
		t.Errorf("expected a retry of each slow URL, got %d slow hits", hits.Load())
	}
}

func TestCrawlDelayWarningOnce(t *testing.T) {
	t.Cleanup(func() { web.SetCrawlDelay("d.test", 0); web.SetCrawlDelay("www.d.test", 0) })
	c := &crawler{res: &CrawlResult{}, rules: parseRobots("User-agent: *\nCrawl-delay: 60\n")}
	c.applyDelay("d.test")
	c.applyDelay("www.d.test")
	c.applyDelay("d.test")
	if n := len(c.res.Warnings); n != 1 {
		t.Errorf("want 1 crawl-delay warning, got %d: %v", n, c.res.Warnings)
	}
}

// An apex whose robots.txt has rules redirects to www, which has none: the
// summary must describe www, not the apex.
func TestRobotsSummaryAfterRedirect(t *testing.T) {
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]
		switch {
		case host == "r.test" && r.URL.Path == "/robots.txt":
			w.Write([]byte("User-agent: *\nDisallow: /private\nSitemap: https://r.test/apex-sitemap.xml\n"))
		case host == "r.test":
			http.Redirect(w, r, "https://www.r.test"+r.URL.Path, http.StatusMovedPermanently)
		case r.URL.Path == "/robots.txt":
			w.WriteHeader(404)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html lang=en><head><title>Home of www</title></head><body>hi</body></html>"))
		}
	}))
	t.Cleanup(srv.Close)
	hp := strings.TrimPrefix(srv.URL, "http://")
	t.Cleanup(web.RewriteHost("r.test", hp))
	t.Cleanup(web.RewriteHost("www.r.test", hp))
	t.Cleanup(web.SetMinGap(0))
	cr, err := Crawl(context.Background(), "r.test", CrawlOptions{Max: 3, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Robots.Found || len(cr.Robots.Sitemaps) != 0 {
		t.Errorf("robots summary still shows the apex rules: %+v", cr.Robots)
	}
}
