package rank

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/web"
)

func init() { ops.Handle("rank", handle) }

const (
	// DefaultDepth is how many result pages are read per keyword.
	DefaultDepth = 3
	// CacheTTL is how long a keyword's answer is reused.
	CacheTTL = 24 * time.Hour
	// defaultGap is the pause between queries so the upstream engines do not block us.
	defaultGap = 3 * time.Second
	// maxGap caps the gap after rate-limit backoffs.
	maxGap = 30 * time.Second
	// assumedPage is an engine's page size when it gave nothing on an earlier page.
	assumedPage = 10
	maxBody     = 4 << 20
)

// Options for Positions.
type Options struct {
	Lang    string // SearXNG "language", default "en"
	Depth   int    // result pages per keyword, default 3
	NoCache bool
	// Config is where to find SearXNG; nil loads the user's config.
	Config *config.Config
	// Instance reuses an open SearXNG (compare opens one for many calls).
	// When nil, Positions opens one and stops it before returning.
	Instance *Instance
	// Gap is the pause between queries (plus jitter); 0 means 3 s, negative means none (tests).
	Gap time.Duration
}

// Hit is where a site ranked for one keyword.
type Hit struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// KeywordResult is one keyword's outcome.
type KeywordResult struct {
	Keyword string `json:"keyword"`
	// BestPosition is the overall rank as SearXNG merges the engines; null when not found.
	BestPosition *int `json:"best_position"`
	// ByEngine is the position in each engine's own list, adjusted for the page.
	ByEngine map[string]int `json:"by_engine,omitempty"`
	URL      string         `json:"url,omitempty"`
	Title    string         `json:"title,omitempty"`
	// Google is true only when Google contributed the ranking result.
	Google bool `json:"google"`
	// Label says "not Google" unless Google answered.
	Label  string `json:"label"`
	Note   string `json:"note,omitempty"`
	Cached bool   `json:"cached,omitempty"`
}

// Result is the answer for all keywords.
type Result struct {
	Site     string          `json:"site"`
	Lang     string          `json:"lang"`
	Depth    int             `json:"depth"`
	Keywords []KeywordResult `json:"keywords"`
	// Warnings are the unresponsive engines (de-duplicated) and other notes.
	Warnings []string `json:"warnings,omitempty"`
	// Started is set when seox started its own SearXNG for this call.
	Started bool `json:"-"`
}

type cached struct {
	KW KeywordResult `json:"kw"`
	// Engines are the unresponsive engines as [name, reason] pairs.
	Engines [][2]string `json:"engines,omitempty"`
}

// cacheKey is the one place the cache key is built; Missing uses it too.
func cacheKey(kw, host, lang string, depth int) string {
	return cache.Key("rank", kw, host, lang, strconv.Itoa(depth))
}

// Missing reports whether any keyword would miss the cache, that is, whether
// Positions would need a SearXNG. Callers use it to start one only when needed.
func Missing(site string, keywords []string, opt Options) bool {
	if opt.NoCache {
		return true
	}
	opt = opt.withDefaults()
	host := siteHost(site)
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		var c cached
		if _, ok := cache.Get(cacheKey(kw, host, opt.Lang, opt.Depth), CacheTTL, &c); !ok {
			return true
		}
	}
	return false
}

func (opt Options) withDefaults() Options {
	if opt.Lang == "" {
		opt.Lang = "en"
	}
	if opt.Depth <= 0 {
		opt.Depth = DefaultDepth
	}
	return opt
}

