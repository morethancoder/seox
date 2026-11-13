package site

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/ops"
)

func init() {
	ops.Handle("audit", handleAudit)
	ops.Handle("page", handlePage)
	ops.Handle("keywords", handleKeywords)
}

// ---- audit

// AuditOptions are the options of Audit. Zero Max means the config default.
type AuditOptions struct {
	Max     int
	Depth   int
	NoCache bool
}

// PageRow is one crawled page in the audit's compact page list.
type PageRow struct {
	URL       string `json:"url"`
	Status    int    `json:"status"`
	Depth     int    `json:"depth"`
	Title     string `json:"title,omitempty"`
	WordCount int    `json:"word_count"`
	TimeMS    int64  `json:"time_ms"`
}

// SeverityCount tallies issues per severity.
type SeverityCount struct {
	High   int `json:"high"`
	Medium int `json:"medium"`
	Low    int `json:"low"`
}

// AuditResult is the outcome of `seox audit`.
type AuditResult struct {
	Site           string         `json:"site"`
	Start          string         `json:"start"`
	Score          int            `json:"score"`
	ScoreFormula   string         `json:"score_formula"`
	PagesCrawled   int            `json:"pages_crawled"`
	Max            int            `json:"max"`
	Limited        bool           `json:"limited"`
	CrawledAt      time.Time      `json:"crawled_at"`
	Counts         SeverityCount  `json:"issue_counts"`
	Issues         []Issue        `json:"issues"`
	Robots         RobotsInfo     `json:"robots"`
	Sitemap        SitemapInfo    `json:"sitemap"`
	HTTPS          bool           `json:"https"`
	StructuredData map[string]int `json:"structured_data_types,omitempty"` // JSON-LD @type -> pages
	ExternalLinks  ExternalInfo   `json:"external_links"`
	SkippedCount   int            `json:"skipped_count"`
	Skipped        []Skipped      `json:"skipped,omitempty"`
	Pages          []PageRow      `json:"pages"`
	Warnings       []string       `json:"warnings,omitempty"`
	Cached         bool           `json:"cached,omitempty"`
}

// ExternalInfo says how many outside links were seen and tested.
type ExternalInfo struct {
	Found   int `json:"found"`
	Checked int `json:"checked"`
	Broken  int `json:"broken"`
}

// ScoreFormula is shown in every audit so the number can be checked.
const ScoreFormula = "100 minus the sum over issues of weight x share; weights high 20, medium 8, low 3; share is 1 for site-wide issues and affected pages / pages crawled for page issues (at least 0.1); clamped to 0-100"

// Audit crawls the site (cached for 6 hours) and runs every check.
func Audit(ctx context.Context, site string, opt AuditOptions) (*AuditResult, error) {
	cr, err := Crawl(ctx, site, CrawlOptions{Max: opt.Max, Depth: opt.Depth, NoCache: opt.NoCache})
	if err != nil {
		return nil, err
	}
	return AuditCrawl(cr), nil
}

// AuditCrawl runs the checks over a finished crawl.
func AuditCrawl(cr *CrawlResult) *AuditResult {
	issues, score, warns := analyse(cr, scopeSite)
	r := &AuditResult{
		Site: cr.Site, Start: cr.Start, Score: score, ScoreFormula: ScoreFormula,
		PagesCrawled: len(cr.Pages), Max: cr.Max, Limited: cr.Limited, CrawledAt: cr.CrawledAt,
		Issues: issues, Robots: cr.Robots, Sitemap: cr.Sitemap, HTTPS: cr.HTTPSHome,
		SkippedCount: cr.SkippedCount, Skipped: cr.Skipped, Warnings: append(append([]string(nil), cr.Warnings...), warns...), Cached: cr.Cached,
		StructuredData: map[string]int{},
	}
	for _, is := range issues {
		switch is.Severity {
		case High:
			r.Counts.High++
		case Medium:
			r.Counts.Medium++
		default:
			r.Counts.Low++
		}
	}
	for _, p := range cr.Pages {
		r.Pages = append(r.Pages, PageRow{URL: p.URL, Status: effStatus(p), Depth: p.Depth, Title: p.Title, WordCount: p.WordCount, TimeMS: p.TimeMS})
		seen := map[string]bool{}
		for _, j := range p.JSONLD {
			for _, t := range j.Types {
				if !seen[t] {
					seen[t] = true
					r.StructuredData[t]++
				}
			}
		}
	}
	if len(r.StructuredData) == 0 {
		r.StructuredData = nil
	}
	r.ExternalLinks = ExternalInfo{Found: cr.ExternalTotal, Checked: len(cr.External)}
	for _, st := range cr.External {
		if linkBroken(st) {
			r.ExternalLinks.Broken++
		}
	}
	return r
}

