package report

import (
	"fmt"
	"sort"

	"github.com/morethancoder/seox/internal/signals"
	"github.com/morethancoder/seox/internal/site"
)

// Action is one recommended step. Impact is an estimate in audit-score
// points so audit issues, speed and ranking wins can be ordered together.
type Action struct {
	Rank     int     `json:"rank"`
	Kind     string  `json:"kind"` // audit, speed or rankings
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Fix      string  `json:"fix"`
	Affected int     `json:"affected"`
	Impact   float64 `json:"impact"`
}

// almostWin is a query that sits just off page one.
type almostWin struct {
	Query    string
	Position float64
}

var severityOrder = map[string]int{site.High: 0, site.Medium: 1, site.Low: 2}

// sortedIssues orders issues by the points they cost, then severity, then id,
// so the same crawl always gives the same list.
func sortedIssues(in []site.Issue) []site.Issue {
	out := append([]site.Issue(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		if severityOrder[a.Severity] != severityOrder[b.Severity] {
			return severityOrder[a.Severity] < severityOrder[b.Severity]
		}
		return a.ID < b.ID
	})
	return out
}

// speedImpact turns a mobile score into score points: 50 is worth 12.5, about
// a high-severity issue; 90 and above is not an action.
func speedImpact(score int) float64 { return float64(100-score) / 4 }

// almostImpact grows with the number of queries near page one, up to 12.
func almostImpact(n int) float64 {
	v := 4 + float64(n)
	if v > 12 {
		v = 12
	}
	return v
}

// topActions merges the audit's issues, a slow mobile score and near-page-one
// wins into one list ordered by impact and keeps the first n.
func topActions(a *site.AuditResult, sp *signals.SpeedResult, wins []almostWin, n int) []Action {
	var list []Action
	if a != nil {
		for _, is := range sortedIssues(a.Issues) {
			list = append(list, Action{Kind: "audit", ID: is.ID, Title: is.Summary, Fix: is.Fix, Affected: is.Count, Impact: is.Points})
		}
	}
	if sp != nil && sp.Mobile != nil && sp.Mobile.Score != nil && *sp.Mobile.Score < 90 {
		fix := "Work through the PageSpeed opportunities for the mobile home page."
		if ops := sp.Mobile.Opportunities; len(ops) > 0 {
			fix = fmt.Sprintf("Start with %q (about %.0f ms saved on mobile).", ops[0].Title, ops[0].SavingsMs)
		}
		list = append(list, Action{Kind: "speed", ID: "speed-mobile", Title: fmt.Sprintf("Mobile speed score is %d of 100", *sp.Mobile.Score),
			Fix: fix, Affected: 1, Impact: speedImpact(*sp.Mobile.Score)})
	}
	if len(wins) > 0 {
		list = append(list, Action{Kind: "rankings", ID: "almost-page-1", Title: fmt.Sprintf("%d queries are close to page one", len(wins)),
			Fix:      fmt.Sprintf("Improve the pages for %q: better title, headings and internal links can lift it onto page one.", wins[0].Query),
			Affected: len(wins), Impact: almostImpact(len(wins))})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Impact > list[j].Impact })
	if len(list) > n {
		list = list[:n]
	}
	for i := range list {
		list[i].Rank = i + 1
	}
	return list
}

// winsFrom picks the near-page-one queries: Search Console's average position
// 8-20 when real rankings are there, else --kw positions 11-20 from SearXNG.
func winsFrom(g *gathered) []almostWin {
	var out []almostWin
	if m := g.mine; m != nil && m.Data != nil {
		for _, r := range m.Data.Almost {
			out = append(out, almostWin{Query: r.Key, Position: r.Position})
		}
		if len(out) > 0 {
			return out
		}
	}
	for _, kr := range g.sites[0].Positions {
		if kr.BestPosition != nil && *kr.BestPosition > 10 && *kr.BestPosition <= 20 {
			out = append(out, almostWin{Query: kr.Keyword, Position: float64(*kr.BestPosition)})
		}
	}
	return out
}
