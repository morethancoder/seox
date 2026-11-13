package site

import "time"

// Hop is one step of a redirect chain.
type Hop struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

// Heading is one h1-h6 in document order.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// MetaTag is one <meta> tag. Kind is the attribute that named it (name, property, http-equiv, charset).
type MetaTag struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
}

// Hreflang is one <link rel="alternate" hreflang>.
type Hreflang struct {
	Lang string `json:"lang"`
	Href string `json:"href"`
}

// JSONLD is one structured-data block.
type JSONLD struct {
	Types []string `json:"types,omitempty"`
	Valid bool     `json:"valid"`
	Error string   `json:"error,omitempty"`
	Raw   string   `json:"raw,omitempty"` // cut at 2000 characters
}

// Link is one <a href> found on a page.
type Link struct {
	URL      string `json:"url"`
	Anchor   string `json:"anchor,omitempty"`
	Nofollow bool   `json:"nofollow,omitempty"`
	Internal bool   `json:"internal"`
}

// Term is one keyword candidate of a page with its position-weighted score.
type Term struct {
	Term  string   `json:"term"`
	Score float64  `json:"score"`
	In    []string `json:"in,omitempty"` // fields it appears in: title, h1, h2, h3, description, anchor, body
}

// Page is everything the crawler recorded about one URL.
type Page struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url,omitempty"`
	Status      int    `json:"status"`                 // status of the URL as requested
	FinalStatus int    `json:"final_status,omitempty"` // status after redirects, when different
	Redirects   []Hop  `json:"redirects,omitempty"`    // the hops before the final response
	TimeMS      int64  `json:"time_ms"`
	Bytes       int    `json:"bytes"`
	ContentType string `json:"content_type,omitempty"`
	Depth       int    `json:"depth"`
	Error       string `json:"error,omitempty"`
	Timeout     bool   `json:"timeout,omitempty"` // Error is a timeout, not a refusal or a DNS failure
	InSitemap   bool   `json:"in_sitemap,omitempty"`
	Parsed      bool   `json:"parsed"` // false for errors and redirects that led nowhere
	XRobots     string `json:"x_robots_tag,omitempty"`

	Title            string     `json:"title,omitempty"`
	TitleCount       int        `json:"title_count,omitempty"`
	Description      string     `json:"description,omitempty"`
	DescriptionCount int        `json:"description_count,omitempty"`
	Canonicals       []string   `json:"canonicals,omitempty"`
	MetaRobots       string     `json:"meta_robots,omitempty"`
	Lang             string     `json:"lang,omitempty"`
	Viewport         string     `json:"viewport,omitempty"`
	Headings         []Heading  `json:"headings,omitempty"`
	Metas            []MetaTag  `json:"metas,omitempty"`
	OpenGraph        []MetaTag  `json:"open_graph,omitempty"`
	Hreflangs        []Hreflang `json:"hreflangs,omitempty"`
	JSONLD           []JSONLD   `json:"json_ld,omitempty"`
	Images           int        `json:"images"`
	ImagesNoAlt      int        `json:"images_without_alt"`
	ImageExamples    []string   `json:"images_without_alt_examples,omitempty"`
	MixedContent     []string   `json:"mixed_content,omitempty"`
	Links            []Link     `json:"links,omitempty"`
	WordCount        int        `json:"word_count"`
	Terms            []Term     `json:"terms,omitempty"`
}

// Noindex reports whether the page asks not to be indexed (meta robots or X-Robots-Tag).
func (p *Page) Noindex() bool {
	return containsToken(p.MetaRobots, "noindex") || containsToken(p.XRobots, "noindex") ||
		containsToken(p.MetaRobots, "none") || containsToken(p.XRobots, "none")
}

// SitemapInfo says what was found at the sitemap URLs.
type SitemapInfo struct {
	Found  bool     `json:"found"`
	Files  []string `json:"files,omitempty"`
	URLs   int      `json:"urls"`
	Errors []string `json:"errors,omitempty"`
}

// RobotsInfo says what robots.txt said to seox.
type RobotsInfo struct {
	Found         bool     `json:"found"`
	Group         string   `json:"group,omitempty"` // the user-agent group that applied
	BlocksAll     bool     `json:"blocks_all,omitempty"`
	CrawlDelaySec float64  `json:"crawl_delay_sec,omitempty"`
	Sitemaps      []string `json:"sitemaps,omitempty"`
}

// ExtStatus is the result of checking one external link.
type ExtStatus struct {
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
	Method string `json:"method,omitempty"` // HEAD or GET (the fallback)
	// Timeout is true when no answer came in time. The link may be fine, so it
	// is not counted as broken.
	Timeout bool `json:"timeout,omitempty"`
}

// Skipped is a URL the crawler chose not to fetch, with the reason.
type Skipped struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// CrawlResult is a finished crawl. It is what gets cached and what audit,
// page and keywords all work from.
type CrawlResult struct {
	Site          string               `json:"site"`  // the start URL as given
	Start         string               `json:"start"` // the home page after redirects
	Host          string               `json:"host"`
	Max           int                  `json:"max"`
	Depth         int                  `json:"depth"`
	Pages         []*Page              `json:"pages"`
	Skipped       []Skipped            `json:"skipped,omitempty"`
	SkippedCount  int                  `json:"skipped_count"`
	BlockedRobots []string             `json:"blocked_by_robots,omitempty"`
	Limited       bool                 `json:"limited"` // stopped at --max with more URLs waiting
	Robots        RobotsInfo           `json:"robots"`
	Sitemap       SitemapInfo          `json:"sitemap"`
	HTTPSHome     bool                 `json:"https_home"`               // the home page is served over https
	HTTPRedirects *bool                `json:"http_redirects,omitempty"` // http:// redirects to https; nil when not tested
	External      map[string]ExtStatus `json:"external,omitempty"`
	ExternalTotal int                  `json:"external_total"` // distinct external links seen
	Warnings      []string             `json:"warnings,omitempty"`
	CrawledAt     time.Time            `json:"crawled_at"`
	Cached        bool                 `json:"-"`
}

// CrawlOptions are the knobs of a crawl. Zero Max means the config default.
type CrawlOptions struct {
	Max     int
	Depth   int // 0 = no limit
	NoCache bool
}
