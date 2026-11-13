package mine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/web"
)

// bingBase is the JSON API. The SOAP and POX endpoints were retired on
// 2026-08-31; the JSON methods take the same key and quotas.
func bingBase() string { return web.BaseURL("bing", "https://ssl.bing.com/webmaster/api.svc/json") }

// BingLinks is what Bing reports as inbound links to the site's pages.
type BingLinks struct {
	TotalLinks     float64    `json:"total_inbound_links"`
	PagesWithLinks int        `json:"pages_with_links"`
	TotalPages     int        `json:"total_pages"`
	Top            []LinkPage `json:"top"`
	Note           string     `json:"note,omitempty"`
}

// LinkPage is one page of the site and how many inbound links Bing counts for it.
type LinkPage struct {
	URL   string  `json:"url"`
	Links float64 `json:"links"`
}

// KeywordPoint is one point of Bing's keyword history.
type KeywordPoint struct {
	Date        string  `json:"date"`
	Impressions float64 `json:"impressions"`
	Broad       float64 `json:"broad_impressions"`
}

type bingSource struct {
	key  string
	site string // the Bing site URL, as Bing lists it
	per  period
	raw  map[string][]bingStat
}

// bingStat is one dated row of GetQueryStats or GetPageStats.
type bingStat struct {
	Key         string
	Date        time.Time
	Clicks      float64
	Impressions float64
	Position    float64
}

var bingDateRe = regexp.MustCompile(`/Date\((-?\d+)`)

// parseBingDate reads Bing's "/Date(1700000000000-0800)/" values.
func parseBingDate(s string) time.Time {
	if m := bingDateRe.FindStringSubmatch(s); m != nil {
		if ms, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.UnixMilli(ms).UTC()
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// bingCall calls one JSON method and returns the "d" payload.
func bingCall(ctx context.Context, key, method string, args url.Values) (json.RawMessage, error) {
	q := url.Values{"apikey": {key}}
	for k, v := range args {
		q[k] = v
	}
	resp, err := web.Get(ctx, bingBase()+"/"+method+"?"+q.Encode(), 0)
	if err != nil {
		return nil, err
	}
	var env struct {
		D         json.RawMessage `json:"d"`
		ErrorCode *int            `json:"ErrorCode"`
		Message   string          `json:"Message"`
	}
	_ = json.Unmarshal(resp.Body, &env)
	if resp.Status/100 != 2 || env.ErrorCode != nil {
		msg := env.Message
		if msg == "" {
			msg = http.StatusText(resp.Status)
		}
		low := strings.ToLower(msg)
		switch {
		case resp.Status == 401 || resp.Status == 403 || strings.Contains(low, "api key") || strings.Contains(low, "apikey") || strings.Contains(low, "notauthorized"):
			return nil, apperr.New(apperr.Forbidden, "Bing Webmaster refused the key or site (%s); check `seox key set bing`", msg)
		case resp.Status == 429:
			return nil, apperr.New(apperr.QuotaExceeded, "Bing Webmaster quota used up: %s", msg)
		}
		return nil, apperr.New(apperr.SourceError, "Bing Webmaster %s answered %d: %s", method, resp.Status, msg)
	}
	if env.D == nil {
		return nil, apperr.New(apperr.SourceError, "Bing Webmaster %s answered with no data", method)
	}
	return env.D, nil
}

// bingSites is GetUserSites.
func bingSites(ctx context.Context, key string) ([]struct {
	URL        string `json:"Url"`
	IsVerified bool   `json:"IsVerified"`
}, error) {
	d, err := bingCall(ctx, key, "GetUserSites", nil)
	if err != nil {
		return nil, err
	}
	var sites []struct {
		URL        string `json:"Url"`
		IsVerified bool   `json:"IsVerified"`
	}
	if err := json.Unmarshal(d, &sites); err != nil {
		return nil, apperr.Wrap(apperr.SourceError, err, "couldn't read Bing's site list")
	}
	return sites, nil
}

func newBing(ctx context.Context, site string, days int) (*bingSource, error) {
	key, _ := config.Key("bing")
	if key == "" {
		return nil, apperr.New(apperr.NeedsAPIKey, "no Bing key; run `seox key set bing`")
	}
	host := siteHost(site)
	if host == "" {
		return nil, apperr.New(apperr.InvalidInput, "%q is not a site or domain", site)
	}
	sites, err := bingSites(ctx, key)
	if err != nil {
		return nil, err
	}
	best, bestScore := "", -1
	for _, s := range sites {
		u, err := url.Parse(s.URL)
		if err != nil || strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") != host {
			continue
		}
		score := 500 - len(u.Path)
		if u.Scheme == "https" {
			score += 50
		}
		if s.IsVerified {
			score += 1000
		}
		if score > bestScore {
			best, bestScore = s.URL, score
		}
	}
	if best == "" {
		return nil, notVerified("no site for %s in your Bing Webmaster account; see `seox setup verify %s`", host, host)
	}
	return &bingSource{key: key, site: best, per: makePeriod(days), raw: map[string][]bingStat{}}, nil
}

// stats fetches GetQueryStats or GetPageStats: one dated row per query or page.
func (b *bingSource) stats(ctx context.Context, method string) ([]bingStat, error) {
	if s, ok := b.raw[method]; ok {
		return s, nil
	}
	d, err := bingCall(ctx, b.key, method, url.Values{"siteUrl": {b.site}})
	if err != nil {
		return nil, err
	}
	var in []struct {
		Query      string  `json:"Query"`
		Date       string  `json:"Date"`
		Clicks     float64 `json:"Clicks"`
		Impr       float64 `json:"Impressions"`
		AvgImprPos float64 `json:"AvgImpressionPosition"`
	}
	if err := json.Unmarshal(d, &in); err != nil {
		return nil, apperr.Wrap(apperr.SourceError, err, "couldn't read Bing's %s answer", method)
	}
	out := make([]bingStat, 0, len(in))
	for _, r := range in {
		out = append(out, bingStat{Key: r.Query, Date: parseBingDate(r.Date), Clicks: r.Clicks, Impressions: r.Impr, Position: r.AvgImprPos})
	}
	b.raw[method] = out
	return out, nil
}

// window adds up the dated rows between two days into one row per key. The
// position is weighted by impressions. Bing reports by week, so a window holds
// the weekly points that start inside it.
func window(stats []bingStat, from, to time.Time) []Row {
	type acc struct{ clicks, impr, posW float64 }
	by := map[string]*acc{}
	var order []string
	end := to.Add(24*time.Hour - time.Nanosecond)
	for _, s := range stats {
		if s.Date.Before(from) || s.Date.After(end) {
			continue
		}
		a := by[s.Key]
		if a == nil {
			a = &acc{}
			by[s.Key] = a
			order = append(order, s.Key)
		}
		a.clicks += s.Clicks
		a.impr += s.Impressions
		a.posW += s.Position * s.Impressions
	}
	rows := make([]Row, 0, len(order))
	for _, k := range order {
		a := by[k]
		r := Row{Key: k, Clicks: a.clicks, Impressions: a.impr}
		if a.impr > 0 {
			r.CTR, r.Position = a.clicks/a.impr, a.posW/a.impr
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Clicks != rows[j].Clicks {
			return rows[i].Clicks > rows[j].Clicks
		}
		return rows[i].Impressions > rows[j].Impressions
	})
	return rows
}

// rows is the current or previous window of query or page rows.
func (b *bingSource) rows(ctx context.Context, method string, prev bool) ([]Row, error) {
	st, err := b.stats(ctx, method)
	if err != nil {
		return nil, err
	}
	if prev {
		return window(st, b.per.pS, b.per.pE), nil
	}
	return window(st, b.per.startT, b.per.endT), nil
}

// links is GetLinkCounts for the first page of results: the site's pages with
// inbound links, most links first.
func (b *bingSource) links(ctx context.Context, limit int) (*BingLinks, error) {
	d, err := bingCall(ctx, b.key, "GetLinkCounts", url.Values{"siteUrl": {b.site}, "page": {"0"}})
	if err != nil {
		return nil, err
	}
	var in struct {
		Links []struct {
			Count float64 `json:"Count"`
			URL   string  `json:"Url"`
		} `json:"Links"`
		TotalPages int `json:"TotalPages"`
	}
	if err := json.Unmarshal(d, &in); err != nil {
		return nil, apperr.Wrap(apperr.SourceError, err, "couldn't read Bing's link counts")
	}
	out := &BingLinks{TotalPages: max(in.TotalPages, 1), PagesWithLinks: len(in.Links)}
	pages := make([]LinkPage, 0, len(in.Links))
	for _, l := range in.Links {
		out.TotalLinks += l.Count
		pages = append(pages, LinkPage{URL: l.URL, Links: l.Count})
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Links > pages[j].Links })
	out.Top = head(pages, limit)
	if in.TotalPages > 1 {
		out.Note = "Bing has more link pages; these totals cover the first page only"
	}
	return out, nil
}

