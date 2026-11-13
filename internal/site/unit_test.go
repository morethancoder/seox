package site

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"HTTPS://Example.COM":                   "https://example.com/",
		"https://example.com:443/a/../b#frag":   "https://example.com/b",
		"http://example.com:80/x/":              "http://example.com/x/",
		"https://example.com/p?b=2&a=1&utm_x=3": "https://example.com/p?a=1&b=2",
		"https://example.com/p?fbclid=abc":      "https://example.com/p",
		"https://example.com//a//b":             "https://example.com/a/b",
		"https://example.com:8080/a":            "https://example.com:8080/a",
	}
	for in, want := range cases {
		got, ok := normalizeURL(in)
		if !ok || got != want {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"mailto:a@b.c", "ftp://x.y/", "/relative", ""} {
		if _, ok := normalizeURL(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if !sameSite("https://www.example.com/a", "http://example.com/b") || sameSite("https://a.example.com/", "https://example.com/") {
		t.Error("sameSite: www and bare domain are one site, other subdomains are not")
	}
	if dedupKey("https://www.example.com/a?x=1") != dedupKey("http://example.com/a?x=1") {
		t.Error("dedupKey should ignore scheme and www")
	}
}

func TestRobots(t *testing.T) {
	body := "User-agent: *\nDisallow: /\n\nUser-agent: SEOX\nUser-agent: other\nCrawl-delay: 2.5\nDisallow: /private\nAllow: /private/open\nDisallow: /*.json$\nDisallow: /tmp?x=\nSitemap: https://a.test/s.xml\n"
	r := parseRobots(body)
	if r.Group != "seox" || r.CrawlDelay != 2500*time.Millisecond || len(r.Sitemaps) != 1 {
		t.Fatalf("%+v", r)
	}
	for path, want := range map[string]bool{
		"/": true, "/private": false, "/private/x": false, "/private/open": true, "/private/open/y": true,
		"/data.json": false, "/data.json?x=1": true, "/tmp?x=1": false, "/blog": true,
	} {
		if got := r.allowed(path); got != want {
			t.Errorf("allowed(%q) = %v, want %v", path, got, want)
		}
	}
	star := parseRobots("User-agent: *\nDisallow: /\n")
	if star.Group != "*" || !star.blocksAll() {
		t.Error("star group that blocks everything")
	}
	if !parseRobots("").allowed("/x") {
		t.Error("empty robots allows everything")
	}
	if !parseRobots("User-agent: *\nDisallow:\n").allowed("/x") {
		t.Error("empty Disallow allows everything")
	}
}

func TestSitemapIndex(t *testing.T) {
	pages, kids, err := parseSitemap([]byte(`<sitemapindex xmlns="x"><sitemap><loc>https://a.test/s1.xml</loc></sitemap></sitemapindex>`))
	if err != nil || len(pages) != 0 || len(kids) != 1 {
		t.Fatal(pages, kids, err)
	}
	if _, _, err := parseSitemap([]byte(`<html></html>`)); err == nil {
		t.Error("html is not a sitemap")
	}
	if _, _, err := parseSitemap([]byte(`<urlset><url>`)); err == nil {
		t.Error("broken xml must error")
	}
}

func TestTokeniseAndWeights(t *testing.T) {
	c := newTermCounter()
	c.addText("The best SEO tools for small business. Don't miss it!", wTitle, "title")
	c.addText("seo tools", wBody, "body")
	got := map[string]float64{}
	for _, term := range c.top(100) {
		got[term.Term] = term.Score
	}
	if got["seo tools"] != 7.5 || got["seo"] != 6 || got["tools"] != 6 {
		t.Errorf("weights %v", got)
	}
	for _, bad := range []string{"the", "for", "for small", "tools for", "it", "don", "the best seo", "nov", "read more", "min"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%q should be filtered", bad)
		}
	}
	if _, ok := got["tools for small"]; !ok {
		t.Error("an inner stopword is allowed in a trigram")
	}
	if _, ok := got["small business miss"]; ok {
		t.Error("n-grams must not cross a sentence break")
	}
	// Arabic: stopwords dropped, diacritics ignored.
	a := newTermCounter()
	a.addText("هذا الكتابُ في المكتبة", 1, "body")
	if !isStop("في") || !isStop("هذا") {
		t.Error("Arabic stopwords")
	}
	for _, w := range []string{"как", "для", "что", "это", "при", "или", "всё"} {
		if !isStop(w) {
			t.Errorf("Russian stopword %q", w)
		}
	}
	for _, w := range []string{"бот", "телеграм", "bot"} {
		if isStop(w) {
			t.Errorf("%q is not a stopword", w)
		}
	}
	terms := a.top(10)
	var names []string
	for _, x := range terms {
		names = append(names, x.Term)
	}
	if !reflect.DeepEqual(names, []string{"الكتاب", "المكتبة"}) && !strings.Contains(strings.Join(names, ","), "الكتاب") {
		t.Errorf("arabic terms %v", names)
	}
}

func TestScoreFormula(t *testing.T) {
	list := []*Issue{
		{ID: "a", Severity: High, Scope: "site", Count: 1},
		{ID: "b", Severity: Medium, Scope: "page", Count: 5},
		{ID: "c", Severity: Low, Scope: "page", Count: 1},
	}
	out, score := rankIssues(list, 10)
	// high site: 20; medium 5/10: 4; low 1/10: 0.3 -> 100 - 24.3 = 75.7 -> 76
	if score != 76 {
		t.Errorf("score %d", score)
	}
	if out[0].ID != "a" || out[0].Points != 20 {
		t.Errorf("%+v", out[0])
	}
	_, zero := rankIssues([]*Issue{{ID: "x", Severity: High, Scope: "site"}, {ID: "y", Severity: High, Scope: "site"}, {ID: "z", Severity: High, Scope: "site"}, {ID: "w", Severity: High, Scope: "site"}, {ID: "v", Severity: High, Scope: "site"}, {ID: "u", Severity: High, Scope: "site"}}, 1)
	if zero != 0 {
		t.Errorf("score floor %d", zero)
	}
	if _, perfect := rankIssues(nil, 5); perfect != 100 {
		t.Error("no issues is 100")
	}
}

func TestKeywordNoiseAndNumbers(t *testing.T) {
	c := newTermCounter()
	c.addText("Posted Nov 12, 2024 by an AI expert. Read more about iphone 15 and the UI of 3D tools", 1, "body")
	got := map[string]float64{}
	for _, x := range c.top(200) {
		got[x.Term] = x.Score
	}
	for _, bad := range []string{"nov", "read", "more", "2024", "15", "an", "of", "3d"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%q should be dropped", bad)
		}
	}
	for _, ok := range []string{"ai", "ai expert", "ui", "iphone 15", "tools"} {
		if _, found := got[ok]; !found {
			t.Errorf("%q should be kept; have %v", ok, got)
		}
	}
}

func TestRobotsEncodingAndBOM(t *testing.T) {
	r := parseRobots("\xef\xbb\xbfUser-agent: *\nDisallow: /مدونة/\nDisallow: /a%3cb\nDisallow: /%7Euser\n")
	if r.Group != "*" {
		t.Fatalf("BOM dropped the group: %+v", r)
	}
	for path, want := range map[string]bool{
		"/%D9%85%D8%AF%D9%88%D9%86%D8%A9/post": false, "/مدونة/x": false, "/a%3Cb": false, "/~user": false, "/other": true,
	} {
		if got := r.allowed(path); got != want {
			t.Errorf("allowed(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestCrawlKeyUsesStartURL(t *testing.T) {
	o := CrawlOptions{Max: 5}
	root, _ := parseSite("example.com")
	blog, _ := parseSite("http://example.com/blog/")
	if crawlKey(root, o) == crawlKey(blog, o) {
		t.Error("start path and scheme must be in the cache key")
	}
	w, _ := parseSite("https://www.example.com")
	if crawlKey(root, o) != crawlKey(w, o) {
		t.Error("www and bare domain share a key")
	}
}
