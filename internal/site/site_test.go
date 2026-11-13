package site

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/web"
)

// fixture serves testdata/site as "example.com", "www.example.com" and a second
// host "ext.test" for outside-link checks. It counts requests per path.
type fixture struct {
	srv   *httptest.Server
	mu    sync.Mutex
	hits  map[string]int
	heads atomic.Int32
}

func (f *fixture) count(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[k]
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "site", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	f := &fixture{hits: map[string]int{}}
	files := map[string]string{
		"/": "home.html", "/about": "about.html", "/blog/": "blog.html", "/blog/post-one": "post.html",
		"/contact": "contact.html", "/orphan": "orphan.html", "/ar": "ar.html",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]
		f.mu.Lock()
		f.hits[host+r.URL.Path]++
		f.hits[r.Method+" "+host+r.URL.Path]++
		f.mu.Unlock()
		if host == "ext.test" {
			switch r.URL.Path {
			case "/ok":
				w.WriteHeader(200)
			case "/fallback":
				if r.Method == http.MethodHead {
					w.WriteHeader(405)
					return
				}
				w.WriteHeader(200)
			default:
				w.WriteHeader(404)
			}
			return
		}
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Write(readFixture(t, "robots.txt"))
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			w.Write(readFixture(t, "sitemap.xml"))
		case "/redirect-old":
			http.Redirect(w, r, "/about", http.StatusMovedPermanently)
		case "/download":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("binary"))
		default:
			if name, ok := files[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(readFixture(t, name))
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(404)
			w.Write([]byte("<html><head><title>Not found</title></head><body>no</body></html>"))
		}
	}))
	t.Cleanup(f.srv.Close)
	hp := strings.TrimPrefix(f.srv.URL, "http://")
	for _, h := range []string{"example.com", "www.example.com", "ext.test", "other-site.test"} {
		t.Cleanup(web.RewriteHost(h, hp))
	}
	t.Cleanup(web.SetMinGap(0))
	return f
}

func pageByPath(cr *CrawlResult, suffix string) *Page {
	for _, p := range cr.Pages {
		if strings.HasSuffix(p.URL, suffix) {
			return p
		}
	}
	return nil
}

func issueByID(r *AuditResult, id string) *Issue {
	for i := range r.Issues {
		if r.Issues[i].ID == id {
			return &r.Issues[i]
		}
	}
	return nil
}

