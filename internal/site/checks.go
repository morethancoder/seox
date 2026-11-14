package site

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// Severity of an issue.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

const maxIssueURLs = 10

// Score formula (see scoreIssues): 100 minus the sum, over every issue found,
// of  weight(severity) * share.  Weights are high 20, medium 8, low 3.
// share is 1 for a site-wide issue (robots.txt, sitemap, https). For a
// page-level issue it is affected_pages / pages_crawled, so the same problem
// costs a 5-page site more than a 500-page one, but never less than 0.1 of the
// weight: a single broken page still shows in the score. The result is
// clamped to 0-100 and rounded.
var severityWeight = map[string]float64{High: 20, Medium: 8, Low: 3}

const minShare = 0.1

// Issue is one finding. ID is stable: agents and the report note key on it.
type Issue struct {
	ID       string   `json:"id"`
	Severity string   `json:"severity"`
	Summary  string   `json:"summary"`
	Fix      string   `json:"fix"`
	Scope    string   `json:"scope"` // "site" or "page"
	Count    int      `json:"count"` // affected pages (1 for a site issue)
	URLs     []string `json:"urls,omitempty"`
	Examples []string `json:"examples,omitempty"` // short detail lines, e.g. "page -> broken link"
	Points   float64  `json:"points"`             // deducted from the score
}

// issueBuilder collects issues in a fixed order so output is stable.
type issueBuilder struct{ list []*Issue }

func (b *issueBuilder) site(id, sev, summary, fix string, detail ...string) {
	b.list = append(b.list, &Issue{ID: id, Severity: sev, Summary: summary, Fix: fix, Scope: "site", Count: 1, Examples: capStrings(detail, maxIssueURLs)})
}

func (b *issueBuilder) pages(id, sev, summary, fix string, urls []string, detail []string) {
	if len(urls) == 0 {
		return
	}
	b.list = append(b.list, &Issue{ID: id, Severity: sev, Summary: summary, Fix: fix, Scope: "page", Count: len(urls),
		URLs: capStrings(urls, maxIssueURLs), Examples: capStrings(detail, maxIssueURLs)})
}

