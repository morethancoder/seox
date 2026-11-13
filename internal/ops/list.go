package ops

func p(name, typ, help string) Param { return Param{Name: name, Type: typ, Help: help} }
func pos(name, help string) Param {
	return Param{Name: name, Type: TString, Required: true, Positional: true, Help: help}
}

// rest is a variadic last positional: one or more values.
func rest(name, help string) Param {
	pr := pos(name, help)
	pr.Variadic = true
	return pr
}
func def(pr Param, v any) Param { pr.Default = v; return pr }

var (
	pSite    = pos("site", "site, domain or URL (www and apex count as one site)")
	pURL     = pos("url", "page URL")
	pMax     = p("max", TInt, "most pages to crawl (default: crawl_max from config, else 200)")
	pDepth   = p("depth", TInt, "deepest link level to follow from the start page (default: no limit)")
	pLimit   = def(p("limit", TInt, "how many rows"), 20)
	pDays    = def(p("days", TInt, "length of the period in days"), 28)
	pLang    = def(p("lang", TString, "language code, e.g. en"), "en")
	pCountry = def(p("country", TString, "two-letter country code"), "us")
	pKw      = p("kw", TStrings, "a keyword to check positions for (repeatable)")
)

// All is every seox operation. Timeouts: audit, compare and report take 10
// minutes, rank 3 minutes, login none, everything else 90 seconds.
var All = []Op{
	// ---- any site, no account (area: site)
	{Name: "audit", Command: "audit", Short: "Crawl a site and list SEO issues ranked by impact, with a 0-100 score", Kind: Read, Auth: AuthNone, Cache: "6h", Timeout: "10m",
		Params: []Param{pSite, pMax, pDepth}},
	{Name: "page", Command: "page", Short: "The same checks on one page in more depth, plus the keywords it targets", Kind: Read, Auth: AuthNone, Cache: "1h", Timeout: "90s",
		Params: []Param{pURL}},
	{Name: "keywords", Command: "keywords", Short: "The terms a site targets, from a crawl, with the pages that target each", Kind: Read, Auth: AuthNone, Cache: "6h", Timeout: "90s",
		Params: []Param{pSite, pMax, pLimit}},

	// ---- outside signals (area: signals)
	{Name: "speed", Command: "speed", Short: "PageSpeed Insights (mobile and desktop) plus CrUX real-user Core Web Vitals and trend", Kind: Read, Auth: AuthNone, Cache: "12h", Timeout: "3m",
		Params: []Param{pURL, def(p("strategy", TString, "mobile, desktop or both"), "both")}},
	{Name: "ideas", Command: "ideas", Short: "Autocomplete ideas for a keyword: questions, A-Z, related (no search volumes)", Kind: Read, Auth: AuthNone, Cache: "7d", Timeout: "90s",
		Params: []Param{pos("keyword", "seed keyword"), pLang, pCountry, pLimit}},
	{Name: "authority", Command: "authority", Short: "Open PageRank score and Wayback first-seen date for one or more domains", Kind: Read, Auth: AuthNone, Cache: "7d", Timeout: "90s",
		Params: []Param{rest("domain", "domain to look up")}},

	// ---- rankings through SearXNG (area: rank)
	{Name: "rank", Command: "rank", Short: "Where a site ranks for keywords, per search engine, through SearXNG", Kind: Read, Auth: AuthNone, Cache: "24h", Timeout: "3m",
		Params: []Param{pSite, rest("keyword", "keyword to check"), pLang, def(p("depth", TInt, "result pages to check per keyword"), 3),
			p("gap", TInt, "seconds between search queries (default 3, plus 0-2 s random; 0 for none)")}},

	// ---- side by side and the written note (area: report)
	{Name: "compare", Command: "compare", Short: "Your site next to competitors: audit, speed, authority, keywords, positions", Kind: Read, Auth: AuthNone, Cache: "6h", Timeout: "10m",
		Params: []Param{pSite, rest("competitor", "competitor site"), pKw, pMax, pDepth}},
	{Name: "report", Command: "report", Short: "Write one markdown SEO note for a site to report_dir", Kind: Local, Auth: AuthNone, Timeout: "10m",
		Params: []Param{pSite, p("competitor", TStrings, "a competitor site (repeatable)"), pKw, pMax, pDepth,
			p("out", TString, "folder to write into (default: report_dir from config, else the current folder)")}},

	// ---- your own verified site (area: mine)
	{Name: "login", Command: "login", Short: "Sign in to Google Search Console (OAuth loopback, token goes to the Keychain)", Kind: Local, Auth: AuthLocal, Timeout: "none",
		Params: []Param{{Name: "client", Type: TFile, Required: true, Help: "Google OAuth Desktop client JSON (client_secret.json)"},
			p("yes", TBool, "open the browser sign-in even without a terminal (also SEOX_LOGIN=1)")}},
	{Name: "logout", Command: "logout", Short: "Forget the Google sign-in", Kind: Local, Auth: AuthLocal, Timeout: "90s"},
	{Name: "mine.queries", Command: "mine queries", Short: "Your site's top queries: clicks, impressions, CTR, position (Search Console, Bing)", Kind: Read, Auth: AuthLogin, Cache: "1h", Timeout: "90s",
		Params: []Param{pSite, pDays, pLimit}},
	{Name: "mine.pages", Command: "mine pages", Short: "Your site's top pages: clicks, impressions, CTR, position", Kind: Read, Auth: AuthLogin, Cache: "1h", Timeout: "90s",
		Params: []Param{pSite, pDays, pLimit}},
	{Name: "mine.drops", Command: "mine drops", Short: "Pages and queries losing ground against the previous period of the same length", Kind: Read, Auth: AuthLogin, Cache: "1h", Timeout: "90s",
		Params: []Param{pSite, pDays, pLimit}},
	{Name: "mine.almost", Command: "mine almost", Short: "Queries at average positions 8-20: close to page one", Kind: Read, Auth: AuthLogin, Cache: "1h", Timeout: "90s",
		Params: []Param{pSite, pDays, pLimit}},
	{Name: "setup.verify", Command: "setup verify", Short: "Steps to verify a site in Search Console and Bing, and whether your account already has it", Kind: Read, Auth: AuthNone, Timeout: "90s",
		Params: []Param{pSite}},

	// ---- setup and housekeeping (package cli)
	{Name: "doctor", Command: "doctor", Short: "State of every source with a fix for each: keys, sign-in, SearXNG, report_dir", Kind: Read, Auth: AuthLocal, Timeout: "90s"},
	{Name: "config.show", Command: "config", Short: "Show the config and where it lives", Kind: Read, Auth: AuthLocal, Timeout: "90s"},
	{Name: "config.set", Command: "config set", Short: "Set a config value (searxng_url, report_dir, crawl_max, arshafa_searxng_dir)", Kind: Local, Auth: AuthLocal, Timeout: "90s",
		Params: []Param{pos("key", "config key"), pos("value", "value")}},
	{Name: "cache.clear", Command: "cache clear", Short: "Delete cached crawls and answers", Kind: Local, Auth: AuthLocal, Timeout: "90s"},
	{Name: "key.set", Command: "key set", Short: "Store a key in the Keychain (google, bing or openpagerank; read from stdin)", Kind: Local, Auth: AuthLocal, Timeout: "90s",
		Params: []Param{pos("name", "google, bing or openpagerank")}},
	{Name: "key.remove", Command: "key remove", Short: "Remove a stored key", Kind: Local, Auth: AuthLocal, Timeout: "90s",
		Params: []Param{pos("name", "google, bing or openpagerank")}},
}
