package site

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/web"
)

const (
	crawlTTL      = 6 * time.Hour
	pageMaxBytes  = 2 << 20 // a page body bigger than this is cut
	maxExternal   = 100     // external links checked per crawl
	maxSkipped    = 50      // skipped URLs listed (the count is exact)
	maxBlocked    = 50
	crawlWorkers  = 2 // matches web.MaxConcurrent: more would only queue
	maxCrawlDelay = 30 * time.Second
)

type crawler struct {
	ctx   context.Context
	res   *CrawlResult
	rules *robotsRules
	start string // normalised home URL after redirects

	mu        sync.Mutex
	seen      map[string]bool
	inSitemap map[string]bool
	claimed   int // pages claimed against the --max budget
	attempts  int
	skipped   int

	sitemapOrder []string // sitemap URLs in file order, queued after the home page's links
}

// Crawl crawls a site and returns the result, from the 6 hour cache when it
// has one. The cache key holds the site, --max and --depth.
func Crawl(ctx context.Context, site string, opt CrawlOptions) (*CrawlResult, error) {
	startURL, ok := parseSite(site)
	if !ok {
		return nil, apperr.New(apperr.InvalidInput, "%q is not a site, domain or URL", site)
	}
	if opt.Max <= 0 {
		opt.Max = defaultMax()
	}
	if opt.Depth < 0 {
		opt.Depth = 0
	}
	key := crawlKey(startURL, opt)
	if !opt.NoCache {
		var cached CrawlResult
		if _, ok := cache.Get(key, crawlTTL, &cached); ok {
			cached.Cached = true
			return &cached, nil
		}
	}
	res, err := runCrawl(ctx, startURL, opt)
	if err != nil {
		return nil, err
	}
	// A crawl cut short by the time limit is not cached: the next run should retry it in full.
	if !res.partial() {
		cache.Put(key, "crawl", res)
	}
	return res, nil
}

func (r *CrawlResult) partial() bool {
	for _, w := range r.Warnings {
		if strings.HasPrefix(w, "stopped early") {
			return true
		}
	}
	return false
}

func defaultMax() int {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return config.DefaultCrawlMax
	}
	return cfg.Crawl()
}

func crawlKey(startURL string, opt CrawlOptions) string {
	// The key holds the scheme and the start path, so `audit example.com` and
	// `audit http://example.com/blog/` never share a result.
	u, _ := url.Parse(startURL)
	return cache.Key("site-crawl", u.Scheme, hostKey(u.Host), pathQuery(u), strconv.Itoa(opt.Max), strconv.Itoa(opt.Depth))
}

