package mine

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
)

func init() {
	ops.Handle("login", login)
	ops.Handle("logout", logout)
	for _, k := range []string{"queries", "pages", "drops", "almost"} {
		ops.Handle("mine."+k, handler(k))
	}
	ops.Handle("setup.verify", func(ctx context.Context, in ops.Input) (*ops.Result, error) {
		v, err := Verify(ctx, in.Str("site"))
		if err != nil {
			return nil, err
		}
		return &ops.Result{Data: v, Meta: ops.Meta{Source: "local"}, Warnings: v.Warnings}, nil
	})
}

// Options tunes a mine call. Zero values mean the defaults.
type Options struct {
	Days  int // length of the window; default 28
	Limit int // rows per list; default 20
	// MinDropImpressions: a key needs this many impressions in the previous
	// window to count as a drop (default 10).
	MinDropImpressions float64
	// MinAlmostImpressions: queries need this many impressions to count as
	// almost on page one (default 50).
	MinAlmostImpressions float64
	NoCache              bool
}

func (o Options) norm() (Options, error) {
	if o.Days == 0 {
		o.Days = 28
	}
	if o.Limit == 0 {
		o.Limit = 20
	}
	if o.Days < 1 || o.Days > 240 {
		return o, apperr.New(apperr.InvalidInput, "--days must be between 1 and 240 (the previous window of the same length must fit in Search Console's 16 months)")
	}
	if o.Limit < 1 || o.Limit > 25000 {
		return o, apperr.New(apperr.InvalidInput, "--limit must be between 1 and 25000")
	}
	if o.MinDropImpressions == 0 {
		o.MinDropImpressions = DefaultMinDropImpressions
	}
	if o.MinAlmostImpressions == 0 {
		o.MinAlmostImpressions = DefaultMinAlmostImpr
	}
	return o, nil
}