func TestCrawlAndAudit(t *testing.T) {
	f := newFixture(t)
	cr, err := Crawl(context.Background(), "example.com", CrawlOptions{Max: 50})
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]bool{}
	for _, p := range cr.Pages {
		urls[p.URL] = true
	}
	for _, want := range []string{"https://example.com/", "https://example.com/about", "https://example.com/blog/",
		"https://example.com/blog/post-one", "https://example.com/orphan", "https://example.com/redirect-old",
		"https://example.com/broken", "https://example.com/contact?a=1&b=2"} {
		if !urls[want] {
			t.Errorf("page %s not crawled; got %v", want, urls)
		}
	}
	// www and the bare domain are one site: contact is fetched once, with the tracking parameter dropped.
	if n := f.count("example.com/contact") + f.count("www.example.com/contact"); n != 2 {
		// contact?...a=1&b=2 and contact (www link, no query) are two distinct URLs
		t.Errorf("contact fetched %d times, want 2 (plain and with query)", n)
	}
	for u := range urls {
		if strings.Contains(u, "/secret/") || strings.HasSuffix(u, ".pdf") || strings.HasSuffix(u, "/download") {
			t.Errorf("should not have crawled %s", u)
		}
	}
	if f.count("example.com/secret/x") != 0 || f.count("example.com/files/catalog.pdf") != 0 {
		t.Error("robots-blocked or non-HTML file was fetched")
	}
	if len(cr.BlockedRobots) != 1 || !strings.HasSuffix(cr.BlockedRobots[0], "/secret/x") {
		t.Errorf("blocked by robots = %v", cr.BlockedRobots)
	}
	if cr.SkippedCount < 3 { // pdf, /download (octet-stream), /secret/x
		t.Errorf("skipped %d", cr.SkippedCount)
	}
	if cr.Robots.Group != "seox" || !cr.Robots.Found {
		t.Errorf("robots %+v", cr.Robots)
	}
	if !cr.Sitemap.Found || cr.Sitemap.URLs != 4 {
		t.Errorf("sitemap %+v (the other-site URL must be dropped)", cr.Sitemap)
	}
	if p := pageByPath(cr, "/orphan"); p == nil || !p.InSitemap {
		t.Error("orphan page should come from the sitemap")
	}
	ro := pageByPath(cr, "/redirect-old")
	if ro == nil || len(ro.Redirects) != 1 || ro.Redirects[0].Status != 301 || ro.Status != 301 {
		t.Errorf("redirect page %+v", ro)
	}
	if b := pageByPath(cr, "/broken"); b == nil || b.Status != 404 {
		t.Errorf("broken page %+v", b)
	}
	home := cr.Pages[0]
	if home.Depth != 0 || home.Title == "" || home.TimeMS < 0 || home.Bytes == 0 {
		t.Errorf("home %+v", home)
	}
	// External: HEAD first, GET fallback, cached.
	if st := cr.External["https://ext.test/ok"]; st.Status != 200 || st.Method != "HEAD" {
		t.Errorf("ext ok %+v", st)
	}
	if st := cr.External["https://ext.test/fallback"]; st.Status != 200 || st.Method != "GET" {
		t.Errorf("ext fallback %+v", st)
	}
	if st := cr.External["https://ext.test/gone"]; st.Status != 404 {
		t.Errorf("ext gone %+v", st)
	}
	if f.count("HEAD ext.test/fallback") != 1 || f.count("GET ext.test/fallback") != 1 {
		t.Error("fallback should send one HEAD then one GET")
	}

	r := AuditCrawl(cr)
	for _, id := range []string{"title-missing", "description-missing", "noindex", "broken-link-internal", "broken-link-external",
		"status-4xx", "image-alt-missing", "title-duplicate", "description-duplicate", "sitemap-url-error", "h1-multiple",
		"heading-skip", "canonical-missing", "jsonld-invalid", "hreflang-invalid", "thin-content", "lang-missing",
		"viewport-missing", "redirect-internal", "http-not-redirected", "blocked-by-robots", "og-missing", "structured-data-missing"} {
		if issueByID(r, id) == nil {
			t.Errorf("missing issue %s", id)
		}
	}
	if is := issueByID(r, "title-missing"); is != nil {
		if is.Severity != High || is.Fix == "" || is.Count != 1 || len(is.URLs) != 1 {
			t.Errorf("title-missing %+v", is)
		}
	}
	if is := issueByID(r, "broken-link-external"); is == nil || len(is.URLs) != 1 || !strings.Contains(is.Examples[0], "ext.test/gone") {
		t.Errorf("broken-link-external %+v", is)
	}
	if issueByID(r, "title-too-long") != nil {
		t.Error("no title is too long in the fixture")
	}
	if r.StructuredData["Organization"] != 1 || r.StructuredData["WebSite"] != 1 {
		t.Errorf("structured data types %v", r.StructuredData)
	}
	if r.Score <= 0 || r.Score >= 100 {
		t.Errorf("score %d", r.Score)
	}
	// Ranked: severities never go back up.
	rank := map[string]int{High: 0, Medium: 1, Low: 2}
	for i := 1; i < len(r.Issues); i++ {
		if rank[r.Issues[i].Severity] < rank[r.Issues[i-1].Severity] {
			t.Errorf("issues not ranked at %d", i)
		}
	}
	if r.Counts.High == 0 || r.PagesCrawled != len(cr.Pages) || r.ExternalLinks.Broken != 1 {
		t.Errorf("%+v %d", r.Counts, r.ExternalLinks.Broken)
	}
}

