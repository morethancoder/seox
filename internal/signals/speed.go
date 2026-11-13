package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/quota"
)

const (
	speedTTL     = 12 // hours
	psiDailyCap  = 25000
	psiQuotaName = "pagespeed"
)

// SpeedOptions are the options of Speed.
type SpeedOptions struct {
	Strategy string // mobile, desktop or both (default both)
	NoCache  bool
}

// Opportunity is one PageSpeed improvement with its estimated saving.
type Opportunity struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	SavingsMs    float64 `json:"savings_ms"`
	SavingsCLS   float64 `json:"savings_cls,omitempty"` // layout shift score saved (unitless, not ms)
	SavingsBytes float64 `json:"savings_bytes,omitempty"`
}

// FieldMetric is a real-user value embedded in the PSI answer (28 days).
type FieldMetric struct {
	P75      float64 `json:"p75"`
	Unit     string  `json:"unit"`
	Category string  `json:"category,omitempty"` // FAST, AVERAGE, SLOW
}

// Lab holds the Lighthouse lab numbers.
type Lab struct {
	LCPms      *float64 `json:"lcp_ms,omitempty"`
	CLS        *float64 `json:"cls,omitempty"`
	TBTms      *float64 `json:"tbt_ms,omitempty"`
	FCPms      *float64 `json:"fcp_ms,omitempty"`
	TTFBms     *float64 `json:"ttfb_ms,omitempty"`
	SpeedIndex *float64 `json:"speed_index_ms,omitempty"`
}

// InteractionMetric is INP from field data, or TBT from the lab when there is no field INP.
type InteractionMetric struct {
	Metric string  `json:"metric"` // INP or TBT
	Value  float64 `json:"value_ms"`
	Source string  `json:"source"` // field or lab
}

// PSIResult is one PageSpeed Insights run.
type PSIResult struct {
	Strategy      string                 `json:"strategy"`
	Source        string                 `json:"source"` // psi or lighthouse-local
	FinalURL      string                 `json:"final_url,omitempty"`
	Score         *int                   `json:"performance_score"` // 0-100, null when Lighthouse failed
	Lab           Lab                    `json:"lab"`
	Interaction   *InteractionMetric     `json:"interaction,omitempty"`
	Opportunities []Opportunity          `json:"opportunities"`
	Field         map[string]FieldMetric `json:"field_data,omitempty"`
	FieldScope    string                 `json:"field_scope,omitempty"` // url or origin
}

// CruxMetric is one CrUX metric: the 75th percentile and the shares of page loads.
type CruxMetric struct {
	P75              float64 `json:"p75"`
	Unit             string  `json:"unit"`
	Rating           string  `json:"rating"` // good, needs-improvement, poor (from the p75)
	Good             float64 `json:"good"`
	NeedsImprovement float64 `json:"needs_improvement"`
	Poor             float64 `json:"poor"`
}

// Trend is the direction of a metric's weekly p75 over the CrUX History window.
type Trend struct {
	Direction string  `json:"direction"` // improving, worsening, stable
	First     float64 `json:"first"`
	Last      float64 `json:"last"`
	ChangePct float64 `json:"change_pct"`
	Points    int     `json:"points"`
}

// CruxResult is the Chrome UX Report answer for the page, else the origin.
type CruxResult struct {
	Scope   string                `json:"scope"` // url or origin
	Target  string                `json:"target"`
	Metrics map[string]CruxMetric `json:"metrics"`
	Trend   map[string]Trend      `json:"trend,omitempty"`
}

// SpeedResult is what Speed returns.
type SpeedResult struct {
	URL      string      `json:"url"`
	Strategy string      `json:"strategy"`
	Mobile   *PSIResult  `json:"mobile,omitempty"`
	Desktop  *PSIResult  `json:"desktop,omitempty"`
	Crux     *CruxResult `json:"crux,omitempty"`
	Keyed    bool        `json:"google_key_used"`

	Warnings  []string `json:"warnings,omitempty"`
	QuotaUsed int      `json:"-"`
	QuotaLeft int      `json:"-"`
	FromCache bool     `json:"-"`
}