func capStrings(s []string, n int) []string {
	if len(s) > n {
		s = s[:n]
	}
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

func effStatus(p *Page) int {
	if p.FinalStatus != 0 {
		return p.FinalStatus
	}
	return p.Status
}

// pageScope says which checks run: the whole site (duplicates, robots.txt,
// sitemap) or one page.
type pageScope bool

const (
	scopeSite pageScope = true
	scopePage pageScope = false
)

// analyse runs every check over a crawl and returns the issues ranked by
// severity and impact, with the score points each one costs.
func analyse(cr *CrawlResult, scope pageScope) ([]Issue, int, []string) {
	b := &issueBuilder{}
	pages := cr.Pages
	byKey := map[string]*Page{}
	for _, p := range pages {
		byKey[dedupKey(p.URL)] = p
	}
	var (
		unreachable, s4xx, s5xx, chain, redirBroken, redirInternal, redirTemp []string
		noTitle, longTitle, shortTitle, multiTitle                            []string
		noDesc, longDesc, shortDesc, multiDesc                                []string
		noH1, multiH1, skipH, emptyH                                          []string
		noCanon, multiCanon, badCanon, elsewhereCanon                         []string
		noindex, mixed, noOG, partOG, noLD, badLD, noAlt                      []string
		thin, noLang, badLang, noViewport, badHreflang, noSelfHreflang, slow  []string
		altDetail, mixedDetail, badLDDetail, hreflangDetail, canonDetail      []string
		brokenInt, brokenIntDetail, brokenExt, brokenExtDetail                []string
		timedOut, timedOutDetail, timedOutPages                               []string
	)
	titles, descs := map[string][]string{}, map[string][]string{}
	hrefTargets, langPrefixes := hreflangIndex(pages)
	var noHreflang []string
	for _, p := range pages {
		u := p.URL
		eff := effStatus(p)
		switch {
		case p.Error != "" && strings.Contains(p.Error, "redirect"):
			redirBroken = append(redirBroken, u)
		case p.Error != "" && p.Timeout:
			timedOutPages = append(timedOutPages, u)
		case p.Error != "":
			unreachable = append(unreachable, u)
		case eff >= 500:
			s5xx = append(s5xx, u)
		case eff >= 400:
			s4xx = append(s4xx, u)
		}
		if n := len(p.Redirects); n >= 2 {
			chain = append(chain, u)
		} else if n == 1 && p.Error == "" {
			redirInternal = append(redirInternal, u)
		}
		if len(p.Redirects) > 0 {
			switch p.Redirects[0].Status {
			case 302, 303, 307:
				redirTemp = append(redirTemp, u)
			}
		}
		if p.TimeMS > 2000 {
			slow = append(slow, u)
		}
		if !p.Parsed {
			continue
		}
		// ---- on-page checks, only for pages that returned HTML
		switch tl := utf8.RuneCountInString(p.Title); {
		case p.Title == "":
			noTitle = append(noTitle, u)
		case tl > 60:
			longTitle = append(longTitle, u)
		case tl < 15:
			shortTitle = append(shortTitle, u)
		}
		if p.TitleCount > 1 {
			multiTitle = append(multiTitle, u)
		}
		if p.Title != "" {
			k := strings.ToLower(p.Title)
			titles[k] = append(titles[k], u)
		}
		switch dl := utf8.RuneCountInString(p.Description); {
		case p.Description == "":
			noDesc = append(noDesc, u)
		case dl > 160:
			longDesc = append(longDesc, u)
		case dl < 70:
			shortDesc = append(shortDesc, u)
		}
		if p.DescriptionCount > 1 {
			multiDesc = append(multiDesc, u)
		}
		if p.Description != "" {
			k := strings.ToLower(p.Description)
			descs[k] = append(descs[k], u)
		}
		h1s, last, skipped, empty := 0, 0, false, false
		for _, h := range p.Headings {
			if h.Level == 1 {
				h1s++
			}
			if last > 0 && h.Level > last+1 {
				skipped = true
			}
			if h.Text == "" {
				empty = true
			}
			last = h.Level
		}
		if h1s == 0 {
			noH1 = append(noH1, u)
		} else if h1s > 1 {
			multiH1 = append(multiH1, u)
		}
		if skipped {
			skipH = append(skipH, u)
		}
		if empty {
			emptyH = append(emptyH, u)
		}
		checkCanonical(p, &noCanon, &multiCanon, &badCanon, &elsewhereCanon, &canonDetail)
		if p.Noindex() {
			noindex = append(noindex, u)
		}
		if len(p.MixedContent) > 0 {
			mixed = append(mixed, u)
			mixedDetail = append(mixedDetail, fmt.Sprintf("%s loads %d http:// resources, e.g. %s", u, len(p.MixedContent), p.MixedContent[0]))
		}
		og := map[string]bool{}
		for _, t := range p.OpenGraph {
			if t.Content != "" {
				og[strings.ToLower(t.Name)] = true
			}
		}
		switch {
		case len(p.OpenGraph) == 0:
			noOG = append(noOG, u)
		case !og["og:title"] || !og["og:description"] || !og["og:image"]:
			partOG = append(partOG, u)
		}
		if len(p.JSONLD) == 0 {
			noLD = append(noLD, u)
		}
		for _, j := range p.JSONLD {
			if !j.Valid {
				badLD = append(badLD, u)
				badLDDetail = append(badLDDetail, u+": "+j.Error)
				break
			}
		}
		if p.ImagesNoAlt > 0 {
			noAlt = append(noAlt, u)
			altDetail = append(altDetail, fmt.Sprintf("%s: %d of %d images have no alt attribute", u, p.ImagesNoAlt, p.Images))
		}
		if eff >= 200 && eff < 300 && p.WordCount < 200 {
			thin = append(thin, u)
		}
		switch {
		case p.Lang == "":
			noLang = append(noLang, u)
		case !langRe.MatchString(p.Lang):
			badLang = append(badLang, u)
		}
		if p.Viewport == "" {
			noViewport = append(noViewport, u)
		}
		checkHreflang(p, &badHreflang, &noSelfHreflang, &hreflangDetail)
		if len(p.Hreflangs) == 0 && hasTranslation(p, byKey, hrefTargets, langPrefixes) {
			noHreflang = append(noHreflang, u)
		}
		// ---- links
		for _, l := range p.Links {
			if l.Internal {
				if t := byKey[dedupKey(l.URL)]; t != nil {
					te := effStatus(t)
					if t.Error != "" && t.Timeout {
						timedOut = appendUnique(timedOut, u)
						timedOutDetail = append(timedOutDetail, fmt.Sprintf("%s -> %s (timeout)", u, l.URL))
					} else if te >= 400 || (t.Error != "" && !strings.Contains(t.Error, "redirect")) {
						brokenInt = appendUnique(brokenInt, u)
						brokenIntDetail = append(brokenIntDetail, fmt.Sprintf("%s -> %s (%s)", u, l.URL, statusText(t)))
					}
				} else if st, ok := cr.External[l.URL]; ok && linkTimedOut(st) {
					timedOut = appendUnique(timedOut, u)
					timedOutDetail = append(timedOutDetail, fmt.Sprintf("%s -> %s (timeout)", u, l.URL))
				} else if st, ok := cr.External[l.URL]; ok && linkBroken(st) {
					// A single-page run tests its own internal links directly.
					brokenInt = appendUnique(brokenInt, u)
					brokenIntDetail = append(brokenIntDetail, fmt.Sprintf("%s -> %s (%s)", u, l.URL, extText(st)))
				}
			} else if st, ok := cr.External[l.URL]; ok && linkTimedOut(st) {
				timedOut = appendUnique(timedOut, u)
				timedOutDetail = append(timedOutDetail, fmt.Sprintf("%s -> %s (timeout)", u, l.URL))
			} else if st, ok := cr.External[l.URL]; ok && linkBroken(st) {
				brokenExt = appendUnique(brokenExt, u)
				brokenExtDetail = append(brokenExtDetail, fmt.Sprintf("%s -> %s (%s)", u, l.URL, extText(st)))
			}
		}
	}

	// ---- build in a fixed order
	b.pages("page-unreachable", High, "The URL could not be fetched (DNS failure, refused connection or another network error)", "Check the server is up and the URL is right; fix or remove links to it.", unreachable, nil)
	b.pages("page-timeout", Low, "The URL could not be checked (timeout)", "The server may be slow or busy, not down. Re-test it; if it stays slow, fix the server.", timedOutPages, nil)
	b.pages("status-5xx", High, "The URL answers with a server error (5xx)", "Fix the server error, or remove links to the URL until it works.", s5xx, nil)
	b.pages("status-4xx", High, "The URL answers 404 or another client error", "Restore the page, redirect it to its replacement, or remove the links and sitemap entries.", s4xx, nil)
	b.pages("broken-link-internal", High, "Pages link to internal URLs that return an error", "Update or remove each link, or redirect the old URL to a live page.", brokenInt, brokenIntDetail)
	b.pages("redirect-broken", High, "A redirect loops, is too long or points somewhere unusable", "Make the old URL redirect once, straight to a working final URL.", redirBroken, nil)
	b.pages("redirect-chain", Medium, "A URL goes through two or more redirects", "Redirect the first URL straight to the final one and update internal links to it.", chain, nil)
	b.pages("redirect-temporary", Low, "A URL uses a temporary redirect (302/303/307)", "Use a 301 or 308 when the move is permanent.", redirTemp, nil)
	b.pages("redirect-internal", Low, "Internal links point at URLs that redirect", "Link straight to the final URL to save a request and keep link signals whole.", redirInternal, nil)
	b.pages("broken-link-external", Medium, "Pages link to outside URLs that are gone (404, 410, 5xx or no answer)", "Replace the link with a working source or remove it.", brokenExt, brokenExtDetail)

	b.pages("link-unchecked-timeout", Low, "Links that could not be checked (timeout)", "These links may be fine. Open them to check; seox did not count them as broken.", timedOut, timedOutDetail)

	b.pages("title-missing", High, "The page has no <title>", "Add a unique title of 15 to 60 characters that names the page's topic.", noTitle, nil)
	b.pages("title-duplicate", Medium, "Several pages share one title", "Give each page its own title.", dupGroups(titles), dupDetail(titles))
	b.pages("title-too-long", Low, "The title is over 60 characters and may be cut in results", "Shorten the title to 60 characters or less, with the main words first.", longTitle, nil)
	b.pages("title-too-short", Low, "The title is under 15 characters", "Expand the title so it describes the page.", shortTitle, nil)
	b.pages("title-multiple", Low, "The page has more than one <title>", "Keep a single <title> in <head>.", multiTitle, nil)
	b.pages("description-missing", Medium, "The page has no meta description", "Add a meta description of 70 to 160 characters that sells the click.", noDesc, nil)
	b.pages("description-duplicate", Medium, "Several pages share one meta description", "Write a unique description for each page.", dupGroups(descs), dupDetail(descs))
	b.pages("description-too-long", Low, "The meta description is over 160 characters and may be cut", "Shorten it to 160 characters or less.", longDesc, nil)
	b.pages("description-too-short", Low, "The meta description is under 70 characters", "Expand it to describe the page in 70 to 160 characters.", shortDesc, nil)
	b.pages("description-multiple", Low, "The page has more than one meta description", "Keep one meta description.", multiDesc, nil)
	b.pages("h1-missing", Medium, "The page has no <h1>", "Add one <h1> that states the page's topic.", noH1, nil)
	b.pages("h1-multiple", Low, "The page has more than one <h1>", "Use one <h1> and demote the others to <h2>.", multiH1, nil)
	b.pages("heading-skip", Low, "Heading levels jump (for example h2 straight to h4)", "Use headings in order without skipping a level.", skipH, nil)
	b.pages("heading-empty", Low, "The page has an empty heading", "Fill or remove the empty heading.", emptyH, nil)
	b.pages("canonical-missing", Low, "The page has no canonical link", "Add <link rel=\"canonical\"> pointing at the page's preferred URL.", noCanon, nil)
	b.pages("canonical-multiple", Medium, "The page has several canonical links", "Keep exactly one canonical link.", multiCanon, nil)
	b.pages("canonical-invalid", Medium, "The canonical link is not a usable URL", "Use a full http(s) URL in the canonical link.", badCanon, canonDetail)
	b.pages("canonical-elsewhere", Low, "The canonical points to a different URL, so this page is left out of the index", "Check that this is intended; otherwise point the canonical at the page itself.", elsewhereCanon, nil)
	b.pages("noindex", Medium, "The page asks search engines not to index it (noindex)", "Remove noindex from the meta robots tag or X-Robots-Tag header if the page should rank.", noindex, nil)
	b.pages("mixed-content", Medium, "An https page loads resources over http", "Load every resource over https.", mixed, mixedDetail)
	b.pages("og-missing", Low, "The page has no Open Graph tags", "Add og:title, og:description and og:image for better link previews.", noOG, nil)
	b.pages("og-incomplete", Low, "Open Graph is missing og:title, og:description or og:image", "Add the missing Open Graph tags.", partOG, nil)
	b.pages("jsonld-invalid", Medium, "A JSON-LD block does not parse", "Fix the JSON syntax; test it with Google's Rich Results Test.", badLD, badLDDetail)
	b.pages("structured-data-missing", Low, "The page has no JSON-LD structured data", "Add JSON-LD (Article, Product, Organization or similar) where it fits the page.", noLD, nil)
	b.pages("image-alt-missing", Medium, "Images have no alt attribute", "Add descriptive alt text, or alt=\"\" for decorative images.", noAlt, altDetail)
	b.pages("thin-content", Medium, "The page has under 200 words of main content", "Add useful content, or merge or noindex the page.", thin, nil)
	b.pages("lang-missing", Medium, "The <html> tag has no lang attribute", "Add lang=\"en\" (or the page's language) to <html>.", noLang, nil)
	b.pages("lang-invalid", Low, "The <html lang> value is not a valid language code", "Use a code such as en, ar or en-US.", badLang, nil)
	b.pages("viewport-missing", Medium, "The page has no viewport meta tag", "Add <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">.", noViewport, nil)
	b.pages("hreflang-invalid", Medium, "An hreflang value or URL is invalid", "Use language codes like en or en-GB (or x-default) and absolute URLs.", badHreflang, hreflangDetail)
	b.pages("hreflang-no-self", Low, "The hreflang set does not include the page itself", "Add a self-referencing hreflang entry.", noSelfHreflang, nil)
	b.pages("hreflang-missing", Low, "This page has a translation on the site but declares no hreflang", "Add hreflang links to this page's translations.", noHreflang, nil)
	b.pages("response-slow", Low, "The server took over 2 seconds to answer", "Look at hosting, caching and server work for this URL.", slow, nil)

	if scope == scopeSite {
		siteChecks(b, cr)
	} else if !cr.HTTPSHome && len(pages) > 0 {
		b.site("https-missing", High, "The page is served over http, not https", "Serve the site over https and redirect http to it.")
	}
	var warns []string
	if len(timedOut) > 0 {
		warns = append(warns, fmt.Sprintf("%d pages have links that timed out and could not be checked; they are not counted as broken", len(timedOut)))
	}
	if len(timedOutPages) > 0 {
		warns = append(warns, fmt.Sprintf("%d URLs timed out and could not be checked; they are not counted as unreachable", len(timedOutPages)))
	}
	issues, score := rankIssues(b.list, len(pages))
	return issues, score, warns
}

func siteChecks(b *issueBuilder, cr *CrawlResult) {
	if !cr.HTTPSHome {
		b.site("https-missing", High, "The site is served over http, not https", "Install a certificate and serve the whole site over https.")
	} else if cr.HTTPRedirects != nil && !*cr.HTTPRedirects {
		b.site("http-not-redirected", Medium, "The http:// version of the site does not redirect to https://", "Add a site-wide 301 from http to https.")
	}
	if !cr.Robots.Found {
		b.site("robots-missing", Low, "The site has no robots.txt", "Add /robots.txt (even an allow-all file) and list the sitemap in it.")
	}
	if len(cr.BlockedRobots) > 0 {
		b.pages("blocked-by-robots", Low, "Internal links point at URLs that robots.txt blocks", "Check the Disallow rules do not hide pages you want indexed.", cr.BlockedRobots, nil)
	}
	switch {
	case !cr.Sitemap.Found && len(cr.Sitemap.Errors) == 0:
		b.site("sitemap-missing", Medium, "No sitemap.xml was found", "Publish /sitemap.xml and reference it from robots.txt.")
	case len(cr.Sitemap.Errors) > 0:
		b.site("sitemap-invalid", Medium, "A sitemap could not be read or parsed", "Fix the sitemap so it is valid XML (urlset or sitemapindex).", cr.Sitemap.Errors...)
	}
	var bad, detail []string
	for _, p := range cr.Pages {
		if !p.InSitemap {
			continue
		}
		switch {
		case p.Error != "":
			bad = append(bad, p.URL)
			detail = append(detail, p.URL+": "+p.Error)
		case effStatus(p) >= 400:
			bad = append(bad, p.URL)
			detail = append(detail, fmt.Sprintf("%s: HTTP %d", p.URL, effStatus(p)))
		case len(p.Redirects) > 0:
			bad = append(bad, p.URL)
			detail = append(detail, fmt.Sprintf("%s: redirects to %s", p.URL, p.FinalURL))
		case p.Noindex():
			bad = append(bad, p.URL)
			detail = append(detail, p.URL+": noindex")
		}
	}
	b.pages("sitemap-url-error", Medium, "The sitemap lists URLs that error, redirect or are noindex", "List only live, indexable, final URLs in the sitemap.", bad, detail)
}

func checkCanonical(p *Page, none, multi, bad, elsewhere, detail *[]string) {
	switch len(p.Canonicals) {
	case 0:
		*none = append(*none, p.URL)
		return
	case 1:
	default:
		distinct := map[string]bool{}
		for _, c := range p.Canonicals {
			distinct[c] = true
		}
		if len(distinct) > 1 {
			*multi = append(*multi, p.URL)
		}
	}
	c := p.Canonicals[0]
	base := p.FinalURL
	if base == "" {
		base = p.URL
	}
	bu, _ := url.Parse(base)
	cu, err := url.Parse(c)
	if err == nil && bu != nil {
		cu = bu.ResolveReference(cu)
	}
	if err != nil || c == "" || cu == nil || (cu.Scheme != "http" && cu.Scheme != "https") || cu.Host == "" {
		*bad = append(*bad, p.URL)
		*detail = append(*detail, fmt.Sprintf("%s: %q", p.URL, c))
		return
	}
	n, ok := normalizeURL(cu.String())
	if ok && dedupKey(n) != dedupKey(base) {
		*elsewhere = append(*elsewhere, p.URL)
	}
}

func checkHreflang(p *Page, bad, noSelf *[]string, detail *[]string) {
	if len(p.Hreflangs) == 0 {
		return
	}
	self := false
	invalid := ""
	base := p.FinalURL
	if base == "" {
		base = p.URL
	}
	for _, h := range p.Hreflangs {
		if h.Lang != "x-default" && !langRe.MatchString(h.Lang) {
			invalid = fmt.Sprintf("bad language code %q", h.Lang)
		}
		hu, err := url.Parse(h.Href)
		if err != nil || (hu.Scheme != "http" && hu.Scheme != "https") || hu.Host == "" {
			invalid = fmt.Sprintf("hreflang %q needs an absolute URL, got %q", h.Lang, h.Href)
			continue
		}
		if n, ok := normalizeURL(h.Href); ok && dedupKey(n) == dedupKey(base) {
			self = true
		}
	}
	if invalid != "" {
		*bad = append(*bad, p.URL)
		*detail = append(*detail, p.URL+": "+invalid)
	}
	if !self {
		*noSelf = append(*noSelf, p.URL)
	}
}

func dupGroups(m map[string][]string) []string {
	var out []string
	for _, k := range sortedKeys(m) {
		if len(m[k]) > 1 {
			out = append(out, m[k]...)
		}
	}
	return out
}

func dupDetail(m map[string][]string) []string {
	var out []string
	for _, k := range sortedKeys(m) {
		if len(m[k]) > 1 {
			s := k
			if len(s) > 60 {
				s = s[:60] + "..."
			}
			out = append(out, fmt.Sprintf("%q is on %d pages, e.g. %s", s, len(m[k]), m[k][0]))
		}
	}
	return out
}

func sortedKeys(m map[string][]string) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}

func appendUnique(s []string, v string) []string {
	if len(s) > 0 && s[len(s)-1] == v {
		return s
	}
	return append(s, v)
}

func statusText(p *Page) string {
	if p.Error != "" {
		return p.Error
	}
	return fmt.Sprintf("HTTP %d", effStatus(p))
}

func extText(st ExtStatus) string {
	if st.Status != 0 {
		return fmt.Sprintf("HTTP %d", st.Status)
	}
	return st.Error
}

// rankIssues applies the score formula and sorts: severity, then points, then id.
func rankIssues(list []*Issue, pagesCrawled int) ([]Issue, int) {
	total := 0.0
	out := make([]Issue, 0, len(list))
	for _, is := range list {
		share := 1.0
		if is.Scope == "page" && pagesCrawled > 0 {
			share = float64(is.Count) / float64(pagesCrawled)
			if share < minShare {
				share = minShare
			}
			if share > 1 {
				share = 1
			}
		}
		is.Points = round1(severityWeight[is.Severity] * share)
		total += severityWeight[is.Severity] * share
		out = append(out, *is)
	}
	rank := map[string]int{High: 0, Medium: 1, Low: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		if out[i].Points != out[j].Points {
			return out[i].Points > out[j].Points
		}
		return out[i].ID < out[j].ID
	})
	score := int(100 - total + 0.5)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return out, score
}