func runCrawl(ctx context.Context, startURL string, opt CrawlOptions) (*CrawlResult, error) {
	u, _ := url.Parse(startURL)
	c := &crawler{
		ctx:       ctx,
		seen:      map[string]bool{},
		inSitemap: map[string]bool{},
		res: &CrawlResult{Site: startURL, Host: hostKey(u.Host), Max: opt.Max, Depth: opt.Depth,
			CrawledAt: time.Now().UTC()},
	}
	if err := c.loadRobots(u); err != nil {
		return nil, err
	}
	if !c.rules.allowed(pathQuery(u)) {
		return nil, apperr.New(apperr.Forbidden, "robots.txt of %s disallows crawling %s for seox", u.Host, startURL)
	}
	c.loadSitemaps(u)

	c.claim(startURL)
	home, _, err := c.fetchPage(startURL, 0)
	if err != nil {
		return nil, err
	}
	if home == nil {
		return nil, apperr.New(apperr.SourceError, "the home page %s is not an HTML page", startURL)
	}
	c.start = startURL
	if home.FinalURL != "" {
		c.start = home.FinalURL
		if !sameSite(c.start, startURL) {
			return nil, apperr.New(apperr.SourceError, "%s redirects to another site (%s); run seox on that site instead", startURL, c.start)
		}
	}
	c.res.Start = c.start
	if su, err := url.Parse(c.start); err == nil {
		c.res.HTTPSHome = su.Scheme == "https"
		c.res.Host = hostKey(su.Host)
		// When the start URL redirected to another host (apex to www), that host's
		// robots.txt and Crawl-delay are the ones that apply from here on.
		if su.Host != u.Host {
			if err := c.loadRobots(su); err != nil {
				return nil, err
			}
		}
	}
	c.markInternal(home)
	c.res.Pages = append(c.res.Pages, home)
	c.mu.Lock()
	c.claimed = 1
	c.mu.Unlock()

	var frontier []queued
	frontier = c.enqueueLinks(frontier, home, 1, opt)
	for _, sm := range c.sitemapOrder {
		frontier = c.enqueue(frontier, sm, 1, opt)
	}

	for len(frontier) > 0 && c.ctx.Err() == nil {
		budget := opt.Max - c.claimedCount()
		if budget <= 0 {
			c.res.Limited = true
			break
		}
		batch, rest := frontier, []queued(nil)
		if len(batch) > budget {
			batch, rest = batch[:budget], batch[budget:]
		}
		results := make([]*Page, len(batch))
		var wg sync.WaitGroup
		sem := make(chan struct{}, crawlWorkers)
		for i, q := range batch {
			if c.ctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(i int, q queued) {
				defer wg.Done()
				defer func() { <-sem }()
				p, _, err := c.fetchPage(q.url, q.depth)
				if err != nil {
					return
				}
				results[i] = p
			}(i, q)
		}
		wg.Wait()
		next := []queued(nil)
		for i, p := range results {
			if p == nil {
				continue // not HTML: skipped, and it does not use the page budget
			}
			c.markInternal(p)
			c.res.Pages = append(c.res.Pages, p)
			c.mu.Lock()
			c.claimed++
			c.mu.Unlock()
			next = c.enqueueLinks(next, p, batch[i].depth+1, opt)
		}
		// URLs trimmed from this level go first so the crawl stays breadth-first.
		frontier = append(rest, next...)
	}
	if err := c.ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			c.res.Warnings = append(c.res.Warnings, fmt.Sprintf("stopped early: the time limit ran out after %d pages; results cover those pages only", len(c.res.Pages)))
		} else {
			return nil, apperr.From(err)
		}
	}
	c.res.SkippedCount = c.skipped
	c.checkHTTPRedirect()
	c.checkExternalLinks()
	if c.res.Limited {
		c.res.Warnings = append(c.res.Warnings, fmt.Sprintf("stopped at --max %d pages; more URLs were waiting (raise --max to crawl further)", opt.Max))
	}
	return c.res, nil
}

type queued struct {
	url   string
	depth int
}

func pathQuery(u *url.URL) string {
	s := u.EscapedPath()
	if s == "" {
		s = "/"
	}
	if u.RawQuery != "" {
		s += "?" + u.RawQuery
	}
	return s
}

func (c *crawler) claimedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claimed
}

// claim marks a URL as seen and reports whether it was new.
func (c *crawler) claim(norm string) bool {
	k := dedupKey(norm)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[k] {
		return false
	}
	c.seen[k] = true
	return true
}

func (c *crawler) enqueueLinks(f []queued, p *Page, depth int, opt CrawlOptions) []queued {
	if !p.Parsed || containsToken(p.MetaRobots, "nofollow") || containsToken(p.XRobots, "nofollow") {
		return f
	}
	for _, l := range p.Links {
		if l.Nofollow {
			continue
		}
		f = c.enqueue(f, l.URL, depth, opt)
	}
	return f
}

// enqueue adds a URL to the next level when it is new, on this site, allowed
// by robots.txt, within --depth and not an obvious non-HTML file.
func (c *crawler) enqueue(f []queued, norm string, depth int, opt CrawlOptions) []queued {
	if opt.Depth > 0 && depth > opt.Depth {
		return f
	}
	if !sameSite(norm, c.start) && !sameSite(norm, c.res.Site) {
		return f
	}
	if hasNonHTMLExt(norm) {
		if c.claim(norm) {
			c.skip(norm, "file type is not HTML")
		}
		return f
	}
	u, err := url.Parse(norm)
	if err != nil {
		return f
	}
	if !c.rules.allowed(pathQuery(u)) {
		if c.claim(norm) {
			c.mu.Lock()
			if len(c.res.BlockedRobots) < maxBlocked {
				c.res.BlockedRobots = append(c.res.BlockedRobots, norm)
			}
			c.mu.Unlock()
			c.skipped++
		}
		return f
	}
	if !c.claim(norm) {
		return f
	}
	return append(f, queued{norm, depth})
}

func (c *crawler) skip(u, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skipped++
	if len(c.res.Skipped) < maxSkipped {
		c.res.Skipped = append(c.res.Skipped, Skipped{URL: u, Reason: reason})
	}
}

