package report

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/term"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/mine"
	"github.com/morethancoder/seox/internal/rank"
	"github.com/morethancoder/seox/internal/signals"
	"github.com/morethancoder/seox/internal/site"
)

// deps are the calls into the other areas. They are variables so tests can put
// fakes behind them without touching the network.
type deps struct {
	crawl      func(ctx context.Context, s string, opt site.CrawlOptions) (*site.CrawlResult, error)
	speed      func(ctx context.Context, url string, opt signals.SpeedOptions) (*signals.SpeedResult, error)
	authority  func(ctx context.Context, domains []string, opt signals.AuthorityOptions) (*signals.AuthorityResult, error)
	ideas      func(ctx context.Context, kw string, opt signals.IdeasOptions) (*signals.IdeasResult, error)
	openRank   func(ctx context.Context, cfg *config.Config) (*rank.Instance, error)
	positions  func(ctx context.Context, s string, kws []string, opt rank.Options) (*rank.Result, error)
	mine       func(ctx context.Context, s string, opt mine.Options) (*mine.SummaryData, error)
	loadConfig func() (*config.Config, error)
	now        func() time.Time
	// stderr is where progress goes; nil unless stderr is a terminal.
	stderr io.Writer
}

func realDeps() *deps {
	d := &deps{
		crawl:      site.Crawl,
		speed:      signals.Speed,
		authority:  signals.Authority,
		ideas:      signals.Ideas,
		openRank:   rank.Open,
		positions:  rank.Positions,
		mine:       mine.SummaryWith,
		loadConfig: config.Load,
		now:        time.Now,
	}
	if term.IsTerminal(int(os.Stderr.Fd())) {
		d.stderr = os.Stderr
	}
	return d
}

var dep = realDeps()

func (d *deps) say(format string, a ...any) {
	if d.stderr != nil {
		fmt.Fprintf(d.stderr, format+"\n", a...)
	}
}

// siteData is everything gathered about one site. Fields tagged json:"-" are
// kept for the note; the rest is what compare returns.
type siteData struct {
	Site      string               `json:"site"`
	Domain    string               `json:"domain"`
	Crawled   bool                 `json:"crawled"`
	Pages     int                  `json:"pages_crawled"`
	Score     *int                 `json:"audit_score"`
	TopIssues []IssueBrief         `json:"top_issues"`
	Speed     SpeedSummary         `json:"speed"`
	Authority AuthSummary          `json:"authority"`
	Positions []rank.KeywordResult `json:"positions,omitempty"`
	Note      string               `json:"note,omitempty"`

	audit *site.AuditResult
	kw    *site.KeywordsResult
	spd   *signals.SpeedResult

	crawlErr error
	warns    []string // crawl and speed warnings, in order
}

// IssueBrief is one audit issue in short form.
type IssueBrief struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Affected int    `json:"affected"`
}

// SpeedSummary is the speed numbers, or "no data" with the reason.
type SpeedSummary struct {
	MobileScore  *int     `json:"mobile_score"`
	DesktopScore *int     `json:"desktop_score"`
	LCPms        *float64 `json:"lcp_ms"`
	CLS          *float64 `json:"cls"`
	Source       string   `json:"source,omitempty"`
	NoData       string   `json:"no_data,omitempty"`
}

// AuthSummary is the authority numbers, or "no data" with the reason.
type AuthSummary struct {
	OPRScore        *float64 `json:"opr_score"`
	FirstSeen       string   `json:"first_seen,omitempty"`
	SnapshotsApprox *int     `json:"snapshots_approx"`
	NoData          string   `json:"no_data,omitempty"`
}

// ideasFor is the autocomplete ideas for one target keyword.
type ideasFor struct {
	Keyword string
	Res     *signals.IdeasResult
	Err     error
}

// minePart is the real-rankings piece of a report.
type minePart struct {
	Data    *mine.SummaryData
	Skipped string // how to enable it, when it was skipped
	Err     error
}

// gathered is the outcome of one run.
type gathered struct {
	sites    []*siteData // [0] is the main site
	ideas    []ideasFor
	mine     *minePart
	warnings []string
	sources  []string
	authNote string
	srxNote  string // note when positions could not be fetched at all
}