func handleSpeed(ctx context.Context, in ops.Input) (*ops.Result, error) {
	res, err := Speed(ctx, in.Str("url"), SpeedOptions{Strategy: in.Str("strategy"), NoCache: in.NoCache})
	if err != nil {
		return nil, err
	}
	out := *res
	out.Warnings = nil
	m := ops.Meta{Source: "pagespeed", QuotaUsed: res.QuotaUsed, QuotaLeft: res.QuotaLeft, Cached: res.FromCache}
	psi, local := false, false
	for _, r := range []*PSIResult{res.Mobile, res.Desktop} {
		if r != nil {
			psi, local = psi || r.Source == "psi", local || r.Source == "lighthouse-local"
		}
	}
	switch {
	case psi && local:
		m.Source = "pagespeed+lighthouse-local"
	case local:
		m.Source = "lighthouse-local"
	}
	if res.Crux != nil {
		m.Source += "+crux"
	}
	if !res.Keyed {
		m.SourceReason = "no google key: Google allows no keyless PageSpeed Insights calls, so lab data comes from local Lighthouse"
	}
	return &ops.Result{Data: &out, Meta: m, Warnings: res.Warnings}, nil
}

// Speed runs PageSpeed Insights (mobile, desktop or both) and, with a google key, CrUX
// and CrUX History for the page (falling back to the origin).
func Speed(ctx context.Context, pageURL string, opt SpeedOptions) (*SpeedResult, error) {
	u, err := normalizeURL(pageURL)
	if err != nil {
		return nil, err
	}
	strategy := strings.ToLower(strings.TrimSpace(opt.Strategy))
	if strategy == "" {
		strategy = "both"
	}
	if strategy != "mobile" && strategy != "desktop" && strategy != "both" {
		return nil, apperr.New(apperr.InvalidInput, "strategy must be mobile, desktop or both, not %q", opt.Strategy)
	}
	key, _ := config.Key("google")
	ck := cache.Key("speed", u, strategy, strconv.FormatBool(key != ""))
	if !opt.NoCache {
		var c SpeedResult
		if _, ok := cache.Get(ck, ttl(speedTTL), &c); ok {
			c.FromCache = true
			return &c, nil
		}
	}
	res := &SpeedResult{URL: u, Strategy: strategy, Keyed: key != ""}
	q := quota.New(psiQuotaName, psiDailyCap)
	partial := false // something failed or fell back: do not cache

	var strategies []string
	if strategy == "both" {
		strategies = []string{"mobile", "desktop"}
	} else {
		strategies = []string{strategy}
	}
	type out struct {
		r   *PSIResult
		err error
	}
	outs := make([]out, len(strategies))
	if key != "" {
		var wg sync.WaitGroup
		for i, s := range strategies {
			if err := q.Spend(1); err != nil {
				outs[i].err = err
				continue
			}
			wg.Add(1)
			go func(i int, s string) {
				defer wg.Done()
				outs[i].r, outs[i].err = runPSI(ctx, u, s, key, q)
			}(i, s)
		}
		wg.Wait()
		res.QuotaUsed, res.QuotaLeft = q.Used(), q.Left()
	} else {
		res.Warnings = append(res.Warnings, noKeyWarning)
	}
	if err := ctx.Err(); err != nil {
		return nil, apperr.From(err)
	}
	// Local Lighthouse, one run at a time: for every strategy without a key, and as the fallback
	// when PSI failed with a key.
	for i, s := range strategies {
		if outs[i].r != nil {
			continue
		}
		if key != "" {
			partial = true
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: PageSpeed Insights failed (%s): lab data from local Lighthouse, no PSI field data", s, outs[i].err.Error()))
		}
		r, err := runLocalLighthouse(ctx, u, s)
		if err != nil {
			if ae := apperr.From(err); ae.Code == apperr.Timeout || key == "" {
				return nil, ae
			}
			// With a key, keep the other strategy and CrUX; "no speed data" below covers both failing.
			partial = true
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: local Lighthouse unavailable or failed: %s", s, err.Error()))
			continue
		}
		outs[i].r = r
	}
	for i, o := range outs {
		if o.r == nil {
			continue
		}
		if strategies[i] == "mobile" {
			res.Mobile = o.r
		} else {
			res.Desktop = o.r
		}
	}
	if res.Mobile == nil && res.Desktop == nil {
		return nil, apperr.New(apperr.SourceError, "no speed data: %s", strings.Join(res.Warnings, "; "))
	}

	if key != "" {
		c, warns, err := runCrux(ctx, u, key)
		if err != nil {
			return nil, err
		}
		res.Crux = c
		res.Warnings = append(res.Warnings, warns...)
		if hasSkip(warns) { // a no-CrUX answer is cached; only a failed lookup is not
			partial = true
		}
	}
	if !partial && ctx.Err() == nil {
		cache.Put(ck, "pagespeed", res)
	}
	return res, nil
}