func (c *crawler) markInternal(p *Page) {
	for i := range p.Links {
		p.Links[i].Internal = sameSite(p.Links[i].URL, c.start)
	}
	if c.inSitemap[dedupKey(p.URL)] {
		p.InSitemap = true
	}
}

// ---- robots.txt

func (c *crawler) loadRobots(u *url.URL) error {
	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"
	r, err := web.Get(c.ctx, robotsURL, 512<<10)
	if err != nil {
		return err
	}
	// Start clean: after a redirect, only the final host's robots.txt may show in the summary.
	c.res.Robots = RobotsInfo{}
	c.rules = &robotsRules{}
	switch {
	case r.Status >= 500:
		// RFC 9309: a server error means the crawler must assume it may not crawl.
		return apperr.New(apperr.Forbidden, "robots.txt of %s answered %d; seox will not crawl without being able to read it", u.Host, r.Status)
	case r.Status >= 200 && r.Status < 300:
		c.rules = parseRobots(string(r.Body))
		c.res.Robots = RobotsInfo{Found: true, Group: c.rules.Group, BlocksAll: c.rules.blocksAll(),
			CrawlDelaySec: c.rules.CrawlDelay.Seconds(), Sitemaps: c.rules.Sitemaps}
		c.applyDelay(u.Host)
	}
	return nil
}

func (c *crawler) applyDelay(host string) {
	d := c.rules.CrawlDelay
	if d <= 0 {
		return
	}
	if d > maxCrawlDelay {
		d = maxCrawlDelay
		w := fmt.Sprintf("robots.txt asks for a Crawl-delay of %.0f s; seox waits at most %s between requests", c.rules.CrawlDelay.Seconds(), maxCrawlDelay)
		if !slices.Contains(c.res.Warnings, w) {
			c.res.Warnings = append(c.res.Warnings, w)
		}
	}
	web.SetCrawlDelay(host, d)
	other := strings.TrimPrefix(host, "www.")
	if other == host {
		other = "www." + host
	}
	web.SetCrawlDelay(other, d)
}

// ---- sitemaps

func (c *crawler) loadSitemaps(u *url.URL) {
	origin := u.Scheme + "://" + u.Host
	todo := append([]string(nil), c.rules.Sitemaps...)
	declared := map[string]bool{}
	for _, s := range todo {
		declared[s] = true
	}
	def := origin + "/sitemap.xml"
	if !declared[def] {
		todo = append(todo, def)
	}
	info := &c.res.Sitemap
	files := 0
	seenFile := map[string]bool{}
	for len(todo) > 0 && files < maxSitemapFiles && c.ctx.Err() == nil {
		sm := todo[0]
		todo = todo[1:]
		if seenFile[sm] {
			continue
		}
		seenFile[sm] = true
		files++
		if strings.HasSuffix(strings.ToLower(sm), ".gz") {
			info.Errors = append(info.Errors, sm+": compressed sitemaps are not read")
			continue
		}
		r, err := web.Get(c.ctx, sm, 0)
		if err != nil {
			if c.ctx.Err() == nil {
				info.Errors = append(info.Errors, sm+": "+err.Error())
			}
			continue
		}
		if r.Status != 200 {
			if declared[sm] || r.Status >= 500 {
				info.Errors = append(info.Errors, fmt.Sprintf("%s: HTTP %d", sm, r.Status))
			}
			continue
		}
		pages, children, err := parseSitemap(r.Body)
		if err != nil {
			info.Errors = append(info.Errors, sm+": "+err.Error())
			continue
		}
		info.Found = true
		info.Files = append(info.Files, sm)
		todo = append(todo, children...)
		for _, p := range pages {
			if info.URLs >= maxSitemapURLs {
				break
			}
			n, ok := normalizeURL(p)
			if !ok || !sameSite(n, c.res.Site) {
				continue
			}
			k := dedupKey(n)
			if c.inSitemap[k] {
				continue
			}
			c.inSitemap[k] = true
			c.sitemapOrder = append(c.sitemapOrder, n)
			info.URLs++
		}
	}
	if len(todo) > 0 && files >= maxSitemapFiles {
		c.res.Warnings = append(c.res.Warnings, fmt.Sprintf("sitemap index has more than %d files; only the first %d were read", maxSitemapFiles, maxSitemapFiles))
	}
}