func TestCrawlLimitsAndCache(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cr, err := Crawl(ctx, "https://www.example.com", CrawlOptions{Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(cr.Pages) != 3 || !cr.Limited {
		t.Errorf("pages %d limited %v", len(cr.Pages), cr.Limited)
	}
	before := f.count("/robots.txt") + f.count("example.com/robots.txt") + f.count("www.example.com/robots.txt")
	cr2, err := Crawl(ctx, "example.com", CrawlOptions{Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !cr2.Cached {
		t.Error("second crawl with the same site and options should be cached (www and bare domain share a key)")
	}
	after := f.count("/robots.txt") + f.count("example.com/robots.txt") + f.count("www.example.com/robots.txt")
	if after != before {
		t.Error("cached crawl made requests")
	}
	if c3, _ := Crawl(ctx, "example.com", CrawlOptions{Max: 3, NoCache: true}); c3 == nil || c3.Cached {
		t.Error("NoCache must crawl again")
	}
	if c4, _ := Crawl(ctx, "example.com", CrawlOptions{Max: 4}); c4 == nil || c4.Cached {
		t.Error("another --max is another cache entry")
	}

	d, err := Crawl(ctx, "example.com", CrawlOptions{Max: 50, Depth: 1, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Pages {
		if p.Depth > 1 {
			t.Errorf("depth %d beyond --depth 1: %s", p.Depth, p.URL)
		}
	}
}

func TestRobotsBlocksStart(t *testing.T) {
	newFixture(t)
	_, err := Crawl(context.Background(), "https://example.com/secret/page", CrawlOptions{Max: 5})
	if err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Errorf("want a robots error, got %v", err)
	}
}

func TestPageAudit(t *testing.T) {
	newFixture(t)
	ctx := context.Background()
	// An audit first so inbound links are known.
	if _, err := Audit(ctx, "example.com", AuditOptions{Max: 200}); err != nil {
		t.Fatal(err)
	}
	r, err := PageAudit(ctx, "https://example.com/about", PageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, is := range r.Issues {
		ids[is.ID] = true
	}
	for _, id := range []string{"title-missing", "noindex", "h1-multiple", "image-alt-missing", "hreflang-invalid", "jsonld-invalid", "canonical-elsewhere"} {
		if id == "canonical-elsewhere" {
			continue // the canonical resolves to the page itself
		}
		if !ids[id] {
			t.Errorf("page issue %s missing; have %v", id, ids)
		}
	}
	if ids["title-duplicate"] || ids["sitemap-missing"] {
		t.Error("site-wide checks must not run for one page")
	}
	if len(r.Page.Headings) != 3 || r.Page.Images != 3 || r.Page.ImagesNoAlt != 1 || len(r.Page.JSONLD) != 1 || r.Page.JSONLD[0].Valid {
		t.Errorf("detail %+v", r.Page)
	}
	if len(r.Page.Metas) < 2 || r.Page.WordCount == 0 {
		t.Errorf("metas %d words %d", len(r.Page.Metas), r.Page.WordCount)
	}
	if len(r.LinksOut) != 1 || len(r.LinksIn) < 2 {
		t.Errorf("links out %d in %d (%s)", len(r.LinksOut), len(r.LinksIn), r.LinksInNote)
	}
	if len(r.Keywords) == 0 {
		t.Error("no keywords")
	}
	// Without a crawl, inbound links say how to get them.
	t.Setenv("SEOX_HOME", t.TempDir())
	r2, err := PageAudit(ctx, "https://example.com/blog/", PageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.LinksIn) != 0 || !strings.Contains(r2.LinksInNote, "seox audit") {
		t.Errorf("note %q", r2.LinksInNote)
	}
	if _, err := PageAudit(ctx, "https://example.com/download", PageOptions{}); err == nil {
		t.Error("a non-HTML URL should be an error")
	}
}

func TestKeywordsFromSite(t *testing.T) {
	newFixture(t)
	r, err := Keywords(context.Background(), "example.com", KeywordsOptions{Max: 50, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]SiteKeyword{}
	for _, k := range r.Keywords {
		by[k.Term] = k
	}
	w, ok := by["widget"]
	if !ok || w.PageCount < 2 {
		t.Fatalf("widget %+v", w)
	}
	if _, ok := by["navigation menu item"]; ok {
		t.Error("navigation anchors must not count")
	}
	if _, ok := by["privacy policy footer"]; ok {
		t.Error("footer anchors must not count")
	}
	if k, ok := by["handmade widget tools"]; !ok || k.Pages[0].URL != "https://example.com/" {
		t.Errorf("handmade widget tools %+v", k)
	}
	if k := by["handmade widget tools"]; len(k.In) == 0 || k.In[0] != "title" {
		t.Errorf("fields %v", k.In)
	}
	if _, ok := by["أدوات الويدجت"]; !ok {
		t.Errorf("Arabic bigram missing")
	}
	if r.Keywords[0].Score < r.Keywords[len(r.Keywords)-1].Score {
		t.Error("not sorted by score")
	}
}

func TestPartialOnDeadline(t *testing.T) {
	f := newFixture(t)
	_ = f
	restore := web.SetMinGap(80 * time.Millisecond)
	defer restore()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	cr, err := Crawl(ctx, "example.com", CrawlOptions{Max: 50})
	if err != nil {
		t.Skipf("deadline hit before the home page: %v", err)
	}
	if !cr.partial() {
		t.Skip("crawl finished in time")
	}
	cached, _ := Crawl(context.Background(), "example.com", CrawlOptions{Max: 50})
	if cached != nil && cached.Cached {
		t.Error("a partial crawl must not be cached")
	}
}

func TestHTTPSiteFlagged(t *testing.T) {
	newFixture(t)
	r, err := Audit(context.Background(), "http://example.com", AuditOptions{Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if is := issueByID(r, "https-missing"); is == nil || is.Severity != High || r.HTTPS {
		t.Errorf("https-missing %+v", is)
	}
}

func TestPageAuditSkipsRobotsBlockedLinks(t *testing.T) {
	f := newFixture(t)
	r, err := PageAudit(context.Background(), "https://example.com/", PageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if f.count("example.com/secret/x") != 0 {
		t.Error("a robots-disallowed internal link was requested")
	}
	found := false
	for _, w := range r.Warnings {
		found = found || strings.Contains(w, "disallowed by robots.txt")
	}
	if !found {
		t.Errorf("warnings %v", r.Warnings)
	}
}