const noKeyWarning = "No google key: lab data from local Lighthouse, no real-user (CrUX) data. Fix: seox key set google (free)"

func hasSkip(w []string) bool {
	for _, x := range w {
		if strings.Contains(x, "skipped") {
			return true
		}
	}
	return false
}

func normalizeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", apperr.New(apperr.InvalidInput, "a URL is required")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	p, err := url.Parse(s)
	if err != nil || p.Host == "" || (p.Scheme != "http" && p.Scheme != "https") {
		return "", apperr.New(apperr.InvalidInput, "not a usable http(s) URL: %q", s)
	}
	return p.String(), nil
}

// ---- PageSpeed Insights

type psiAudit struct {
	Title        string   `json:"title"`
	Score        *float64 `json:"score"`
	NumericValue *float64 `json:"numericValue"`
	// MetricSavings is where Lighthouse 13 "insight" audits put their estimated savings (ms for LCP/FCP).
	MetricSavings map[string]float64 `json:"metricSavings"`
	Details       struct {
		Type                string  `json:"type"`
		OverallSavingsMs    float64 `json:"overallSavingsMs"`
		OverallSavingsBytes float64 `json:"overallSavingsBytes"`
	} `json:"details"`
}

// lhResult is Lighthouse's own JSON: PSI's lighthouseResult and the local CLI output share it.
type lhResult struct {
	FinalURL   string `json:"finalUrl"`
	Categories struct {
		Performance struct {
			Score *float64 `json:"score"`
		} `json:"performance"`
	} `json:"categories"`
	Audits map[string]psiAudit `json:"audits"`
}

type psiField struct {
	Metrics map[string]struct {
		Percentile float64 `json:"percentile"`
		Category   string  `json:"category"`
	} `json:"metrics"`
	OriginFallback bool `json:"origin_fallback"`
}

type psiResponse struct {
	ID               string   `json:"id"`
	LoadingExp       psiField `json:"loadingExperience"`
	OriginLoadingExp psiField `json:"originLoadingExperience"`
	Lighthouse       lhResult `json:"lighthouseResult"`
}

