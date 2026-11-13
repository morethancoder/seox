package site

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// inlineTags do not break text into separate sentences; every other element
// does, so n-grams are never built across paragraphs or list items.
var inlineTags = map[string]bool{
	"a": true, "span": true, "b": true, "strong": true, "em": true, "i": true, "u": true, "small": true,
	"code": true, "mark": true, "abbr": true, "sub": true, "sup": true, "font": true, "label": true,
	"cite": true, "q": true, "s": true, "time": true, "bdi": true, "bdo": true,
}

type walker struct {
	p        *Page
	base     *url.URL
	https    bool
	seenHTML bool
	inMain   int

	all, main strings.Builder
	terms     *termCounter
	seenLinks map[string]bool
	titleSeen bool
	mixedSeen map[string]bool
}

// parsePage reads one HTML document into the page record. pageURL is the final
// URL, used to resolve relative links.
func parsePage(p *Page, body []byte, pageURL string) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return
	}
	base, _ := url.Parse(pageURL)
	w := &walker{p: p, base: base, https: base != nil && base.Scheme == "https", terms: newTermCounter(),
		seenLinks: map[string]bool{}, mixedSeen: map[string]bool{}}
	w.walk(doc, false)
	text := w.main.String()
	if len(splitWords(text)) == 0 {
		text = w.all.String()
	}
	p.WordCount = len(splitWords(text))
	w.terms.addText(text, wBody, "body")
	if p.Title != "" {
		w.terms.addText(p.Title, wTitle, "title")
	}
	for _, h := range p.Headings {
		switch h.Level {
		case 1:
			w.terms.addText(h.Text, wH1, "h1")
		case 2:
			w.terms.addText(h.Text, wH23, "h2")
		case 3:
			w.terms.addText(h.Text, wH23, "h3")
		}
	}
	if p.Description != "" {
		w.terms.addText(p.Description, wDescription, "description")
	}
	p.Terms = w.terms.top(40)
	p.Parsed = true
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// textOf is the visible text of a node, with image alt text standing in for images.
func textOf(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
		case html.ElementNode:
			switch n.Data {
			case "script", "style", "noscript", "template":
				return
			case "img":
				if alt, ok := attr(n, "alt"); ok {
					b.WriteString(" " + alt + " ")
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return collapse(b.String())
}

func (w *walker) resolve(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || w.base == nil {
		return "", false
	}
	u, err := w.base.Parse(ref)
	if err != nil {
		return "", false
	}
	return normalizeURL(u.String())
}

func (w *walker) checkMixed(ref string) {
	if !w.https || ref == "" {
		return
	}
	u, err := w.base.Parse(strings.TrimSpace(ref))
	if err != nil || u.Scheme != "http" {
		return
	}
	s := u.String()
	if !w.mixedSeen[s] {
		w.mixedSeen[s] = true
		w.p.MixedContent = append(w.p.MixedContent, s)
	}
}

func (w *walker) walk(n *html.Node, excl bool) {
	switch n.Type {
	case html.TextNode:
		if !excl {
			w.all.WriteString(n.Data)
			if w.inMain > 0 {
				w.main.WriteString(n.Data)
			}
		}
		return
	case html.ElementNode:
		if !w.element(n, &excl) {
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, excl)
	}
	if n.Type == html.ElementNode {
		if n.Data == "main" || n.Data == "article" {
			w.inMain--
		}
		if !inlineTags[n.Data] {
			w.all.WriteString("\n")
			w.main.WriteString("\n")
		}
	}
}

// element handles one element and reports whether to walk its children.
func (w *walker) element(n *html.Node, excl *bool) bool {
	p := w.p
	if !inlineTags[n.Data] {
		w.all.WriteString("\n")
		w.main.WriteString("\n")
	}
	switch n.Data {
	case "main", "article":
		w.inMain++
	case "nav":
		*excl = true
	case "header", "footer", "aside":
		if w.inMain == 0 {
			*excl = true
		}
	case "html":
		if v, ok := attr(n, "lang"); ok {
			p.Lang = strings.TrimSpace(v)
		} else if v, ok := attr(n, "xml:lang"); ok {
			p.Lang = strings.TrimSpace(v)
		}
	case "base":
		if href, ok := attr(n, "href"); ok && w.base != nil {
			if u, err := w.base.Parse(href); err == nil {
				w.base = u
			}
		}
	case "title":
		// An <svg><title> is a tooltip for the icon, not the page title.
		if n.Namespace != "" {
			return false
		}
		p.TitleCount++
		if !w.titleSeen {
			w.titleSeen = true
			p.Title = textOf(n)
		}
		return false
	case "meta":
		w.meta(n)
	case "link":
		w.link(n)
	case "script":
		typ, _ := attr(n, "type")
		if strings.EqualFold(strings.TrimSpace(typ), "application/ld+json") {
			p.JSONLD = append(p.JSONLD, parseJSONLD(textOf0(n)))
		} else if src, ok := attr(n, "src"); ok {
			w.checkMixed(src)
		}
		return false
	case "style", "noscript", "template":
		return false
	case "h1", "h2", "h3", "h4", "h5", "h6":
		p.Headings = append(p.Headings, Heading{Level: int(n.Data[1] - '0'), Text: textOf(n)})
	case "a":
		w.anchor(n, *excl)
	case "img":
		w.image(n)
	case "iframe", "source", "video", "audio", "embed", "track":
		if src, ok := attr(n, "src"); ok {
			w.checkMixed(src)
		}
	case "object":
		if d, ok := attr(n, "data"); ok {
			w.checkMixed(d)
		}
	}
	return true
}

// textOf0 is the raw text inside a node (script bodies are a single text child).
func textOf0(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

func (w *walker) meta(n *html.Node) {
	p := w.p
	content, _ := attr(n, "content")
	content = collapse(content)
	if cs, ok := attr(n, "charset"); ok {
		p.Metas = append(p.Metas, MetaTag{Kind: "charset", Content: cs})
		return
	}
	kind, name := "", ""
	for _, k := range []string{"name", "property", "http-equiv", "itemprop"} {
		if v, ok := attr(n, k); ok && v != "" {
			kind, name = k, v
			break
		}
	}
	if kind == "" {
		return
	}
	tag := MetaTag{Kind: kind, Name: name, Content: content}
	p.Metas = append(p.Metas, tag)
	lower := strings.ToLower(name)
	switch {
	case kind == "name" && lower == "description":
		p.DescriptionCount++
		if p.Description == "" {
			p.Description = content
		}
	case kind == "name" && lower == "robots":
		if p.MetaRobots != "" {
			p.MetaRobots += ", "
		}
		p.MetaRobots += content
	case kind == "name" && lower == "viewport":
		p.Viewport = content
	case strings.HasPrefix(lower, "og:"):
		p.OpenGraph = append(p.OpenGraph, tag)
	}
}

func (w *walker) link(n *html.Node) {
	p := w.p
	rel, _ := attr(n, "rel")
	href, _ := attr(n, "href")
	rels := strings.Fields(strings.ToLower(rel))
	has := func(s string) bool {
		for _, r := range rels {
			if r == s {
				return true
			}
		}
		return false
	}
	switch {
	case has("canonical"):
		p.Canonicals = append(p.Canonicals, strings.TrimSpace(href))
	case has("alternate"):
		if lang, ok := attr(n, "hreflang"); ok {
			p.Hreflangs = append(p.Hreflangs, Hreflang{Lang: strings.TrimSpace(lang), Href: strings.TrimSpace(href)})
		}
	case has("stylesheet"):
		w.checkMixed(href)
	}
}

func (w *walker) anchor(n *html.Node, excl bool) {
	href, ok := attr(n, "href")
	if !ok {
		return
	}
	h := strings.TrimSpace(href)
	low := strings.ToLower(h)
	if h == "" || strings.HasPrefix(h, "#") || strings.HasPrefix(low, "javascript:") ||
		strings.HasPrefix(low, "mailto:") || strings.HasPrefix(low, "tel:") || strings.HasPrefix(low, "data:") {
		return
	}
	text := textOf(n)
	if !excl && text != "" {
		w.terms.addText(text, wAnchor, "anchor")
	}
	target, ok := w.resolve(h)
	if !ok {
		return
	}
	rel, _ := attr(n, "rel")
	nofollow := strings.Contains(" "+strings.ToLower(rel)+" ", " nofollow ")
	key := target + "\x00" + text
	if w.seenLinks[key] {
		return
	}
	w.seenLinks[key] = true
	if len(text) > 120 {
		text = text[:120]
	}
	w.p.Links = append(w.p.Links, Link{URL: target, Anchor: text, Nofollow: nofollow})
}

func (w *walker) image(n *html.Node) {
	p := w.p
	src, _ := attr(n, "src")
	if role, _ := attr(n, "role"); role == "presentation" || role == "none" {
		w.checkMixed(src)
		return
	}
	p.Images++
	if _, ok := attr(n, "alt"); !ok {
		p.ImagesNoAlt++
		if len(p.ImageExamples) < 5 && src != "" {
			p.ImageExamples = append(p.ImageExamples, src)
		}
	}
	w.checkMixed(src)
}

// parseJSONLD checks that a block is valid JSON and collects its @type values,
// including those inside @graph and nested objects.
func parseJSONLD(raw string) JSONLD {
	raw = strings.TrimSpace(raw)
	j := JSONLD{Raw: raw}
	if len(j.Raw) > 2000 {
		j.Raw = j.Raw[:2000]
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		j.Error = err.Error()
		return j
	}
	j.Valid = true
	seen := map[string]bool{}
	var rec func(any)
	rec = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				rec(x)
			}
		case map[string]any:
			switch ty := t["@type"].(type) {
			case string:
				if !seen[ty] {
					seen[ty] = true
					j.Types = append(j.Types, ty)
				}
			case []any:
				for _, x := range ty {
					if s, ok := x.(string); ok && !seen[s] {
						seen[s] = true
						j.Types = append(j.Types, s)
					}
				}
			}
			for k, x := range t {
				if k != "@type" {
					rec(x)
				}
			}
		}
	}
	rec(v)
	sortStrings(j.Types)
	return j
}

var langRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func containsToken(list, tok string) bool {
	for _, t := range strings.FieldsFunc(strings.ToLower(list), func(r rune) bool { return r == ',' || r == ' ' }) {
		if t == tok {
			return true
		}
	}
	return false
}
