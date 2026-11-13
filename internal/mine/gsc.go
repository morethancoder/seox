package mine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/web"
)

// Sentinel errors a report can test with errors.Is to skip this section.
var (
	// ErrNotVerified means no Search Console property or Bing site matches the site.
	ErrNotVerified = errors.New("site is not verified in Search Console or Bing")
	// ErrNotSignedIn means there is no Google sign-in and no Bing key.
	ErrNotSignedIn = errors.New("not signed in to Search Console and no Bing key")
)

func notSignedIn(msg string) *apperr.Error {
	return &apperr.Error{Code: apperr.LoginRequired, Message: msg, Err: ErrNotSignedIn, Detail: map[string]string{"command": LoginFix}}
}

func notVerified(format string, args ...any) *apperr.Error {
	return &apperr.Error{Code: apperr.NotFound, Message: fmt.Sprintf(format, args...), Err: ErrNotVerified}
}

func gscBase() string { return web.BaseURL("gsc", "https://www.googleapis.com/webmasters/v3") }

// Row is one line of search data: a query or a page with its metrics.
type Row struct {
	Key         string  `json:"key"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

// Site is one Search Console property.
type Site struct {
	URL        string `json:"site_url"`
	Permission string `json:"permission_level"`
}

// maxGSCRows is Search Analytics' own cap for one request (a var so a test can force paging).
var maxGSCRows = 25000

// gscGet and gscPost send an authorised request through the shared client and
// turn Google's error answers into coded errors.
func gscDo(ctx context.Context, token, method, path string, body any) ([]byte, error) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, gscBase()+path, rd)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "bad Search Console request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := web.Do(ctx, req, 0)
	if err != nil {
		return nil, err
	}
	if resp.Status/100 == 2 {
		return resp.Body, nil
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp.Body, &e)
	msg := e.Error.Message
	if msg == "" {
		msg = http.StatusText(resp.Status)
	}
	switch resp.Status {
	case 401:
		return nil, &apperr.Error{Code: apperr.LoginRequired, Message: "Google refused the sign-in (" + msg + "); run `" + LoginFix + "`", Err: ErrNotSignedIn}
	case 403:
		return nil, apperr.New(apperr.Forbidden, "Search Console refused this request: %s", msg)
	case 404:
		return nil, apperr.New(apperr.NotFound, "Search Console: %s", msg)
	case 429:
		return nil, apperr.New(apperr.QuotaExceeded, "Search Console quota used up: %s", msg)
	}
	return nil, apperr.New(apperr.SourceError, "Search Console answered %d: %s", resp.Status, msg)
}

// listSites is sites.list: every property the account can see.
func listSites(ctx context.Context, token string) ([]Site, error) {
	b, err := gscDo(ctx, token, http.MethodGet, "/sites", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		SiteEntry []struct {
			SiteURL         string `json:"siteUrl"`
			PermissionLevel string `json:"permissionLevel"`
		} `json:"siteEntry"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, apperr.Wrap(apperr.SourceError, err, "couldn't read Search Console's site list")
	}
	sites := make([]Site, 0, len(out.SiteEntry))
	for _, e := range out.SiteEntry {
		sites = append(sites, Site{URL: e.SiteURL, Permission: e.PermissionLevel})
	}
	return sites, nil
}

// siteHost reduces a domain or URL to its lowercase host without "www.".
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

// matchProperty picks the property for a site: sc-domain:<domain> (the exact
// host first, then a parent domain) before any URL-prefix property that is on
// the same host, https before http and the shortest path first. Properties
// where the account is only an unverified user are not usable and are reported
// through unverified.
func matchProperty(sites []Site, host string) (best *Site, unverified *Site) {
	bestScore := -1
	for i := range sites {
		s := &sites[i]
		score := -1
		if d, ok := strings.CutPrefix(s.URL, "sc-domain:"); ok {
			d = strings.ToLower(d)
			switch {
			case d == host:
				score = 1000
			case strings.HasSuffix(host, "."+d):
				score = 900 + len(d)
			}
		} else if u, err := url.Parse(s.URL); err == nil && strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") == host {
			score = 500 - len(u.Path)
			if u.Scheme == "https" {
				score += 50
			}
		}
		if score < 0 {
			continue
		}
		if s.Permission == "siteUnverifiedUser" {
			if unverified == nil {
				unverified = s
			}
			continue
		}
		if score > bestScore {
			best, bestScore = s, score
		}
	}
	return best, unverified
}

