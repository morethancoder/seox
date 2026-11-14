package site

import "testing"

func hrefPage(u string, hl ...Hreflang) *Page {
	return &Page{URL: u, Status: 200, Parsed: true, Hreflangs: hl}
}

func missingURLs(pages []*Page) map[string]bool {
	issues, _, _ := analyse(&CrawlResult{Pages: pages, Host: "example.com"}, scopeSite)
	out := map[string]bool{}
	for _, i := range issues {
		if i.ID == "hreflang-missing" {
			for _, u := range i.URLs {
				out[u] = true
			}
		}
	}
	return out
}

func TestHreflangMissingNeedsATranslation(t *testing.T) {
	base := "https://example.com"
	pages := []*Page{
		hrefPage(base+"/", Hreflang{"en", base + "/"}, Hreflang{"ru", base + "/ru/"}),
		hrefPage(base+"/ru/", Hreflang{"en", base + "/"}, Hreflang{"ru", base + "/ru/"}),
		// Translated pair, neither declares hreflang: both flagged.
		hrefPage(base + "/about/"),
		hrefPage(base + "/ru/about/"),
		// Per-language posts with different slugs: not flagged.
		hrefPage(base + "/blog/one/"),
		hrefPage(base + "/ru/blog/odin/"),
		// Named by another page's hreflang: flagged.
		hrefPage(base + "/pricing/"),
		hrefPage(base+"/ru/tseny/", Hreflang{"en", base + "/pricing/"}, Hreflang{"ru", base + "/ru/tseny/"}),
	}
	got := missingURLs(pages)
	for _, u := range []string{base + "/about/", base + "/ru/about/", base + "/pricing/"} {
		if !got[u] {
			t.Errorf("%s should be flagged, got %v", u, got)
		}
	}
	for _, u := range []string{base + "/blog/one/", base + "/ru/blog/odin/"} {
		if got[u] {
			t.Errorf("%s has no translation and must not be flagged", u)
		}
	}
}