func handleAudit(ctx context.Context, in ops.Input) (*ops.Result, error) {
	r, err := Audit(ctx, in.Str("site"), AuditOptions{Max: in.Int("max", 0), Depth: in.Int("depth", 0), NoCache: in.NoCache})
	if err != nil {
		return nil, err
	}
	return &ops.Result{Data: r, Meta: ops.Meta{Source: "crawl", Cached: r.Cached}, Warnings: r.Warnings}, nil
}

// ---- keywords

// KeywordsOptions are the options of Keywords.
type KeywordsOptions struct {
	Max     int
	Limit   int // 0 = 20
	NoCache bool
}

// KeywordsResult is the outcome of `seox keywords`.
type KeywordsResult struct {
	Site         string        `json:"site"`
	PagesCrawled int           `json:"pages_crawled"`
	Limited      bool          `json:"limited"`
	Weights      string        `json:"weights"`
	Keywords     []SiteKeyword `json:"keywords"`
	Warnings     []string      `json:"warnings,omitempty"`
	Cached       bool          `json:"cached,omitempty"`
}

const weightsNote = "title 5, h1 4, h2/h3 2, meta description 2, anchor text 1.5, body 1; 1-3 word terms; 2-word terms score x1.25 and 3-word terms x1.5 so phrases rank beside single words; a page targets a term when it is in the page's top 40"

// Keywords aggregates the crawl's terms across pages.
func Keywords(ctx context.Context, site string, opt KeywordsOptions) (*KeywordsResult, error) {
	cr, err := Crawl(ctx, site, CrawlOptions{Max: opt.Max, NoCache: opt.NoCache})
	if err != nil {
		return nil, err
	}
	return KeywordsFromCrawl(cr, opt.Limit), nil
}

// KeywordsFromCrawl is Keywords over a crawl already in hand.
func KeywordsFromCrawl(cr *CrawlResult, limit int) *KeywordsResult {
	if limit <= 0 {
		limit = 20
	}
	return &KeywordsResult{Site: cr.Start, PagesCrawled: len(cr.Pages), Limited: cr.Limited, Weights: weightsNote,
		Keywords: aggregateKeywords(cr.Pages, limit), Warnings: cr.Warnings, Cached: cr.Cached}
}

func handleKeywords(ctx context.Context, in ops.Input) (*ops.Result, error) {
	r, err := Keywords(ctx, in.Str("site"), KeywordsOptions{Max: in.Int("max", 0), Limit: in.Int("limit", 20), NoCache: in.NoCache})
	if err != nil {
		return nil, err
	}
	return &ops.Result{Data: r, Meta: ops.Meta{Source: "crawl", Cached: r.Cached}, Warnings: r.Warnings}, nil
}

// ---- page

// PageOptions are the options of PageAudit.
type PageOptions struct{ NoCache bool }

// LinkOut is a link on the page with its status when it was tested.
type LinkOut struct {
	URL      string `json:"url"`
	Anchor   string `json:"anchor,omitempty"`
	Internal bool   `json:"internal"`
	Nofollow bool   `json:"nofollow,omitempty"`
	Status   int    `json:"status,omitempty"`
	Error    string `json:"error,omitempty"`
}

// LinkIn is a page on the same site that links here.
type LinkIn struct {
	From   string `json:"from"`
	Anchor string `json:"anchor,omitempty"`
}