// gscSource reads Search Analytics for one property and keeps what it
// fetched, so drops and almost on the same data cost one call each.
type gscSource struct {
	token string
	prop  string
	per   period
	memo  map[string][]Row
	asked map[string]int
}

// period is the current window and the one before it, both of Days days.
type period struct {
	Days                 int
	Start, End           string
	PrevStart, PrevEnd   string
	startT, endT, pS, pE time.Time
}

// now is the clock; tests fix it.
var now = time.Now

// dataDelay is how many days before today the range ends: Search Console's
// final data lags by about two days.
const dataDelay = 2

func makePeriod(days int) period {
	if days < 1 {
		days = 28
	}
	t := now().UTC()
	end := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -dataDelay)
	start := end.AddDate(0, 0, -(days - 1))
	pe := start.AddDate(0, 0, -1)
	ps := pe.AddDate(0, 0, -(days - 1))
	f := func(t time.Time) string { return t.Format("2006-01-02") }
	return period{Days: days, Start: f(start), End: f(end), PrevStart: f(ps), PrevEnd: f(pe), startT: start, endT: end, pS: ps, pE: pe}
}

func newGSC(ctx context.Context, site string, days int) (*gscSource, error) {
	token, err := accessToken(ctx)
	if err != nil {
		return nil, err
	}
	host := siteHost(site)
	if host == "" {
		return nil, apperr.New(apperr.InvalidInput, "%q is not a site or domain", site)
	}
	sites, err := listSites(ctx, token)
	if err != nil {
		return nil, err
	}
	p, un := matchProperty(sites, host)
	if p == nil {
		if un != nil {
			return nil, notVerified("your account is only an unverified user of %s; finish verifying it, see `seox setup verify %s`", un.URL, host)
		}
		return nil, notVerified("no Search Console property for %s in the signed-in account; see `seox setup verify %s`", host, host)
	}
	return &gscSource{token: token, prop: p.URL, per: makePeriod(days), memo: map[string][]Row{}, asked: map[string]int{}}, nil
}