func runPSI(ctx context.Context, pageURL, strategy, key string, q *quota.Tracker) (*PSIResult, error) {
	base := baseURL("psi", "https://www.googleapis.com/pagespeedonline/v5")
	v := url.Values{"url": {pageURL}, "strategy": {strategy}, "category": {"performance"}}
	if key != "" {
		v.Set("key", key)
	}
	var raw psiResponse
	r, err := doGet(ctx, base+"/runPagespeed?"+v.Encode(), 16<<20)
	if err != nil {
		return nil, apperr.From(errRedact(err, key))
	}
	status := r.Status
	switch {
	case status == 429:
		// Only a keyed per-day quota answer uses up the local bucket; a burst or keyless 429 does not.
		if key != "" && bytes.Contains(r.Body, []byte("PerDay")) {
			q.Exhaust()
		}
		if key == "" {
			return nil, apperr.New(apperr.QuotaExceeded, "PageSpeed Insights refused a keyless call (429). Fix: seox key set google (free)")
		}
		return nil, apperr.New(apperr.QuotaExceeded, "PageSpeed Insights limit reached for this key (429). Fix: wait and retry, or check the key's quota in Google Cloud")
	case status == 400:
		return nil, apperr.New(apperr.InvalidInput, "PageSpeed Insights could not test %s (400): check the URL is public and reachable", pageURL)
	case status == 403:
		return nil, apperr.New(apperr.Forbidden, "PageSpeed Insights refused the request (403): the google key is invalid or the PageSpeed Insights API is not enabled for it")
	case status/100 != 2:
		return nil, apperr.New(apperr.SourceError, "PageSpeed Insights answered HTTP %d", status)
	}
	if err := json.Unmarshal(r.Body, &raw); err != nil {
		return nil, apperr.Wrap(apperr.SourceError, err, "unreadable PageSpeed Insights answer")
	}
	return parsePSI(&raw, strategy), nil
}

func errRedact(err error, key string) error {
	if key == "" || err == nil {
		return err
	}
	if ae, ok := err.(*apperr.Error); ok {
		c := *ae
		c.Message = redact(c.Message, key)
		return &c
	}
	return fmt.Errorf("%s", redact(err.Error(), key))
}

func parsePSI(raw *psiResponse, strategy string) *PSIResult {
	r := &PSIResult{Strategy: strategy, Source: "psi", FinalURL: raw.Lighthouse.FinalURL, Opportunities: []Opportunity{}}
	if s := raw.Lighthouse.Categories.Performance.Score; s != nil {
		n := int(*s*100 + 0.5)
		r.Score = &n
	}
	num := func(id string) *float64 {
		if a, ok := raw.Lighthouse.Audits[id]; ok {
			return a.NumericValue
		}
		return nil
	}
	r.Lab = Lab{LCPms: num("largest-contentful-paint"), CLS: num("cumulative-layout-shift"), TBTms: num("total-blocking-time"),
		FCPms: num("first-contentful-paint"), TTFBms: num("server-response-time"), SpeedIndex: num("speed-index")}

	fe := raw.LoadingExp
	r.FieldScope = "url"
	if len(fe.Metrics) == 0 || fe.OriginFallback {
		if len(raw.OriginLoadingExp.Metrics) > 0 {
			fe, r.FieldScope = raw.OriginLoadingExp, "origin"
		} else if len(fe.Metrics) == 0 {
			r.FieldScope = ""
		}
	}
	if len(fe.Metrics) > 0 {
		r.Field = map[string]FieldMetric{}
		names := map[string]string{"LARGEST_CONTENTFUL_PAINT_MS": "lcp", "CUMULATIVE_LAYOUT_SHIFT_SCORE": "cls",
			"INTERACTION_TO_NEXT_PAINT": "inp", "FIRST_CONTENTFUL_PAINT_MS": "fcp", "EXPERIMENTAL_TIME_TO_FIRST_BYTE": "ttfb"}
		for k, m := range fe.Metrics {
			n, ok := names[k]
			if !ok {
				continue
			}
			fm := FieldMetric{P75: m.Percentile, Unit: "ms", Category: m.Category}
			if n == "cls" {
				fm.P75, fm.Unit = m.Percentile/100, "score" // PSI reports CLS multiplied by 100
			}
			r.Field[n] = fm
		}
	}
	if inp, ok := r.Field["inp"]; ok {
		r.Interaction = &InteractionMetric{Metric: "INP", Value: inp.P75, Source: "field"}
	} else if r.Lab.TBTms != nil {
		r.Interaction = &InteractionMetric{Metric: "TBT", Value: *r.Lab.TBTms, Source: "lab"}
	}
	for id, a := range raw.Lighthouse.Audits {
		ms := a.Details.OverallSavingsMs
		cls := 0.0
		if a.Score != nil && *a.Score < 1 {
			cls = a.MetricSavings["CLS"]
		}
		if ms <= 0 && a.Score != nil && *a.Score < 1 {
			// Lighthouse 13 insight audits: savings are in metricSavings (ms), ranked by LCP, then FCP, then TBT.
			for _, k := range []string{"LCP", "FCP", "TBT"} {
				if v := a.MetricSavings[k]; v > 0 {
					ms = v
					break
				}
			}
		}
		if ms > 0 || cls > 0 {
			r.Opportunities = append(r.Opportunities, Opportunity{ID: id, Title: a.Title, SavingsMs: ms, SavingsCLS: cls, SavingsBytes: a.Details.OverallSavingsBytes})
		}
	}
	sort.Slice(r.Opportunities, func(i, j int) bool {
		if r.Opportunities[i].SavingsMs != r.Opportunities[j].SavingsMs {
			return r.Opportunities[i].SavingsMs > r.Opportunities[j].SavingsMs
		}
		if r.Opportunities[i].SavingsCLS != r.Opportunities[j].SavingsCLS {
			return r.Opportunities[i].SavingsCLS > r.Opportunities[j].SavingsCLS
		}
		return r.Opportunities[i].ID < r.Opportunities[j].ID
	})
	if len(r.Opportunities) > 5 {
		r.Opportunities = r.Opportunities[:5]
	}
	return r
}