// ---- fetching one page

func isHTML(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if ct == "" {
		ct = strings.ToLower(http.DetectContentType(body[:min(len(body), 512)]))
	}
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// pageTimeout caps one page request. A var so tests can shorten it.
var pageTimeout = 40 * time.Second

func (c *crawler) getPage(u string) (*web.Response, error) {
	ctx, cancel := context.WithTimeout(c.ctx, pageTimeout)
	defer cancel()
	return web.GetNoRedirect(ctx, u, pageMaxBytes)
}

// fetchPage fetches one URL, following redirects by hand so the whole chain is
// recorded. It returns a nil page when the URL turned out not to be HTML. A
// non-nil error means the crawl cannot go on (the context ended).
func (c *crawler) fetchPage(raw string, depth int) (*Page, bool, error) {
	c.mu.Lock()
	c.attempts++
	c.mu.Unlock()
	p := &Page{URL: raw, Depth: depth}
	cur := raw
	var hops []Hop
	visited := map[string]bool{}
	var elapsed time.Duration
	for i := 0; i <= web.MaxRedirects; i++ {
		visited[cur] = true
		t0 := time.Now()
		r, err := c.getPage(cur)
		if err != nil && isTimeout(err) && c.ctx.Err() == nil && !(i == 0 && depth == 0) {
			// One slower retry: a busy server often answers the second time.
			select {
			case <-c.ctx.Done():
			case <-time.After(retryPause):
				r, err = c.getPage(cur)
			}
		}
		elapsed += time.Since(t0)
		p.TimeMS = elapsed.Milliseconds()
		if err != nil {
			if c.ctx.Err() != nil {
				return nil, false, apperr.From(c.ctx.Err())
			}
			if i == 0 && depth == 0 {
				return nil, false, err
			}
			p.Error = err.Error()
			p.Timeout = isTimeout(err)
			p.Redirects = hops
			if i > 0 {
				p.FinalURL = cur
			}
			return p, true, nil
		}
		if i == 0 {
			p.Status = r.Status
		}
		loc := r.Header.Get("Location")
		if r.Status >= 300 && r.Status < 400 && loc != "" {
			hops = append(hops, Hop{URL: cur, Status: r.Status})
			cu, _ := url.Parse(cur)
			nu, err := cu.Parse(loc)
			next, ok := "", false
			if err == nil {
				next, ok = normalizeURL(nu.String())
			}
			if !ok {
				p.Error = "redirect to an unreadable Location: " + loc
				p.Redirects = hops
				return p, true, nil
			}
			p.Redirects = hops
			p.FinalURL = next
			if visited[next] {
				p.Error = "redirect loop"
				return p, true, nil
			}
			if !sameSite(next, c.res.Site) && !sameSite(next, cur) {
				return p, true, nil // leaves the site: recorded, not followed
			}
			if nuu, err := url.Parse(next); err == nil && !c.rules.allowed(pathQuery(nuu)) {
				p.Error = "redirect target is disallowed by robots.txt"
				return p, true, nil
			}
			if dedupKey(next) != dedupKey(cur) && !c.claim(next) {
				return p, true, nil // another URL already covers the target page
			}
			cur = next
			continue
		}
		// A final response.
		if cur != raw {
			p.FinalURL = cur
		}
		if len(hops) > 0 {
			p.Redirects = hops
		}
		if r.Status != p.Status {
			p.FinalStatus = r.Status
		}
		p.ContentType = r.Header.Get("Content-Type")
		p.Bytes = len(r.Body)
		p.XRobots = strings.Join(r.Header.Values("X-Robots-Tag"), ", ")
		if r.Status >= 200 && r.Status < 300 {
			if !isHTML(p.ContentType, r.Body) {
				c.skip(raw, "content type is not HTML: "+p.ContentType)
				return nil, false, nil
			}
			if r.Truncated {
				c.addWarning(fmt.Sprintf("%s is larger than %d MB; only the start was read", raw, pageMaxBytes>>20))
			}
			parsePage(p, r.Body, cur)
		}
		return p, true, nil
	}
	p.Error = fmt.Sprintf("more than %d redirects", web.MaxRedirects)
	p.Redirects = hops
	return p, true, nil
}

func (c *crawler) addWarning(w string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.res.Warnings = append(c.res.Warnings, w)
}
