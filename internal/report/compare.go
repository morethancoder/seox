package report

import (
	"context"
	"sort"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/signals"
	"github.com/morethancoder/seox/internal/site"
	"strings"
)

func init() {
	ops.Handle("compare", handleCompare)
	ops.Handle("report", handleReport)
}

// SiteKeywords is how one site's target keywords relate to the others.
type SiteKeywords struct {
	Site string `json:"site"`
	// Shared: terms also on your site (for your own site: terms that at least one competitor has).
	Shared []string `json:"shared"`
	// Unique: terms found on this site and on none of the other compared sites.
	Unique []string `json:"unique"`
}

// KeywordGap is a term at least one competitor targets and your site does not.
type KeywordGap struct {
	Term  string   `json:"term"`
	Sites []string `json:"sites"`
}

// KeywordCompare is the keyword side of compare.
type KeywordCompare struct {
	Sites []SiteKeywords `json:"sites"` // your site first
	Gaps  []KeywordGap   `json:"gaps"`
	Note  string         `json:"note"`
}

// CompareResult is the outcome of `seox compare`.
type CompareResult struct {
	Site      string         `json:"site"`
	Sites     []*siteData    `json:"sites"` // your site first
	Keywords  KeywordCompare `json:"keywords"`
	Keyword   []string       `json:"position_keywords,omitempty"`
	Positions string         `json:"positions_note,omitempty"`
}

const (
	termCap      = 25
	keywordsNote = "terms come from each site's crawl (title, headings, description, anchors and body, weighted); lists are the top terms by score, capped at 25"
)

type namedTerms struct {
	name  string
	terms []site.SiteKeyword
}

// compareKeywords works out shared, unique and gap terms. The first entry is
// your site. Terms keep the order of their score, best first.
func compareKeywords(sites []namedTerms) KeywordCompare {
	kc := KeywordCompare{Sites: []SiteKeywords{}, Gaps: []KeywordGap{}, Note: keywordsNote}
	if len(sites) == 0 {
		return kc
	}
	holders := map[string][]int{} // term -> indexes of the sites that have it
	for i, s := range sites {
		for _, t := range s.terms {
			holders[t.Term] = append(holders[t.Term], i)
		}
	}
	ordered := func(i int) []site.SiteKeyword {
		t := append([]site.SiteKeyword(nil), sites[i].terms...)
		sort.SliceStable(t, func(a, b int) bool { return t[a].Score > t[b].Score })
		return t
	}
	for i, s := range sites {
		sk := SiteKeywords{Site: s.name, Shared: []string{}, Unique: []string{}}
		for _, t := range ordered(i) {
			h := holders[t.Term]
			switch {
			case len(h) == 1:
				sk.Unique = append(sk.Unique, t.Term)
			case i == 0:
				sk.Shared = append(sk.Shared, t.Term)
			default:
				for _, j := range h {
					if j == 0 {
						sk.Shared = append(sk.Shared, t.Term)
					}
				}
			}
		}
		sk.Shared, sk.Unique = capList(sk.Shared), capList(sk.Unique)
		kc.Sites = append(kc.Sites, sk)
	}
	// Gaps: competitor terms your site lacks, the ones most competitors share first.
	type gap struct {
		term  string
		sites []string
		score float64
		first int
	}
	var gaps []*gap
	seen := map[string]*gap{}
	for i := 1; i < len(sites); i++ {
		for _, t := range ordered(i) {
			if len(holders[t.Term]) > 0 && holders[t.Term][0] == 0 {
				continue
			}
			g := seen[t.Term]
			if g == nil {
				g = &gap{term: t.Term, first: len(gaps)}
				seen[t.Term] = g
				gaps = append(gaps, g)
			}
			g.sites = append(g.sites, sites[i].name)
			if t.Score > g.score {
				g.score = t.Score
			}
		}
	}
	sort.SliceStable(gaps, func(a, b int) bool {
		if len(gaps[a].sites) != len(gaps[b].sites) {
			return len(gaps[a].sites) > len(gaps[b].sites)
		}
		if gaps[a].score != gaps[b].score {
			return gaps[a].score > gaps[b].score
		}
		return gaps[a].first < gaps[b].first
	})
	for _, g := range gaps {
		if len(kc.Gaps) == termCap {
			break
		}
		kc.Gaps = append(kc.Gaps, KeywordGap{Term: g.term, Sites: g.sites})
	}
	return kc
}

func capList(s []string) []string {
	if len(s) > termCap {
		return s[:termCap]
	}
	return s
}

func keywordCompareOf(g *gathered) KeywordCompare {
	var in []namedTerms
	for _, sd := range g.sites {
		if sd.kw == nil {
			continue
		}
		in = append(in, namedTerms{name: sd.Domain, terms: sd.kw.Keywords})
	}
	if len(g.sites) > 0 && g.sites[0].kw == nil {
		return compareKeywords(nil)
	}
	return compareKeywords(in)
}

// competitorsOf cleans the list: no empties, no repeats, never the main site.
func competitorsOf(main string, list []string) []string {
	seen := map[string]bool{signals.NormalizeDomain(main): true}
	var out []string
	for _, c := range list {
		c = strings.TrimSpace(c)
		d := signals.NormalizeDomain(c)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, c)
	}
	return out
}

func handleCompare(ctx context.Context, in ops.Input) (*ops.Result, error) {
	main := strings.TrimSpace(in.Str("site"))
	if signals.NormalizeDomain(main) == "" {
		return nil, apperr.New(apperr.InvalidInput, "give a site to compare")
	}
	comps := competitorsOf(main, in.Strings("competitor"))
	if len(comps) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "give at least one competitor that is not %s itself", main)
	}
	kws := dedupe(in.Strings("kw"))
	g, err := gather(ctx, gatherOpts{main: main, competitors: comps, kws: kws, max: in.Int("max", 0), depth: in.Int("depth", 0), noCache: in.NoCache})
	if err != nil {
		return nil, err
	}
	res := &CompareResult{Site: g.sites[0].Domain, Sites: g.sites, Keywords: keywordCompareOf(g), Keyword: kws}
	if len(kws) > 0 {
		res.Positions = positionsLabel
	}
	return &ops.Result{Data: res, Meta: ops.Meta{Source: strings.Join(g.sources, "+")}, Warnings: g.warnings}, nil
}

const positionsLabel = "positions come from SearXNG: not Google unless the label says Google"
