package report

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/signals"
)

// ReportResult is the outcome of `seox report`: where the note is and the
// numbers it opens with.
type ReportResult struct {
	Path        string   `json:"path"`
	Site        string   `json:"site"`
	Date        string   `json:"date"`
	Score       int      `json:"score"`
	Pages       int      `json:"pages_crawled"`
	Issues      int      `json:"issues"`
	Actions     []Action `json:"actions"`
	Competitors []string `json:"competitors"`
	Sections    []string `json:"sections_with_data"`
}

func handleReport(ctx context.Context, in ops.Input) (*ops.Result, error) {
	main := strings.TrimSpace(in.Str("site"))
	if signals.NormalizeDomain(main) == "" {
		return nil, apperr.New(apperr.InvalidInput, "give a site to report on")
	}
	cfg, err := dep.loadConfig()
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "cannot read the config")
	}
	o := gatherOpts{main: main, competitors: competitorsOf(main, in.Strings("competitor")), kws: dedupe(in.Strings("kw")),
		max: in.Int("max", 0), depth: in.Int("depth", 0), noCache: in.NoCache, ideas: true, mine: true}
	g, err := gather(ctx, o)
	if err != nil {
		return nil, err
	}
	m := g.sites[0]
	actions := topActions(m.audit, m.spd, winsFrom(g), 5)
	date := dep.now().Format("2006-01-02")
	dir := in.Str("out")
	if dir == "" {
		dir = cfg.ReportDir
	}
	if dir == "" {
		dir = "."
	}
	dir = config.ExpandHome(dir)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	path, err := writeNew(dir, m.Domain, date, renderNote(g, o, date, actions))
	if err != nil {
		return nil, err
	}
	res := &ReportResult{Path: path, Site: m.Domain, Date: date, Score: m.audit.Score, Pages: m.Pages,
		Issues: len(m.audit.Issues), Actions: actions, Competitors: []string{}}
	for _, c := range g.sites[1:] {
		res.Competitors = append(res.Competitors, c.Domain)
	}
	res.Sections = []string{"audit", "keywords"}
	if m.spd != nil {
		res.Sections = append(res.Sections, "speed")
	}
	if m.Authority.NoData == "" {
		res.Sections = append(res.Sections, "authority")
	}
	if len(m.Positions) > 0 {
		res.Sections = append(res.Sections, "positions")
	}
	if g.mine != nil && g.mine.Data != nil {
		res.Sections = append(res.Sections, "real rankings")
	}
	return &ops.Result{Data: res, Meta: ops.Meta{Source: strings.Join(g.sources, "+")}, Warnings: g.warnings}, nil
}