// PageResult is the outcome of `seox page`.
type PageResult struct {
	URL         string    `json:"url"`
	Score       int       `json:"score"`
	Issues      []Issue   `json:"issues"`
	Page        *Page     `json:"page"` // every heading, meta tag, JSON-LD block, hreflang, image count and word count
	Keywords    []Term    `json:"keywords"`
	LinksOut    []LinkOut `json:"links_out"`
	LinksTested int       `json:"links_tested"`
	LinksIn     []LinkIn  `json:"links_in"`
	LinksInNote string    `json:"links_in_note,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	Cached      bool      `json:"cached,omitempty"`
}

const (
	pageTTL       = time.Hour
	maxPageLinks  = 60  // links tested per page
	maxLinksShown = 500 // links listed per page
)

// PageAudit fetches one page, runs the same checks as the audit (the ones that
// need a single page), and adds the detail, the links and the page's keywords.
func PageAudit(ctx context.Context, rawURL string, opt PageOptions) (*PageResult, error) {
	norm, ok := parseSite(rawURL)
	if !ok {
		return nil, apperr.New(apperr.InvalidInput, "%q is not a URL", rawURL)
	}
	key := cache.Key("site-page", norm)
	if !opt.NoCache {
		var cached PageResult
		if _, ok := cache.Get(key, pageTTL, &cached); ok {
			cached.Cached = true
			return &cached, nil
		}
	}
	u, _ := url.Parse(norm)
	c := &crawler{ctx: ctx, seen: map[string]bool{}, inSitemap: map[string]bool{},
		res: &CrawlResult{Site: norm, Host: hostKey(u.Host), Max: 1, CrawledAt: time.Now().UTC()}}
	if err := c.loadRobots(u); err != nil {
		return nil, err
	}
	if !c.rules.allowed(pathQuery(u)) {
		return nil, apperr.New(apperr.Forbidden, "robots.txt of %s disallows %s for seox", u.Host, norm)
	}
	c.claim(norm)
	p, _, err := c.fetchPage(norm, 0)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperr.New(apperr.InvalidInput, "%s is not an HTML page", norm)
	}
	c.start = norm
	if p.FinalURL != "" {
		c.start = p.FinalURL
	}
	c.res.Start = c.start
	if su, err := url.Parse(c.start); err == nil {
		c.res.HTTPSHome = su.Scheme == "https"
	}
	c.markInternal(p)
	c.res.Pages = []*Page{p}

	// Test the page's links, internal ones first, so broken-link checks cover this page.
	var targets []string
	for _, internal := range []bool{true, false} {
		for _, l := range p.Links {
			if l.Internal == internal {
				targets = append(targets, l.URL)
			}
		}
	}
	targets = uniqueStrings(targets)
	// Internal URLs that robots.txt disallows are never requested.
	var allowedTargets []string
	var blocked int
	for _, t := range targets {
		if tu, err := url.Parse(t); err == nil && sameSite(t, c.start) && !c.rules.allowed(pathQuery(tu)) {
			blocked++
			continue
		}
		allowedTargets = append(allowedTargets, t)
	}
	targets = allowedTargets
	res := &PageResult{URL: p.URL, LinksOut: []LinkOut{}, LinksIn: []LinkIn{}}
	if len(targets) > maxPageLinks {
		res.Warnings = append(res.Warnings, "the page has more than 60 links; only the first 60 (internal first) were tested")
		targets = targets[:maxPageLinks]
	}
	if blocked > 0 {
		res.Warnings = append(res.Warnings, strconv.Itoa(blocked)+" internal links are disallowed by robots.txt and were not tested")
	}
	c.res.External = CheckLinks(ctx, targets, opt.NoCache)
	if err := ctx.Err(); err != nil {
		return nil, apperr.From(err)
	}
	res.LinksTested = len(c.res.External)
	var warns []string
	res.Issues, res.Score, warns = analyse(c.res, scopePage)
	res.Warnings = append(res.Warnings, c.res.Warnings...)
	res.Warnings = append(res.Warnings, warns...)

	for i, l := range p.Links {
		if i >= maxLinksShown {
			break
		}
		lo := LinkOut{URL: l.URL, Anchor: l.Anchor, Internal: l.Internal, Nofollow: l.Nofollow}
		if st, ok := c.res.External[l.URL]; ok {
			lo.Status, lo.Error = st.Status, st.Error
		}
		res.LinksOut = append(res.LinksOut, lo)
	}
	if in, note := inboundLinks(p); in != nil {
		res.LinksIn, res.LinksInNote = in, note
	} else {
		res.LinksInNote = note
	}
	pg := *p
	pg.Links, pg.Terms = nil, nil
	res.Page = &pg
	res.Keywords = p.Terms
	if len(res.Keywords) > 25 {
		res.Keywords = res.Keywords[:25]
	}
	cache.Put(key, "crawl", res)
	return res, nil
}

// inboundLinks finds links to the page in the site's cached audit crawl. A
// single page fetch cannot know who links to it, and crawling the whole site
// for that would be slow, so without a cached crawl it says so.
func inboundLinks(p *Page) ([]LinkIn, string) {
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, ""
	}
	start := u.Scheme + "://" + u.Host + "/"
	key := crawlKey(start, CrawlOptions{Max: defaultMax()})
	var cr CrawlResult
	if _, ok := cache.Get(key, crawlTTL, &cr); !ok {
		return nil, "inbound links come from a site crawl: run `seox audit " + hostKey(u.Host) + "` first (default options), then run this again"
	}
	want := map[string]bool{dedupKey(p.URL): true}
	if p.FinalURL != "" {
		want[dedupKey(p.FinalURL)] = true
	}
	in := []LinkIn{}
	for _, o := range cr.Pages {
		if want[dedupKey(o.URL)] {
			continue
		}
		for _, l := range o.Links {
			if want[dedupKey(l.URL)] {
				in = append(in, LinkIn{From: o.URL, Anchor: l.Anchor})
			}
		}
	}
	sort.SliceStable(in, func(i, j int) bool { return in[i].From < in[j].From })
	note := "from the cached crawl of " + cr.Start + " (" + itoa(len(cr.Pages)) + " pages)"
	return in, note
}

func uniqueStrings(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

func handlePage(ctx context.Context, in ops.Input) (*ops.Result, error) {
	r, err := PageAudit(ctx, in.Str("url"), PageOptions{NoCache: in.NoCache})
	if err != nil {
		return nil, err
	}
	return &ops.Result{Data: r, Meta: ops.Meta{Source: "crawl", Cached: r.Cached}, Warnings: r.Warnings}, nil
}
