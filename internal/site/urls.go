package site

import (
	"net/url"
	"path"
	"sort"
	"strings"
)

// trackingParams are dropped when normalising: they change the URL but never
// the page, so keeping them would make the crawler fetch one page many times.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "msclkid": true, "mc_cid": true, "mc_eid": true, "_ga": true,
}

// normalizeURL returns the canonical text form of a URL the crawler uses as an
// identity: lowercase scheme and host, default port removed, fragment dropped,
// dot segments resolved, an empty path made "/", tracking parameters removed
// and the query sorted. The trailing slash of a non-root path is kept as the
// site wrote it, because /a and /a/ can be different pages; a redirect between
// them is reported by the redirect checks and the crawler dedups the target.
func normalizeURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	u.Scheme = scheme
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	u.Host = host
	u.User = nil
	u.Fragment = ""
	u.RawFragment = ""
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") && clean != "/" {
		clean += "/"
	}
	if clean == "." {
		clean = "/"
	}
	u.Path, _ = url.PathUnescape(clean)
	u.RawPath = clean
	u.RawQuery = sortedQuery(u.Query())
	return u.String(), true
}

func sortedQuery(q url.Values) string {
	for k := range q {
		if strings.HasPrefix(k, "utm_") || trackingParams[k] {
			delete(q, k)
		}
	}
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
		sort.Strings(q[k])
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// hostKey is a host with "www." removed, so the bare domain and www are one site.
func hostKey(host string) string {
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}

// sameSite reports whether two normalised URLs are on the same site.
func sameSite(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return hostKey(ua.Host) == hostKey(ub.Host)
}

// dedupKey is the identity used to decide whether a URL was already seen: it
// ignores the scheme and "www." so http/https and www/apex variants of one page
// are fetched once.
func dedupKey(norm string) string {
	u, err := url.Parse(norm)
	if err != nil {
		return norm
	}
	k := hostKey(u.Host) + u.EscapedPath()
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}

// nonHTMLExt are file extensions the crawler does not fetch: they are never
// HTML, and downloading them would waste the polite request budget.
var nonHTMLExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true, ".avif": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".rar": true, ".7z": true, ".dmg": true, ".exe": true,
	".css": true, ".js": true, ".json": true, ".xml": true, ".txt": true, ".rss": true, ".atom": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".webm": true, ".wav": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
}

func hasNonHTMLExt(norm string) bool {
	u, err := url.Parse(norm)
	if err != nil {
		return false
	}
	return nonHTMLExt[strings.ToLower(path.Ext(u.Path))]
}

// parseSite turns what the user typed (a domain, a host or a URL) into the
// normalised start URL. A bare domain gets https.
func parseSite(in string) (string, bool) {
	in = strings.TrimSpace(in)
	if in == "" || strings.ContainsAny(in, " \t") {
		return "", false
	}
	if !strings.Contains(in, "://") {
		in = "https://" + in
	}
	return normalizeURL(in)
}