// Positions finds where site ranks for each keyword. It starts SearXNG once
// for all keywords and stops it on return (or when ctx ends).
func Positions(ctx context.Context, site string, keywords []string, opt Options) (*Result, error) {
	host := siteHost(site)
	if host == "" {
		return nil, apperr.New(apperr.InvalidInput, "cannot read a site from %q", site)
	}
	if len(keywords) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "give at least one keyword")
	}
	opt = opt.withDefaults()
	gap := opt.Gap
	if gap == 0 {
		gap = defaultGap
	}
	pace := &pacer{gap: gap}
	res := &Result{Site: host, Lang: opt.Lang, Depth: opt.Depth}
	warn := newWarnSet()

	inst := opt.Instance
	// Open lazily: a fully cached run must not start SearXNG at all.
	open := func() (*Instance, error) {
		if inst != nil {
			return inst, nil
		}
		cfg := opt.Config
		if cfg == nil {
			var err error
			if cfg, err = config.Load(); err != nil {
				return nil, apperr.Wrap(apperr.Internal, err, "cannot read the config")
			}
		}
		i, err := Open(ctx, cfg)
		if err != nil {
			return nil, err
		}
		inst = i
		res.Started = i.Own()
		for _, n := range i.Notes {
			warn.add(n)
		}
		return inst, nil
	}
	if opt.Instance == nil {
		defer func() {
			if inst != nil {
				inst.Stop()
			}
		}()
	}

	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		key := cacheKey(kw, host, opt.Lang, opt.Depth)
		if !opt.NoCache {
			var c cached
			if _, ok := cache.Get(key, CacheTTL, &c); ok {
				c.KW.Cached = true
				res.Keywords = append(res.Keywords, c.KW)
				for _, e := range c.Engines {
					warn.addEngine(e[0], e[1])
				}
				continue
			}
		}
		in, err := open()
		if err != nil {
			return nil, err
		}
		kr, ws, err := search(ctx, in.URL, host, kw, opt, pace)
		if err != nil {
			return nil, err
		}
		for _, e := range ws {
			warn.addEngine(e[0], e[1])
		}
		if pace.slowed {
			warn.add("search engines rate-limited seox; queries were slowed down (gap now " + pace.gap.String() + ")")
		}
		// A failed lookup (no results at all) is not cached, so the next run tries the engines again.
		if !strings.HasPrefix(kr.Note, "no data") {
			cache.Put(key, "searxng", cached{KW: *kr, Engines: ws})
		}
		res.Keywords = append(res.Keywords, *kr)
	}
	res.Warnings = warn.list
	return res, nil
}

// searxResponse is the part of SearXNG's JSON that rank reads.
type searxResponse struct {
	Results []struct {
		URL       string   `json:"url"`
		Title     string   `json:"title"`
		Engines   []string `json:"engines"`
		Positions []int    `json:"positions"`
	} `json:"results"`
	Unresponsive [][]string `json:"unresponsive_engines"`
}

// search reads result pages 1..depth for one keyword and finds the site.
func search(ctx context.Context, base, host, kw string, opt Options, pace *pacer) (*KeywordResult, [][2]string, error) {
	kr := &KeywordResult{Keyword: kw, Label: "not Google"}
	var warns [][2]string
	seen := 0
	// offsets[engine] is how many places the engine's earlier pages took up.
	offsets := map[string]int{}
	for page := 1; page <= opt.Depth; page++ {
		if err := pace.wait(ctx); err != nil {
			return nil, nil, err
		}
		q := url.Values{"q": {kw}, "format": {"json"}, "pageno": {strconv.Itoa(page)}, "language": {opt.Lang}}
		r, err := web.Get(ctx, base+"/search?"+q.Encode(), maxBody)
		if err != nil {
			return nil, nil, err
		}
		if r.Status != 200 {
			return nil, nil, apperr.New(apperr.SourceError, "SearXNG answered %d for %q (is the json format on?)", r.Status, kw)
		}
		var sr searxResponse
		if err := json.Unmarshal(r.Body, &sr); err != nil {
			return nil, nil, apperr.Wrap(apperr.SourceError, err, "SearXNG sent something that is not JSON")
		}
		for _, u := range sr.Unresponsive {
			if len(u) > 0 {
				reason := ""
				if len(u) > 1 {
					reason = u[1]
				}
				warns = append(warns, [2]string{u[0], reason})
				pace.limited(u[0], reason)
			}
		}
		pageMax := map[string]int{}
		for _, it := range sr.Results {
			seen++
			for j, e := range it.Engines {
				if j < len(it.Positions) && it.Positions[j] > pageMax[e] {
					pageMax[e] = it.Positions[j]
				}
			}
			if kr.BestPosition != nil || !sameSite(host, it.URL) {
				continue
			}
			pos := seen
			kr.BestPosition, kr.URL, kr.Title = &pos, it.URL, it.Title
			kr.ByEngine = map[string]int{}
			for j, e := range it.Engines {
				if j < len(it.Positions) {
					kr.ByEngine[e] = offsets[e] + it.Positions[j]
				}
				if e == "google" {
					kr.Google = true
				}
			}
		}
		if kr.BestPosition != nil {
			break
		}
		if len(sr.Results) == 0 {
			break // the engines have nothing more
		}
		// Next page's offset per engine: what this one filled, or a typical page.
		for _, e := range allEngines(sr) {
			if pageMax[e] > 0 {
				offsets[e] += pageMax[e]
			} else {
				offsets[e] += assumedPage
			}
		}
	}
	switch {
	case kr.BestPosition == nil && seen == 0:
		// Zero results means the engines failed (rate limits, timeouts), not that the site is unranked.
		kr.Note = "no data (engines gave no results; see warnings)"
	case kr.BestPosition == nil:
		kr.Note = "not in top " + strconv.Itoa(seen)
	case kr.Google:
		kr.Label = "Google"
	}
	return kr, warns, nil
}