// gatherOpts says what to collect.
type gatherOpts struct {
	main        string
	competitors []string
	kws         []string
	max, depth  int
	noCache     bool
	ideas, mine bool
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range list {
		if w != "" && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// gather runs every piece at the same time. Crawls of different sites go in
// parallel (the web limiter keeps each host polite); speed and authority run
// alongside them; ideas start as soon as the main crawl is done.
func gather(parent context.Context, o gatherOpts) (*gathered, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	names := append([]string{o.main}, o.competitors...)
	g := &gathered{}
	for _, n := range names {
		g.sites = append(g.sites, &siteData{Site: n, Domain: signals.NormalizeDomain(n)})
	}
	main := g.sites[0]
	var wg sync.WaitGroup

	// Crawls, one per site.
	for i, sd := range g.sites {
		wg.Add(1)
		go func(i int, sd *siteData) {
			defer wg.Done()
			dep.say("crawling %s", sd.Domain)
			cr, err := dep.crawl(ctx, sd.Site, site.CrawlOptions{Max: o.max, Depth: o.depth, NoCache: o.noCache})
			if err != nil {
				sd.crawlErr = err
				if i == 0 {
					cancel() // nothing useful can be said without the main site
				}
				return
			}
			sd.Crawled = true
			sd.Pages = len(cr.Pages)
			sd.audit = site.AuditCrawl(cr)
			sd.kw = site.KeywordsFromCrawl(cr, 100)
			for _, w := range cr.Warnings {
				sd.warns = append(sd.warns, sd.Domain+": "+w)
			}
			if i == 0 && o.ideas {
				g.ideas = runIdeas(ctx, sd.kw, sd.Domain, o.noCache, &wg)
			}
		}(i, sd)
	}

	// Speed, one call per site.
	for _, sd := range g.sites {
		wg.Add(1)
		go func(sd *siteData) {
			defer wg.Done()
			dep.say("speed %s", sd.Domain)
			res, err := dep.speed(ctx, "https://"+sd.Domain+"/", signals.SpeedOptions{NoCache: o.noCache})
			if err != nil {
				sd.Speed.NoData = err.Error()
				sd.warns = append(sd.warns, "speed "+sd.Domain+": "+err.Error())
				return
			}
			sd.spd = res
			for _, w := range res.Warnings {
				if !strings.HasPrefix(w, "No google key") {
					w = "speed " + sd.Domain + ": " + w
				}
				sd.warns = append(sd.warns, w)
			}
		}(sd)
	}

	// Authority: one batched call for every domain.
	var auth *signals.AuthorityResult
	var authErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		var domains []string
		for _, sd := range g.sites {
			domains = append(domains, sd.Domain)
		}
		dep.say("authority for %d domains", len(domains))
		auth, authErr = dep.authority(ctx, domains, signals.AuthorityOptions{NoCache: o.noCache})
	}()

	// Positions: one SearXNG for all sites.
	var posRes []*rank.Result
	var posErrs []error
	var posFatal error
	if len(o.kws) > 0 {
		posRes = make([]*rank.Result, len(g.sites))
		posErrs = make([]error, len(g.sites))
		wg.Add(1)
		go func() {
			defer wg.Done()
			posFatal = runPositions(ctx, g.sites, o, posRes, posErrs)
		}()
	}

	// Real rankings of the main site.
	if o.mine {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.mine = runMine(ctx, o.main, o.noCache)
		}()
	}

	wg.Wait()

	if main.crawlErr != nil {
		return nil, main.crawlErr
	}
	if err := parent.Err(); err != nil {
		return nil, apperr.From(err)
	}
	g.assemble(o, auth, authErr, posRes, posErrs, posFatal)
	return g, nil
}

// brandOf is the domain name without its TLD ("powertelebot" for
// powertelebot.com), used to keep the site's own name out of the ideas terms.
func brandOf(domain string) string {
	d := strings.TrimPrefix(strings.ToLower(domain), "www.")
	if i := strings.Index(d, "."); i > 0 {
		d = d[:i]
	}
	return d
}