// ---- CrUX and CrUX History

var cruxMetricNames = map[string]struct {
	key        string
	unit       string
	good, poor float64
}{
	"largest_contentful_paint":        {"lcp", "ms", 2500, 4000},
	"cumulative_layout_shift":         {"cls", "score", 0.1, 0.25},
	"interaction_to_next_paint":       {"inp", "ms", 200, 500},
	"first_contentful_paint":          {"fcp", "ms", 1800, 3000},
	"experimental_time_to_first_byte": {"ttfb", "ms", 800, 1800},
}

type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*f = flexNum(nanVal)
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexNum(n)
	return nil
}

type cruxResponse struct {
	Record struct {
		Metrics map[string]struct {
			Histogram []struct {
				Density float64 `json:"density"`
			} `json:"histogram"`
			Percentiles struct {
				P75 flexNum `json:"p75"`
			} `json:"percentiles"`
			Timeseries struct {
				P75s []flexNum `json:"p75s"`
			} `json:"percentilesTimeseries"`
		} `json:"metrics"`
	} `json:"record"`
}

// cruxQuery posts one CrUX request. found is false on 404 (no data for the target).
func cruxQuery(ctx context.Context, method, key string, body map[string]string) (resp *cruxResponse, found bool, err error) {
	base := baseURL("crux", "https://chromeuxreport.googleapis.com/v1")
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/records:"+method+"?key="+url.QueryEscape(key), bytes.NewReader(b))
	if err != nil {
		return nil, false, apperr.Wrap(apperr.Internal, err, "building CrUX request")
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := doReq(ctx, req, 4<<20)
	if err != nil {
		return nil, false, errRedact(err, key)
	}
	switch {
	case r.Status == 404:
		return nil, false, nil
	case r.Status == 429:
		return nil, false, apperr.New(apperr.QuotaExceeded, "CrUX rate limit reached (150 requests a minute): try again shortly")
	case r.Status == 400:
		return nil, false, apperr.New(apperr.InvalidInput, "CrUX rejected the request (400): the URL or origin is malformed")
	case r.Status == 403:
		return nil, false, apperr.New(apperr.Forbidden, "CrUX refused the request (403): check the google key allows the Chrome UX Report API")
	case r.Status/100 != 2:
		return nil, false, apperr.New(apperr.SourceError, "CrUX answered HTTP %d", r.Status)
	}
	var out cruxResponse
	if err := json.Unmarshal(r.Body, &out); err != nil {
		return nil, false, apperr.Wrap(apperr.SourceError, err, "unreadable CrUX answer")
	}
	return &out, true, nil
}

func runCrux(ctx context.Context, pageURL, key string) (*CruxResult, []string, error) {
	var warns []string
	p, _ := url.Parse(pageURL)
	origin := p.Scheme + "://" + p.Host
	targets := []struct{ scope, field, val string }{{"url", "url", pageURL}}
	if pageURL != origin && pageURL != origin+"/" {
		targets = append(targets, struct{ scope, field, val string }{"origin", "origin", origin})
	} else {
		targets = []struct{ scope, field, val string }{{"origin", "origin", origin}}
	}
	for _, t := range targets {
		resp, found, err := cruxQuery(ctx, "queryRecord", key, map[string]string{t.field: t.val})
		if err != nil {
			if ae := apperr.From(err); ae.Code == apperr.Timeout {
				return nil, nil, ae
			}
			return nil, append(warns, "CrUX skipped: "+err.Error()), nil
		}
		if !found {
			continue
		}
		res := &CruxResult{Scope: t.scope, Target: t.val, Metrics: map[string]CruxMetric{}}
		for name, m := range resp.Record.Metrics {
			info, ok := cruxMetricNames[name]
			if !ok {
				continue
			}
			cm := CruxMetric{P75: float64(m.Percentiles.P75), Unit: info.unit}
			if len(m.Histogram) == 3 {
				cm.Good, cm.NeedsImprovement, cm.Poor = m.Histogram[0].Density, m.Histogram[1].Density, m.Histogram[2].Density
			}
			cm.Rating = "needs-improvement"
			if cm.P75 <= info.good {
				cm.Rating = "good"
			} else if cm.P75 > info.poor {
				cm.Rating = "poor"
			}
			res.Metrics[info.key] = cm
		}
		hist, hfound, herr := cruxQuery(ctx, "queryHistoryRecord", key, map[string]string{t.field: t.val})
		switch {
		case herr != nil && apperr.From(herr).Code == apperr.Timeout:
			return nil, nil, apperr.From(herr)
		case herr != nil:
			warns = append(warns, "CrUX History skipped: "+herr.Error())
		case hfound:
			res.Trend = map[string]Trend{}
			for name, m := range hist.Record.Metrics {
				if info, ok := cruxMetricNames[name]; ok {
					if tr, ok := trendOf(m.Timeseries.P75s); ok {
						res.Trend[info.key] = tr
					}
				}
			}
		}
		if t.scope == "origin" && len(targets) > 1 {
			warns = append(warns, "No CrUX data for this page: showing the origin "+origin)
		}
		return res, warns, nil
	}
	return nil, append(warns, "CrUX has no data for this page or its origin (too little traffic)"), nil
}

// trendOf compares the average of the first and last quarter (at least 1, at most 4 points) of
// the weekly p75 series. All CrUX metrics are lower-is-better. A move under 5% is stable.
func trendOf(series []flexNum) (Trend, bool) {
	var pts []float64
	for _, v := range series {
		if f := float64(v); f == f {
			pts = append(pts, f)
		}
	}
	if len(pts) < 2 {
		return Trend{}, false
	}
	n := len(pts) / 4
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	avg := func(s []float64) float64 {
		t := 0.0
		for _, v := range s {
			t += v
		}
		return t / float64(len(s))
	}
	first, last := avg(pts[:n]), avg(pts[len(pts)-n:])
	tr := Trend{First: first, Last: last, Points: len(pts), Direction: "stable"}
	if first > 0 {
		tr.ChangePct = (last - first) / first * 100
	}
	switch {
	case tr.ChangePct > 5:
		tr.Direction = "worsening"
	case tr.ChangePct < -5:
		tr.Direction = "improving"
	}
	return tr, true
}