// query pages searchAnalytics.query up to limit rows for one dimension
// ("query" or "page") and one date range. Rows come back sorted by clicks.
func (g *gscSource) query(ctx context.Context, dim, start, end string, limit int) ([]Row, error) {
	if limit < 1 {
		limit = 1
	}
	var rows []Row
	for len(rows) < limit {
		n := min(limit-len(rows), maxGSCRows)
		b, err := gscDo(ctx, g.token, http.MethodPost, "/sites/"+url.PathEscape(g.prop)+"/searchAnalytics/query", map[string]any{
			"startDate": start, "endDate": end, "dimensions": []string{dim},
			"dataState": "final", "type": "web", "rowLimit": n, "startRow": len(rows),
		})
		if err != nil {
			return nil, err
		}
		var out struct {
			Rows []struct {
				Keys        []string `json:"keys"`
				Clicks      float64  `json:"clicks"`
				Impressions float64  `json:"impressions"`
				CTR         float64  `json:"ctr"`
				Position    float64  `json:"position"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, apperr.Wrap(apperr.SourceError, err, "couldn't read Search Console's answer")
		}
		for _, r := range out.Rows {
			if len(r.Keys) == 0 {
				continue
			}
			rows = append(rows, Row{Key: r.Keys[0], Clicks: r.Clicks, Impressions: r.Impressions, CTR: r.CTR, Position: r.Position})
		}
		if len(out.Rows) < n {
			break
		}
	}
	return rows, nil
}

// rows returns the current or previous window for a dimension, fetching at
// least n rows and reusing an earlier fetch that was big enough.
func (g *gscSource) rows(ctx context.Context, dim string, prev bool, n int) ([]Row, error) {
	k := fmt.Sprintf("%s|%v", dim, prev)
	if g.asked[k] >= n {
		return g.memo[k], nil
	}
	s, e := g.per.Start, g.per.End
	if prev {
		s, e = g.per.PrevStart, g.per.PrevEnd
	}
	r, err := g.query(ctx, dim, s, e, n)
	if err != nil {
		return nil, err
	}
	g.memo[k], g.asked[k] = r, n
	return r, nil
}

func head[T any](r []T, n int) []T {
	if n >= 0 && len(r) > n {
		r = r[:n]
	}
	return r
}

// analysisRows is how many rows drops and almost read per window: enough to
// see the long tail without a heavy load on the API.
const analysisRows = 5000

// ---- analysis shared by Search Console and Bing rows

// Drop is a page or query that earned fewer clicks than in the previous window.
type Drop struct {
	Key             string   `json:"key"`
	Clicks          float64  `json:"clicks"`
	PrevClicks      float64  `json:"prev_clicks"`
	ClicksDelta     float64  `json:"clicks_delta"`
	Impressions     float64  `json:"impressions"`
	PrevImpressions float64  `json:"prev_impressions"`
	Position        float64  `json:"position"`
	PrevPosition    float64  `json:"prev_position"`
	PositionDelta   *float64 `json:"position_delta"` // positive means it moved down; null when absent now
}

// Default thresholds for drops and almost.
const (
	DefaultMinDropImpressions = 10
	DefaultMinAlmostImpr      = 50
	AlmostFrom, AlmostTo      = 8.0, 20.0
)

func round(v float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	if v < 0 {
		return -float64(int(-v*p+0.5)) / p
	}
	return float64(int(v*p+0.5)) / p
}

// computeDrops compares the two windows. Only keys with at least minImpr
// impressions in the previous window count, so noise from tiny keys is left
// out; a key missing from the current window lost all its clicks.
func computeDrops(cur, prev []Row, minImpr float64, limit int) []Drop {
	curBy := make(map[string]Row, len(cur))
	for _, r := range cur {
		curBy[r.Key] = r
	}
	out := []Drop{}
	for _, p := range prev {
		if p.Impressions < minImpr {
			continue
		}
		c, had := curBy[p.Key]
		delta := c.Clicks - p.Clicks
		if delta >= 0 {
			continue
		}
		d := Drop{Key: p.Key, Clicks: c.Clicks, PrevClicks: p.Clicks, ClicksDelta: round(delta, 2),
			Impressions: c.Impressions, PrevImpressions: p.Impressions, Position: round(c.Position, 2), PrevPosition: round(p.Position, 2)}
		if had && c.Position > 0 && p.Position > 0 {
			pd := round(c.Position-p.Position, 2)
			d.PositionDelta = &pd
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ClicksDelta != out[j].ClicksDelta {
			return out[i].ClicksDelta < out[j].ClicksDelta
		}
		return out[i].PrevImpressions > out[j].PrevImpressions
	})
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// computeAlmost keeps queries whose average position is 8-20 with enough
// impressions, most impressions first: the page-one opportunities.
func computeAlmost(rows []Row, minImpr float64, limit int) []Row {
	out := []Row{}
	for _, r := range rows {
		if r.Position >= AlmostFrom && r.Position <= AlmostTo && r.Impressions >= minImpr {
			r.Position = round(r.Position, 2)
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Impressions > out[j].Impressions })
	return head(out, limit)
}

func tidy(rows []Row) []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		r.CTR, r.Position = round(r.CTR, 4), round(r.Position, 2)
		out[i] = r
	}
	return out
}