// hreflangIndex collects every URL some page lists in its hreflang (keyed by
// dedupKey, with the listing page remembered) and the language path prefixes
// the site uses. A prefix is learnt only from a declared hreflang whose URL
// starts with its own language code, e.g. ru -> /ru/..., so unrelated first
// path segments such as /blog/ are never mistaken for a language.
func hreflangIndex(pages []*Page) (map[string]map[string]bool, map[string]bool) {
	targets := map[string]map[string]bool{}
	prefixes := map[string]bool{}
	for _, p := range pages {
		for _, h := range p.Hreflangs {
			n, ok := normalizeURL(h.Href)
			if !ok {
				continue
			}
			k := dedupKey(n)
			if targets[k] == nil {
				targets[k] = map[string]bool{}
			}
			targets[k][dedupKey(p.URL)] = true
			hu, err := url.Parse(n)
			if err != nil {
				continue
			}
			seg := firstSegment(hu.EscapedPath())
			lang := strings.ToLower(h.Lang)
			if seg != "" && (strings.ToLower(seg) == lang || strings.ToLower(seg) == strings.SplitN(lang, "-", 2)[0]) {
				prefixes[strings.ToLower(seg)] = true
			}
		}
	}
	return targets, prefixes
}

func firstSegment(path string) string {
	return strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
}

// hasTranslation reports whether a page that declares no hreflang has a
// translation on the crawled site: the same path under another language
// prefix (or none, for the default language), or another page naming this
// page in its hreflang. Posts written per language have neither, so they are
// not flagged.
func hasTranslation(p *Page, byKey map[string]*Page, targets map[string]map[string]bool, prefixes map[string]bool) bool {
	self := dedupKey(p.URL)
	for from := range targets[self] {
		if from != self {
			return true
		}
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return false
	}
	path := u.EscapedPath()
	rest := path
	if seg := firstSegment(path); prefixes[strings.ToLower(seg)] {
		rest = strings.TrimPrefix(path, "/"+seg)
		if rest == "" {
			rest = "/"
		}
	}
	try := func(pre string) bool {
		c := *u
		c.RawQuery = ""
		if pre == "" {
			c.Path, c.RawPath = rest, ""
		} else if rest == "/" {
			c.Path, c.RawPath = "/"+pre+"/", ""
		} else {
			c.Path, c.RawPath = "/"+pre+rest, ""
		}
		k := dedupKey(c.String())
		return k != self && byKey[k] != nil
	}
	if try("") {
		return true
	}
	for pre := range prefixes {
		if try(pre) {
			return true
		}
	}
	return false
}
