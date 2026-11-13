package report

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/morethancoder/seox/internal/rank"
	"github.com/morethancoder/seox/internal/signals"
)

const (
	maxURLsShown   = 5
	maxIdeasShown  = 8
	maxKeywordRows = 20
)

// cell makes untrusted text (page titles, URLs) safe inside a table cell.
func cell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	// A backslash keeps text such as <h1> from being read as an HTML tag.
	s = strings.ReplaceAll(s, "<", "\\<")
	if s == "" {
		return "-"
	}
	return s
}

// yamlStr quotes a string for the frontmatter. A JSON string is valid YAML.
func yamlStr(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strconv.Quote(s)
}

type table struct {
	b    strings.Builder
	cols int
}

func newTable(head ...string) *table {
	t := &table{cols: len(head)}
	t.b.WriteString("| " + strings.Join(head, " | ") + " |\n|" + strings.Repeat(" --- |", len(head)) + "\n")
	return t
}

func (t *table) row(cells ...string) {
	for len(cells) < t.cols {
		cells = append(cells, "-")
	}
	for i := range cells {
		cells[i] = cell(cells[i])
	}
	t.b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
}

func (t *table) String() string { return t.b.String() + "\n" }

func ptrInt(p *int) string {
	if p == nil {
		return "no data"
	}
	return strconv.Itoa(*p)
}

func ptrF(p *float64, f string) string {
	if p == nil {
		return "no data"
	}
	return fmt.Sprintf(f, *p)
}

func lcpText(p *float64) string {
	if p == nil {
		return "no data"
	}
	return fmt.Sprintf("%.2f s", *p/1000)
}

func speedCell(s SpeedSummary, pick func(SpeedSummary) string) string {
	if s.NoData != "" {
		return "no data"
	}
	return pick(s)
}

// compareTable is the side-by-side table used by the report.
func compareTable(sites []*siteData) string {
	head := []string{"", "your site"}
	for _, sd := range sites[1:] {
		head = append(head, sd.Domain)
	}
	head[0] = "metric"
	t := newTable(head...)
	add := func(label string, f func(*siteData) string) {
		row := []string{label}
		for _, sd := range sites {
			row = append(row, f(sd))
		}
		t.row(row...)
	}
	add("site", func(sd *siteData) string { return sd.Domain })
	add("audit score", func(sd *siteData) string {
		if sd.Score == nil {
			return "no data (not crawled)"
		}
		return strconv.Itoa(*sd.Score) + " / 100"
	})
	add("pages crawled", func(sd *siteData) string { return strconv.Itoa(sd.Pages) })
	add("top issues", func(sd *siteData) string {
		var s []string
		for _, is := range sd.TopIssues {
			s = append(s, fmt.Sprintf("%s (%d)", is.Summary, is.Affected))
		}
		if len(s) == 0 {
			return "none"
		}
		return strings.Join(s, "; ")
	})
	add("mobile speed", func(sd *siteData) string {
		return speedCell(sd.Speed, func(s SpeedSummary) string { return ptrInt(s.MobileScore) })
	})
	add("desktop speed", func(sd *siteData) string {
		return speedCell(sd.Speed, func(s SpeedSummary) string { return ptrInt(s.DesktopScore) })
	})
	add("LCP (lab)", func(sd *siteData) string {
		return speedCell(sd.Speed, func(s SpeedSummary) string { return lcpText(s.LCPms) })
	})
	add("CLS (lab)", func(sd *siteData) string {
		return speedCell(sd.Speed, func(s SpeedSummary) string { return ptrF(s.CLS, "%.3f") })
	})
	add("authority (OPR 0-10)", func(sd *siteData) string {
		if sd.Authority.OPRScore == nil {
			return "no data"
		}
		return fmt.Sprintf("%.1f", *sd.Authority.OPRScore)
	})
	add("first seen (Wayback)", func(sd *siteData) string { return sd.Authority.FirstSeen })
	add("snapshots (approx.)", func(sd *siteData) string {
		if sd.Authority.SnapshotsApprox == nil {
			return "no data"
		}
		return "about " + strconv.Itoa(*sd.Authority.SnapshotsApprox)
	})
	return t.String()
}

func positionsTable(sites []*siteData, kws []string) string {
	head := []string{"keyword"}
	for _, sd := range sites {
		head = append(head, sd.Domain)
	}
	t := newTable(head...)
	for _, kw := range kws {
		row := []string{kw}
		for _, sd := range sites {
			row = append(row, positionText(sd.Positions, kw))
		}
		t.row(row...)
	}
	return t.String()
}

func positionText(ps []rank.KeywordResult, kw string) string {
	for _, p := range ps {
		if p.Keyword == kw {
			if p.BestPosition == nil {
				if p.Note != "" {
					return p.Note
				}
				return "not found"
			}
			s := "#" + strconv.Itoa(*p.BestPosition)
			if !p.Google {
				s += " (not Google)"
			}
			return s
		}
	}
	return "no data"
}

func scoreText(sd *siteData) string {
	if sd.Score == nil {
		return "no data"
	}
	return strconv.Itoa(*sd.Score)
}