// KeywordStats is Bing's impression history for one keyword.
type KeywordStats struct {
	Keyword  string         `json:"keyword"`
	Country  string         `json:"country"`
	Language string         `json:"language"`
	From     string         `json:"from,omitempty"` // first and last day Bing returned
	To       string         `json:"to,omitempty"`
	Note     string         `json:"note"`
	Points   []KeywordPoint `json:"points"`
}

// BingKeywordStats returns Bing's impression history for one keyword
// (GetKeywordStats(q, country, language)). The method takes no dates, so the
// result is whatever history Bing keeps, not a --days window; From and To say
// what it covers. It needs only the key: the data is Bing-wide, not the site's.
func BingKeywordStats(ctx context.Context, keyword, country, language string) (*KeywordStats, error) {
	key, _ := config.Key("bing")
	if key == "" {
		return nil, apperr.New(apperr.NeedsAPIKey, "no Bing key; run `seox key set bing`")
	}
	if strings.TrimSpace(keyword) == "" {
		return nil, apperr.New(apperr.InvalidInput, "keyword is empty")
	}
	if country == "" {
		country = "us"
	}
	if language == "" {
		language = "en-US"
	}
	d, err := bingCall(ctx, key, "GetKeywordStats", url.Values{"q": {keyword}, "country": {country}, "language": {language}})
	if err != nil {
		return nil, err
	}
	var in []struct {
		Date  string  `json:"Date"`
		Impr  float64 `json:"Impressions"`
		Broad float64 `json:"BroadImpressions"`
	}
	if err := json.Unmarshal(d, &in); err != nil {
		return nil, apperr.Wrap(apperr.SourceError, err, "couldn't read Bing's keyword stats")
	}
	out := &KeywordStats{Keyword: keyword, Country: country, Language: language, Points: []KeywordPoint{},
		Note: "Bing's keyword history is not limited by --days; from and to show the period it returned"}
	for _, r := range in {
		out.Points = append(out.Points, KeywordPoint{Date: parseBingDate(r.Date).Format("2006-01-02"), Impressions: r.Impr, Broad: r.Broad})
	}
	sort.SliceStable(out.Points, func(i, j int) bool { return out.Points[i].Date < out.Points[j].Date })
	if n := len(out.Points); n > 0 {
		out.From, out.To = out.Points[0].Date, out.Points[n-1].Date
	}
	return out, nil
}