// pickIdeasTerms chooses up to 3 terms for ideas, in score order: phrases of
// 2-3 words first, single words only to fill the gap. A generic single word
// gives off-topic autocomplete, and the brand name only finds the brand.
func pickIdeasTerms(terms []site.SiteKeyword, brand string) []string {
	var phrases, singles []string
	for _, k := range terms {
		t := strings.TrimSpace(k.Term)
		if t == "" || (brand != "" && strings.ReplaceAll(strings.ToLower(t), " ", "") == brand) {
			continue
		}
		if strings.IndexFunc(t, unicode.IsDigit) >= 0 {
			continue // dates and counts ("16 октября 2026") make poor ideas seeds
		}
		switch n := len(strings.Fields(t)); {
		case n >= 2 && n <= 3:
			phrases = append(phrases, t)
		case n == 1:
			singles = append(singles, t)
		}
	}
	out := append(phrases, singles...)
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// ideasLang is the suggest language for a term's script: Arabic or Cyrillic
// letters pick "ar" or "ru"; anything else is "" so Ideas keeps its default.
func ideasLang(term string) string {
	for _, r := range term {
		switch {
		case unicode.Is(unicode.Arabic, r):
			return "ar"
		case unicode.Is(unicode.Cyrillic, r):
			return "ru"
		}
	}
	return ""
}

// runIdeas asks for ideas on the top 3 target keywords (see pickIdeasTerms).
// wg is the caller's group: the counter is above zero while the caller runs,
// so Add is safe.
func runIdeas(ctx context.Context, kw *site.KeywordsResult, domain string, noCache bool, wg *sync.WaitGroup) []ideasFor {
	terms := pickIdeasTerms(kw.Keywords, brandOf(domain))
	out := make([]ideasFor, len(terms))
	for i, t := range terms {
		out[i].Keyword = t
		wg.Add(1)
		go func(i int, t string) {
			defer wg.Done()
			out[i].Res, out[i].Err = dep.ideas(ctx, t, signals.IdeasOptions{Lang: ideasLang(t), NoCache: noCache, Limit: 10})
		}(i, t)
	}
	return out
}

func runMine(ctx context.Context, s string, noCache bool) *minePart {
	dep.say("real rankings (Search Console, Bing)")
	d, err := dep.mine(ctx, s, mine.Options{Days: 28, Limit: 10, NoCache: noCache})
	switch {
	case err == nil:
		return &minePart{Data: d}
	case errors.Is(err, mine.ErrNotSignedIn):
		return &minePart{Skipped: "not signed in. To enable: `seox login --client client_secret.json` for Search Console, or `seox key set bing`"}
	case errors.Is(err, mine.ErrNotVerified):
		return &minePart{Skipped: "this site is not verified in Search Console or Bing. To enable: `seox setup verify " + signals.NormalizeDomain(s) + "`"}
	}
	return &minePart{Err: err}
}

// searxNeeded says whether any (site, keyword) pair misses the rank cache, so
// SearXNG is only started when a search is really needed. rank.Missing owns
// the cache key.
func searxNeeded(sites []*siteData, o gatherOpts) bool {
	for _, sd := range sites {
		if rank.Missing(sd.Domain, o.kws, rank.Options{NoCache: o.noCache}) {
			return true
		}
	}
	return false
}

// runPositions opens SearXNG once (only on a cache miss), asks for each site
// through that one instance and stops it on return. A fatal error (no
// SearXNG) is returned; a failure for one site lands in errs.
func runPositions(ctx context.Context, sites []*siteData, o gatherOpts, res []*rank.Result, errs []error) error {
	opt := rank.Options{NoCache: o.noCache}
	if searxNeeded(sites, o) {
		cfg, err := dep.loadConfig()
		if err != nil {
			return err
		}
		dep.say("starting search for positions")
		inst, err := dep.openRank(ctx, cfg)
		if err != nil {
			return err
		}
		defer inst.Stop()
		opt.Instance = inst
		opt.Config = cfg
	}
	for i, sd := range sites {
		if ctx.Err() != nil {
			return nil
		}
		dep.say("positions for %s", sd.Domain)
		res[i], errs[i] = dep.positions(ctx, sd.Site, o.kws, opt)
	}
	return nil
}

// assemble folds the pieces into the per-site summaries and the warnings, in a
// fixed order so output is stable.
func (g *gathered) assemble(o gatherOpts, auth *signals.AuthorityResult, authErr error, posRes []*rank.Result, posErrs []error, posFatal error) {
	var warns []string
	src := map[string]bool{}
	for _, sd := range g.sites {
		if sd.crawlErr != nil {
			sd.Note = "could not be crawled: " + sd.crawlErr.Error()
			warns = append(warns, "crawl "+sd.Domain+": "+sd.crawlErr.Error())
			sd.TopIssues = []IssueBrief{}
		} else {
			src["crawl"] = true
			sc := sd.audit.Score
			sd.Score = &sc
			sd.TopIssues = topIssues(sd.audit, 3)
		}
		warns = append(warns, sd.warns...)
		if sp := sd.spd; sp != nil {
			sd.Speed = summariseSpeed(sp)
			src["pagespeed"] = true
			if sp.Crux != nil {
				src["crux"] = true
			}
		}
	}

	// Authority.
	byDomain := map[string]signals.DomainAuthority{}
	if authErr != nil {
		warns = append(warns, "authority: "+authErr.Error())
	} else if auth != nil {
		for _, d := range auth.Domains {
			byDomain[d.Domain] = d
		}
		warns = append(warns, auth.Warnings...)
		g.authNote = auth.Note
	}
	for _, sd := range g.sites {
		d, ok := byDomain[sd.Domain]
		if !ok {
			sd.Authority.NoData = "no authority data"
			if authErr != nil {
				sd.Authority.NoData = authErr.Error()
			}
			continue
		}
		sd.Authority = AuthSummary{OPRScore: d.Score, FirstSeen: d.FirstSeen, SnapshotsApprox: d.SnapshotsApprox}
		if d.Score == nil {
			sd.Authority.NoData = "no Open PageRank score (needs a key: seox key set openpagerank, or the domain is not indexed)"
		} else {
			src["openpagerank"] = true
		}
		if d.FirstSeen != "" {
			src["wayback"] = true
		}
	}

	// Positions.
	if len(o.kws) > 0 {
		switch {
		case posFatal != nil:
			g.srxNote = posFatal.Error()
			warns = append(warns, "positions skipped: "+posFatal.Error())
		default:
			var rw []string
			for i, sd := range g.sites {
				if posErrs[i] != nil {
					warns = append(warns, "positions "+sd.Domain+": "+posErrs[i].Error())
					continue
				}
				if posRes[i] != nil {
					sd.Positions = posRes[i].Keywords
					rw = append(rw, posRes[i].Warnings...)
					src["searxng"] = true
				}
			}
			warns = append(warns, dedupe(rw)...)
		}
	}

	// Ideas and mine (report only).
	for _, id := range g.ideas {
		if id.Err != nil {
			warns = append(warns, "ideas for \""+id.Keyword+"\": "+id.Err.Error())
			continue
		}
		src["autocomplete"] = true
		warns = append(warns, id.Res.Warnings...)
	}
	if m := g.mine; m != nil {
		switch {
		case m.Skipped != "":
			warns = append(warns, "real rankings skipped: "+m.Skipped)
		case m.Err != nil:
			warns = append(warns, "real rankings: "+m.Err.Error())
		default:
			for _, s := range strings.Split(m.Data.Source, "+") {
				src[s] = true
			}
			warns = append(warns, m.Data.Warnings...)
		}
	}
	g.warnings = dedupe(warns)
	for _, s := range []string{"crawl", "pagespeed", "crux", "openpagerank", "wayback", "searxng", "autocomplete", "gsc", "bing"} {
		if src[s] {
			g.sources = append(g.sources, s)
		}
	}
}

func topIssues(a *site.AuditResult, n int) []IssueBrief {
	out := []IssueBrief{}
	for _, is := range sortedIssues(a.Issues) {
		if len(out) == n {
			break
		}
		out = append(out, IssueBrief{ID: is.ID, Severity: is.Severity, Summary: is.Summary, Affected: is.Count})
	}
	return out
}

// speedSource names where the lab numbers came from: Google's PageSpeed servers, Lighthouse run on
// this machine (no google key, or PSI failed), or both when the strategies differ.
func speedSource(sp *signals.SpeedResult) string {
	psi, local := false, false
	for _, r := range []*signals.PSIResult{sp.Mobile, sp.Desktop} {
		if r == nil {
			continue
		}
		if r.Source == "lighthouse-local" {
			local = true
		} else {
			psi = true
		}
	}
	switch {
	case psi && local:
		return "PageSpeed Insights (Google's servers) for one strategy, local Lighthouse for the other"
	case local:
		return "local Lighthouse on this machine, no real-user data"
	default:
		return "PageSpeed Insights lab run on Google's servers"
	}
}

func summariseSpeed(sp *signals.SpeedResult) SpeedSummary {
	s := SpeedSummary{Source: speedSource(sp) + ", home page"}
	if sp.Mobile != nil {
		s.MobileScore = sp.Mobile.Score
	}
	if sp.Desktop != nil {
		s.DesktopScore = sp.Desktop.Score
	}
	for _, r := range []*signals.PSIResult{sp.Mobile, sp.Desktop} {
		if r == nil {
			continue
		}
		if s.LCPms == nil {
			s.LCPms = r.Lab.LCPms
		}
		if s.CLS == nil {
			s.CLS = r.Lab.CLS
		}
	}
	if sp.Crux != nil {
		s.Source += " plus CrUX real-user data"
	}
	if s.MobileScore == nil && s.DesktopScore == nil {
		s.NoData = "Lighthouse returned no score"
	}
	return s
}