// Report is the answer of one mine command.
type Report struct {
	// Data holds "site", "period", the Search Console lists and a "bing" section.
	Data         map[string]any `json:"data"`
	Source       string         `json:"source"` // "gsc", "bing" or "gsc+bing"
	SourceReason string         `json:"source_reason,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
	Cached       bool           `json:"cached,omitempty"`
}

// outcome is what gather collected from the two sources.
type outcome[G, B any] struct {
	gsc          G
	bing         B
	hasG, hasB   bool
	source, why  string
	warnings     []string
	property     string
	bingSiteName string
}

// gather runs the Search Console part and the Bing part. Each works alone. A
// Bing failure is only a warning when Search Console worked; a Search Console
// failure is only a warning when Bing worked.
func gather[G, B any](ctx context.Context, site string, days int,
	gfn func(context.Context, *gscSource) (G, error),
	bfn func(context.Context, *bingSource, *[]string) (B, error)) (*outcome[G, B], error) {

	o := &outcome[G, B]{}
	var gErr, bErr error
	if signedIn() {
		g, err := newGSC(ctx, site, days)
		if err == nil {
			o.property = g.prop
			o.gsc, err = gfn(ctx, g)
		}
		gErr = err
	} else {
		gErr = notSignedIn(notSignedInMessage())
	}
	o.hasG = gErr == nil
	if ctx.Err() != nil {
		return nil, apperr.From(ctx.Err())
	}

	key, _ := config.Key("bing")
	hasKey := key != ""
	if hasKey {
		b, err := newBing(ctx, site, days)
		if err == nil {
			o.bingSiteName = b.site
			o.bing, err = bfn(ctx, b, &o.warnings)
		}
		bErr = err
		o.hasB = err == nil
		if ctx.Err() != nil {
			return nil, apperr.From(ctx.Err())
		}
	}

	switch {
	case o.hasG && o.hasB:
		o.source = "gsc+bing"
	case o.hasG:
		o.source = "gsc"
		if hasKey {
			o.warnings = append(o.warnings, "Bing Webmaster failed, so only Search Console data is shown: "+apperr.From(bErr).Message)
		} else {
			o.why = "no Bing key (seox key set bing adds Bing data)"
		}
	case o.hasB:
		o.source = "bing"
		if errors.Is(gErr, ErrNotSignedIn) {
			o.why = "not signed in to Search Console (seox login adds Search Console data)"
		} else {
			o.warnings = append(o.warnings, "Search Console failed, so only Bing data is shown: "+apperr.From(gErr).Message)
		}
	default:
		if !hasKey {
			return nil, gErr
		}
		if errors.Is(gErr, ErrNotSignedIn) {
			return nil, bErr
		}
		if errors.Is(gErr, ErrNotVerified) && errors.Is(bErr, ErrNotVerified) {
			return nil, notVerified("%s; %s", apperr.From(gErr).Message, apperr.From(bErr).Message)
		}
		return nil, gErr
	}
	return o, nil
}

func periodMap(per period) map[string]any {
	return map[string]any{"start": per.Start, "end": per.End, "days": per.Days,
		"previous_start": per.PrevStart, "previous_end": per.PrevEnd, "data_state": "final"}
}

// bingGranularity is said in the data so nobody reads Bing's weekly rows as daily.
const bingGranularity = "weekly"

// Mine runs one of "queries", "pages", "drops" or "almost" for a site.
func Mine(ctx context.Context, kind, site string, opt Options) (*Report, error) {
	opt, err := opt.norm()
	if err != nil {
		return nil, err
	}
	host := siteHost(site)
	if host == "" {
		return nil, apperr.New(apperr.InvalidInput, "%q is not a site or domain", site)
	}
	method := map[string]string{"queries": "GetQueryStats", "pages": "GetPageStats", "drops": "", "almost": "GetQueryStats"}[kind]
	dim := map[string]string{"queries": "query", "pages": "page", "almost": "query"}[kind]

	gfn := func(ctx context.Context, g *gscSource) (map[string]any, error) {
		switch kind {
		case "queries", "pages":
			r, err := g.rows(ctx, dim, false, opt.Limit)
			return map[string]any{kind: tidy(head(r, opt.Limit))}, err
		case "almost":
			r, err := g.rows(ctx, "query", false, analysisRows)
			return map[string]any{"queries": computeAlmost(r, opt.MinAlmostImpressions, opt.Limit),
				"min_impressions": opt.MinAlmostImpressions, "position_range": []float64{AlmostFrom, AlmostTo}}, err
		case "drops":
			d, err := gscDrops(ctx, g, opt)
			return map[string]any{"pages": d.Pages, "queries": d.Queries, "min_impressions": opt.MinDropImpressions}, err
		}
		return nil, apperr.New(apperr.InvalidInput, "unknown mine kind %q", kind)
	}
	bfn := func(ctx context.Context, b *bingSource, warn *[]string) (map[string]any, error) {
		sec := map[string]any{"site": b.site, "granularity": bingGranularity}
		switch kind {
		case "queries", "pages":
			r, err := b.rows(ctx, method, false)
			if err != nil {
				return nil, err
			}
			sec[kind] = tidy(head(r, opt.Limit))
			if l, err := b.links(ctx, opt.Limit); err == nil {
				sec["links"] = l
			} else {
				*warn = append(*warn, "Bing link counts unavailable: "+apperr.From(err).Message)
			}
		case "almost":
			r, err := b.rows(ctx, method, false)
			if err != nil {
				return nil, err
			}
			sec["queries"] = computeAlmost(r, opt.MinAlmostImpressions, opt.Limit)
		case "drops":
			d, err := bingDrops(ctx, b, opt)
			if err != nil {
				return nil, err
			}
			sec["pages"], sec["queries"] = d.Pages, d.Queries
		}
		return sec, nil
	}

	key := cache.Key("mine", kind, host, itoa(opt.Days), itoa(opt.Limit), ftoa(opt.MinDropImpressions), ftoa(opt.MinAlmostImpressions),
		boolStr(signedIn()), boolStr(hasBingKey()), makePeriod(opt.Days).End)
	if !opt.NoCache {
		var c Report
		if _, ok := cache.Get(key, ttl, &c); ok {
			c.Cached = true
			return &c, nil
		}
	}
	o, err := gather(ctx, host, opt.Days, gfn, bfn)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"site": host, "period": periodMap(makePeriod(opt.Days))}
	if o.hasG {
		data["property"] = o.property
		for k, v := range o.gsc {
			data[k] = v
		}
	}
	if o.hasB {
		data["bing"] = o.bing
	}
	rep := &Report{Data: data, Source: o.source, SourceReason: o.why, Warnings: o.warnings}
	cache.Put(key, o.source, rep)
	return rep, nil
}

// DropsResult lists what lost clicks, pages and queries apart.
type DropsResult struct {
	Pages   []Drop `json:"pages"`
	Queries []Drop `json:"queries"`
}

func gscDrops(ctx context.Context, g *gscSource, opt Options) (*DropsResult, error) {
	d := &DropsResult{}
	for _, x := range []struct {
		dim string
		out *[]Drop
	}{{"page", &d.Pages}, {"query", &d.Queries}} {
		cur, err := g.rows(ctx, x.dim, false, analysisRows)
		if err != nil {
			return d, err
		}
		prev, err := g.rows(ctx, x.dim, true, analysisRows)
		if err != nil {
			return d, err
		}
		*x.out = computeDrops(cur, prev, opt.MinDropImpressions, opt.Limit)
	}
	return d, nil
}

func bingDrops(ctx context.Context, b *bingSource, opt Options) (*DropsResult, error) {
	d := &DropsResult{}
	for _, x := range []struct {
		method string
		out    *[]Drop
	}{{"GetPageStats", &d.Pages}, {"GetQueryStats", &d.Queries}} {
		cur, err := b.rows(ctx, x.method, false)
		if err != nil {
			return d, err
		}
		prev, _ := b.rows(ctx, x.method, true)
		*x.out = computeDrops(cur, prev, opt.MinDropImpressions, opt.Limit)
	}
	return d, nil
}

// SummaryData is what a report needs from a verified site.
type SummaryData struct {
	Site     string         `json:"site"`
	Property string         `json:"property,omitempty"` // the Search Console property
	Source   string         `json:"source"`             // "gsc", "bing" or "gsc+bing"
	Period   map[string]any `json:"period"`
	Queries  []Row          `json:"queries,omitempty"`
	Pages    []Row          `json:"pages,omitempty"`
	Drops    *DropsResult   `json:"drops,omitempty"`
	Almost   []Row          `json:"almost,omitempty"`
	// Bing is the separate Bing section: weekly top queries and inbound links.
	Bing     *BingSummary `json:"bing,omitempty"`
	Warnings []string     `json:"warnings,omitempty"`
}

// BingSummary is Bing's side of a Summary.
type BingSummary struct {
	Site        string     `json:"site"`
	Granularity string     `json:"granularity"`
	Queries     []Row      `json:"queries"`
	Links       *BingLinks `json:"links,omitempty"`
}

const summaryLimit = 10

// Summary gathers top queries, top pages, drops, almost and Bing links for a
// site with the default thresholds. When nothing is available it returns an
// error for which errors.Is(err, ErrNotSignedIn) or errors.Is(err,
// ErrNotVerified) is true, so a report can leave the section out. Anything
// else is a real failure.
func Summary(ctx context.Context, site string, days int) (*SummaryData, error) {
	return SummaryWith(ctx, site, Options{Days: days, Limit: summaryLimit})
}

// SummaryWith is Summary with explicit options.
func SummaryWith(ctx context.Context, site string, opt Options) (*SummaryData, error) {
	opt, err := opt.norm()
	if err != nil {
		return nil, err
	}
	host := siteHost(site)
	if host == "" {
		return nil, apperr.New(apperr.InvalidInput, "%q is not a site or domain", site)
	}
	gfn := func(ctx context.Context, g *gscSource) (*SummaryData, error) {
		s := &SummaryData{}
		q, err := g.rows(ctx, "query", false, analysisRows)
		if err != nil {
			return nil, err
		}
		p, err := g.rows(ctx, "page", false, analysisRows)
		if err != nil {
			return nil, err
		}
		s.Queries, s.Pages = tidy(head(q, opt.Limit)), tidy(head(p, opt.Limit))
		s.Almost = computeAlmost(q, opt.MinAlmostImpressions, opt.Limit)
		if s.Drops, err = gscDrops(ctx, g, opt); err != nil {
			return nil, err
		}
		return s, nil
	}
	bfn := func(ctx context.Context, b *bingSource, warn *[]string) (*BingSummary, error) {
		r, err := b.rows(ctx, "GetQueryStats", false)
		if err != nil {
			return nil, err
		}
		bs := &BingSummary{Site: b.site, Granularity: bingGranularity, Queries: tidy(head(r, opt.Limit))}
		if l, err := b.links(ctx, opt.Limit); err == nil {
			bs.Links = l
		} else {
			*warn = append(*warn, "Bing link counts unavailable: "+apperr.From(err).Message)
		}
		return bs, nil
	}
	o, err := gather(ctx, host, opt.Days, gfn, bfn)
	if err != nil {
		return nil, err
	}
	s := o.gsc
	if s == nil {
		s = &SummaryData{}
	}
	s.Site, s.Source, s.Property, s.Period = host, o.source, o.property, periodMap(makePeriod(opt.Days))
	s.Bing = o.bing
	s.Warnings = o.warnings
	return s, nil
}

// ---- small helpers

func hasBingKey() bool { k, _ := config.Key("bing"); return k != "" }

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ttl is how long a mine answer is reused; the data moves once a day.
const ttl = time.Hour

func handler(kind string) ops.Handler {
	return func(ctx context.Context, in ops.Input) (*ops.Result, error) {
		rep, err := Mine(ctx, kind, in.Str("site"), Options{Days: in.Int("days", 28), Limit: in.Int("limit", 20), NoCache: in.NoCache})
		if err != nil {
			return nil, err
		}
		return &ops.Result{Data: rep.Data, Warnings: rep.Warnings,
			Meta: ops.Meta{Source: rep.Source, SourceReason: rep.SourceReason, Cached: rep.Cached}}, nil
	}
}