func allEngines(sr searxResponse) []string {
	m := map[string]bool{}
	for _, it := range sr.Results {
		for _, e := range it.Engines {
			m[e] = true
		}
	}
	out := make([]string, 0, len(m))
	for e := range m {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// siteHost reduces a site, domain or URL to a lower-case host without www.
func siteHost(site string) string {
	s := strings.TrimSpace(site)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// sameSite is true when the result's host is the site's host or one of its
// subdomains, with www and the apex counted as one.
func sameSite(host, rawURL string) bool {
	h := siteHost(rawURL)
	return h != "" && (h == host || strings.HasSuffix(h, "."+host))
}

// warnSet lists each warning once; an engine is listed once with the first
// reason seen, since SearXNG words the same failure differently per page
// ("too many requests", "Suspended: too many requests").
type warnSet struct {
	seen map[string]bool
	list []string
}

func newWarnSet() *warnSet { return &warnSet{seen: map[string]bool{}} }
func (w *warnSet) add(s string) {
	if !w.seen[s] {
		w.seen[s] = true
		w.list = append(w.list, s)
	}
}
func (w *warnSet) addEngine(name, reason string) {
	if w.seen["engine:"+name] {
		return
	}
	w.seen["engine:"+name] = true
	msg := "engine " + name + " did not answer"
	// SearXNG repeats a failed engine's reason as "Suspended: <reason>" on later pages; it is the same failure.
	reason = strings.TrimSpace(strings.TrimPrefix(reason, "Suspended:"))
	if reason != "" {
		msg += " (" + reason + ")"
	}
	w.list = append(w.list, msg)
}

// Pacing hooks; tests replace them so nothing really sleeps.
var (
	// jitterMax is the most random time added to each gap.
	jitterMax = 2 * time.Second
	sleep     = sleepCtx
	jitter    = func(max time.Duration) time.Duration {
		if max <= 0 {
			return 0
		}
		return time.Duration(rand.Int63n(int64(max)))
	}
)

// pacer spaces every SearXNG query of one run (all keywords and pages), since
// the engines behind it answer a burst with CAPTCHAs or "too many requests".
type pacer struct {
	gap     time.Duration
	started bool
	slowed  bool
	hit     map[string]bool
}

// wait blocks before every query but the first; a cancel returns the timeout code.
func (p *pacer) wait(ctx context.Context) error {
	if !p.started {
		p.started = true
		if err := ctx.Err(); err != nil {
			return apperr.From(err)
		}
		return nil
	}
	d := p.gap
	if d >= 0 {
		d += jitter(jitterMax)
	}
	if !sleep(ctx, d) {
		return apperr.From(ctx.Err())
	}
	return nil
}

// limited doubles the gap (up to maxGap) the first time an engine reports a
// rate limit or CAPTCHA; later "Suspended" repeats of the same engine are the same failure.
func (p *pacer) limited(engine, reason string) {
	r := strings.ToLower(reason)
	if !(strings.Contains(r, "too many") || strings.Contains(r, "captcha") ||
		strings.Contains(r, "rate limit") || strings.Contains(r, "429")) {
		return
	}
	if p.hit == nil {
		p.hit = map[string]bool{}
	}
	if p.hit[engine] || p.gap < 0 {
		return
	}
	p.hit[engine] = true
	p.gap = min(p.gap*2, maxGap)
	p.slowed = true
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d < 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func handle(ctx context.Context, in ops.Input) (*ops.Result, error) {
	kws := in.Strings("keyword")
	res, err := Positions(ctx, in.Str("site"), kws, Options{
		Lang:    in.Str("lang"),
		Depth:   in.Int("depth", DefaultDepth),
		NoCache: in.NoCache,
		Gap:     gapOption(in),
	})
	if err != nil {
		return nil, err
	}
	meta := ops.Meta{Source: "searxng"}
	if res.Started {
		meta.SourceReason = "started seox's own copy of Arshafa's SearXNG"
	}
	all := len(res.Keywords) > 0
	for _, k := range res.Keywords {
		all = all && k.Cached
	}
	meta.Cached = all
	return &ops.Result{Data: res, Meta: meta, Warnings: res.Warnings}, nil
}

// gapOption reads --gap (whole seconds); 0 means no pause, absent keeps the default.
func gapOption(in ops.Input) time.Duration {
	if !in.Has("gap") {
		return 0
	}
	n := in.Int("gap", int(defaultGap/time.Second))
	if n <= 0 {
		return -1
	}
	return time.Duration(n) * time.Second
}
