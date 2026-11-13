package site

import (
	"encoding/xml"
	"strings"
)

// Caps keep a huge site from turning the sitemap step into a crawl of its own.
const (
	maxSitemapFiles = 10
	maxSitemapURLs  = 5000
)

type sitemapDoc struct {
	XMLName  xml.Name
	URLs     []sitemapLoc `xml:"url"`
	Sitemaps []sitemapLoc `xml:"sitemap"`
}

type sitemapLoc struct {
	Loc string `xml:"loc"`
}

// parseSitemap reads one sitemap file. For a sitemap index it returns the
// child sitemap URLs; for a urlset it returns the page URLs.
func parseSitemap(body []byte) (pages, children []string, err error) {
	var d sitemapDoc
	if err := xml.Unmarshal(body, &d); err != nil {
		return nil, nil, err
	}
	switch strings.ToLower(d.XMLName.Local) {
	case "urlset":
	case "sitemapindex":
	default:
		return nil, nil, errNotSitemap
	}
	for _, u := range d.URLs {
		if l := strings.TrimSpace(u.Loc); l != "" {
			pages = append(pages, l)
		}
	}
	for _, s := range d.Sitemaps {
		if l := strings.TrimSpace(s.Loc); l != "" {
			children = append(children, l)
		}
	}
	return pages, children, nil
}

type sitemapError string

func (e sitemapError) Error() string { return string(e) }

const errNotSitemap = sitemapError("not a sitemap: root element is neither urlset nor sitemapindex")