// renderNote builds the markdown note.
func renderNote(g *gathered, o gatherOpts, date string, actions []Action) string {
	main := g.sites[0]
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	// Frontmatter.
	w("---\ntype: seo-report\nsite: %s\ndate: %s\ncompetitors:", yamlStr(main.Domain), date)
	if len(g.sites) == 1 {
		w(" []\n")
	} else {
		w("\n")
		for _, c := range g.sites[1:] {
			w("  - %s\n", yamlStr(c.Domain))
		}
	}
	w("score: %s\n---\n\n", scoreText(main))

	w("# SEO report: %s\n\n", cell(main.Domain))
	a := main.audit
	w("## Summary\n\n")
	w("%s was crawled on %s: %d pages", main.Domain, date, main.Pages)
	if a.Limited {
		w(" (stopped at the page limit; more pages exist)")
	}
	w(". Audit score **%d / 100** with %d high, %d medium and %d low issues.", a.Score, a.Counts.High, a.Counts.Medium, a.Counts.Low)
	if s := main.Speed; s.NoData == "" && s.MobileScore != nil {
		w(" Mobile speed %d / 100.", *s.MobileScore)
	}
	if s := main.Authority; s.OPRScore != nil {
		w(" Authority (Open PageRank) %.1f / 10.", *s.OPRScore)
	}
	w("\n\n")
	if len(g.warnings) > 0 {
		w("Some data is missing or partial:\n\n")
		for _, x := range g.warnings {
			w("- %s\n", cell(x))
		}
		w("\n")
	}

	// Actions.
	w("## Top 5 actions\n\n")
	if len(actions) == 0 {
		w("Nothing to fix: the audit found no issues.\n\n")
	} else {
		w("Ordered by estimated impact in score points (audit issues use their score cost; speed and near-page-one wins use a comparable estimate).\n\n")
		for _, ac := range actions {
			w("%d. **%s** (%s, affects %d)\n   - Fix: %s\n", ac.Rank, cell(ac.Title), ac.Kind, ac.Affected, cell(ac.Fix))
		}
		w("\n")
	}

	// Audit.
	w("## Audit\n\n")
	w("Score %d / 100. Formula: %s.\n\n", a.Score, a.ScoreFormula)
	if len(a.Issues) == 0 {
		w("No issues found.\n\n")
	}
	for _, is := range sortedIssues(a.Issues) {
		w("### %s (%s, %d affected)\n\n", cell(is.Summary), is.Severity, is.Count)
		w("- Fix: %s\n", cell(is.Fix))
		urls := is.URLs
		more := is.Count - len(urls)
		if len(urls) > maxURLsShown {
			more += len(urls) - maxURLsShown
			urls = urls[:maxURLsShown]
		}
		for _, u := range urls {
			w("- %s\n", cell(u))
		}
		if len(urls) == 0 {
			for i, e := range is.Examples {
				if i == maxURLsShown {
					break
				}
				w("- %s\n", cell(e))
			}
		}
		if more > 0 && len(urls) > 0 {
			w("- and %d more\n", more)
		}
		w("\n")
	}

	// Speed.
	w("## Speed\n\n")
	if sp := main.spd; sp == nil {
		w("No data: %s\n\n", cell(main.Speed.NoData))
	} else {
		t := newTable("strategy", "score", "LCP (lab)", "CLS (lab)", "interaction")
		for _, r := range []*signals.PSIResult{sp.Mobile, sp.Desktop} {
			if r == nil {
				continue
			}
			inter := "no data"
			if r.Interaction != nil {
				inter = fmt.Sprintf("%s %.0f ms (%s)", r.Interaction.Metric, r.Interaction.Value, r.Interaction.Source)
			}
			t.row(r.Strategy, ptrInt(r.Score), lcpText(r.Lab.LCPms), ptrF(r.Lab.CLS, "%.3f"), inter)
		}
		w("%s", t.String())
		w("Source: %s. Scores and LCP/CLS here are lab numbers for the home page, not what your visitors see.", main.Speed.Source)
		if sp.Crux == nil {
			w(" Real-user CrUX data was not used (it needs a Google key: `seox key set google`).")
		} else {
			w(" CrUX real-user data (%s, 75th percentile):", sp.Crux.Scope)
			for name, m := range sp.Crux.Metrics {
				w(" %s %.4g %s (%s);", name, m.P75, m.Unit, m.Rating)
			}
		}
		w("\n\n")
		if r := sp.Mobile; r != nil && len(r.Opportunities) > 0 {
			w("Mobile opportunities:\n\n")
			for i, op := range r.Opportunities {
				if i == 5 {
					break
				}
				w("- %s (about %.0f ms)\n", cell(op.Title), op.SavingsMs)
			}
			w("\n")
		}
	}

	// Authority.
	w("## Authority\n\n")
	au := main.Authority
	t := newTable("measure", "value")
	t.row("Open PageRank (0-10)", ptrF(au.OPRScore, "%.1f"))
	t.row("first seen (Wayback Machine)", au.FirstSeen)
	if au.SnapshotsApprox != nil {
		t.row("snapshots (approximate)", "about "+strconv.Itoa(*au.SnapshotsApprox))
	} else {
		t.row("snapshots (approximate)", "no data")
	}
	w("%s", t.String())
	if au.NoData != "" {
		w("No data for the score: %s\n\n", cell(au.NoData))
	}
	w("Open PageRank is a rough, free authority estimate and not the same as Moz or Ahrefs ratings. Snapshot counts are approximate.\n\n")

	// Keywords.
	w("## Keywords\n\n")
	w("Terms the site targets, from the crawl. %s\n\n", main.kw.Weights)
	t = newTable("term", "score", "pages")
	for i, k := range main.kw.Keywords {
		if i == maxKeywordRows {
			break
		}
		t.row(k.Term, fmt.Sprintf("%.1f", k.Score), strconv.Itoa(k.PageCount))
	}
	w("%s", t.String())

	// Ideas.
	w("## Ideas for the top 3 target keywords\n\n")
	if len(g.ideas) == 0 {
		w("No target keywords were found to expand.\n\n")
	}
	w("Ideas are search-box autocomplete suggestions: no search volumes, so they show what people type, not how often.\n\n")
	for _, id := range g.ideas {
		w("### %s\n\n", cell(id.Keyword))
		if id.Err != nil {
			w("No data: %s\n\n", cell(id.Err.Error()))
			continue
		}
		for _, grp := range []struct {
			name string
			list []signals.Idea
		}{{"Questions", id.Res.Questions}, {"Comparisons", id.Res.Comparisons}, {"Modifiers", id.Res.Modifiers}, {"Related", id.Res.Related}} {
			if len(grp.list) == 0 {
				continue
			}
			var s []string
			for i, x := range grp.list {
				if i == maxIdeasShown {
					break
				}
				s = append(s, cell(x.Text))
			}
			w("- %s: %s\n", grp.name, strings.Join(s, ", "))
		}
		w("\n")
	}

	// Positions.
	if len(o.kws) > 0 {
		w("## Positions for your keywords\n\n")
		if main.Positions == nil && g.srxNote != "" {
			w("No data: %s\n\n", cell(g.srxNote))
		} else {
			w("%s", positionsTable(g.sites, o.kws))
			w("Positions come from SearXNG, a meta search engine: **not Google** unless marked Google. Only the first %d result pages are checked, so \"not found\" means not in that range. Treat them as a guide.\n\n", rank.DefaultDepth)
		}
	}

	// Real rankings.
	w("## Real rankings (Search Console and Bing)\n\n")
	m := g.mine
	switch {
	case m == nil || m.Skipped != "":
		reason := "not requested"
		if m != nil {
			reason = m.Skipped
		}
		w("Skipped: %s\n\n", cell(reason))
	case m.Err != nil:
		w("No data: %s\n\n", cell(m.Err.Error()))
	default:
		d := m.Data
		w("Source: %s, last 28 days.\n\n", d.Source)
		if len(d.Queries) > 0 {
			w("Top queries:\n\n")
			t := newTable("query", "clicks", "impressions", "CTR", "position")
			for _, r := range d.Queries {
				t.row(r.Key, fmt.Sprintf("%.0f", r.Clicks), fmt.Sprintf("%.0f", r.Impressions), fmt.Sprintf("%.1f%%", r.CTR*100), fmt.Sprintf("%.1f", r.Position))
			}
			w("%s", t.String())
		}
		if len(d.Almost) > 0 {
			w("Close to page one (average position 8-20):\n\n")
			t := newTable("query", "impressions", "position")
			for _, r := range d.Almost {
				t.row(r.Key, fmt.Sprintf("%.0f", r.Impressions), fmt.Sprintf("%.1f", r.Position))
			}
			w("%s", t.String())
		}
		if d.Drops != nil && (len(d.Drops.Queries) > 0 || len(d.Drops.Pages) > 0) {
			w("Losing ground against the previous period: %d queries and %d pages.\n\n", len(d.Drops.Queries), len(d.Drops.Pages))
		}
		if d.Bing != nil && d.Bing.Links != nil {
			w("Bing counts %.0f inbound links to the site.\n\n", d.Bing.Links.TotalLinks)
		}
	}

	// Competitors.
	if len(g.sites) > 1 {
		w("## Competitors\n\n")
		w("%s", compareTable(g.sites))
		kc := keywordCompareOf(g)
		for _, sk := range kc.Sites {
			w("- %s: shared %s; unique %s\n", cell(sk.Site), listText(sk.Shared), listText(sk.Unique))
		}
		w("\n")
		if len(kc.Gaps) > 0 {
			w("Keyword gaps (terms competitors target and you do not):\n\n")
			t := newTable("term", "competitors")
			for _, gp := range kc.Gaps {
				t.row(gp.Term, strings.Join(gp.Sites, ", "))
			}
			w("%s", t.String())
		}
		for _, sd := range g.sites[1:] {
			if sd.Note != "" {
				w("- %s %s\n", cell(sd.Domain), cell(sd.Note))
			}
		}
	}
	return b.String()
}

func listText(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return cell(strings.Join(s, ", "))
}
