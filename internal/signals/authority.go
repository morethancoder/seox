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
)

const authorityTTL = 7 * 24 // hours

// AuthorityOptions are the options of Authority.
type AuthorityOptions struct {
	NoCache bool
}

// DomainAuthority is what is known about one domain.
type DomainAuthority struct {
	Domain string `json:"domain"`
	// Score is the Open PageRank (0-10); null without a key or when the domain is not in the index.
	Score            *float64 `json:"score"`
	GlobalRank       *int     `json:"global_rank,omitempty"`
	ReferringDomains *float64 `json:"referring_domains,omitempty"`
	// FirstSeen is the earliest Wayback Machine capture of the HOMEPAGE (YYYY-MM-DD); empty when never archived.
	FirstSeen string `json:"first_seen,omitempty"`
	// SnapshotsApprox is a rough size of the homepage capture list (CDX pages x captures on the first page), not a count.
	SnapshotsApprox *int `json:"snapshots_approx,omitempty"`
	WaybackPages    int  `json:"wayback_pages,omitempty"`
	Archived        bool `json:"archived"`
}

// AuthorityResult is what Authority returns.
type AuthorityResult struct {
	Domains  []DomainAuthority `json:"domains"`
	Note     string            `json:"note"`
	Warnings []string          `json:"warnings,omitempty"`
	AllCache bool              `json:"-"`
}

type oprEntry struct {
	Found    bool     `json:"found"`
	Score    *float64 `json:"score"`
	Rank     *int     `json:"rank"`
	Referral *float64 `json:"referring"`
}

type waybackEntry struct {
	FirstSeen string `json:"first_seen"`
	Pages     int    `json:"pages"`
	Approx    int    `json:"approx"`
}

func handleAuthority(ctx context.Context, in ops.Input) (*ops.Result, error) {
	res, err := Authority(ctx, in.Strings("domain"), AuthorityOptions{NoCache: in.NoCache})
	if err != nil {
		return nil, err
	}
	out := *res
	out.Warnings = nil
	m := ops.Meta{Source: "openpagerank+wayback", Cached: res.AllCache}
	if k, _ := config.Key("openpagerank"); k == "" {
		m.Source, m.SourceReason = "wayback", "no openpagerank key: score is null"
	}
	return &ops.Result{Data: &out, Meta: m, Warnings: res.Warnings}, nil
}

// NormalizeDomain reduces a domain, host or URL to a bare lowercase host without "www.".
func NormalizeDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(s, "://") {
		if p, err := url.Parse(s); err == nil {
			s = p.Host
		}
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "]") {
		s = s[:i]
	}
	return strings.TrimPrefix(strings.TrimSuffix(s, "."), "www.")
}

// Authority looks up Open PageRank (with an openpagerank key; one call per 100 domains) and the
// Wayback Machine first-seen date for each domain. Results keep the order given.
func Authority(ctx context.Context, domains []string, opt AuthorityOptions) (*AuthorityResult, error) {
	var list []string
	seen := map[string]bool{}
	for _, d := range domains {
		d = NormalizeDomain(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		list = append(list, d)
	}
	if len(list) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "give at least one domain")
	}
	res := &AuthorityResult{Domains: make([]DomainAuthority, len(list)),
		Note: "score is the Open PageRank (0-10). first_seen and snapshots_approx are for the homepage only (exact URL match in the Wayback CDX); snapshots_approx is a rough size, not an exact count."}
	for i, d := range list {
		res.Domains[i].Domain = d
	}
	cachedAll := true

	// Open PageRank
	key, _ := config.Key("openpagerank")
	if key == "" {
		res.Warnings = append(res.Warnings, "No openpagerank key: score is null. Fix: seox key set openpagerank (free at openpagerank.keywordseverywhere.com)")
	} else {
		var todo []int
		for i, d := range list {
			var e oprEntry
			if !opt.NoCache {
				if _, ok := cache.Get(cache.Key("opr", d), ttl(authorityTTL), &e); ok {
					applyOPR(&res.Domains[i], e)
					continue
				}
			}
			todo = append(todo, i)
		}
		for start := 0; start < len(todo); start += 100 {
			cachedAll = false
			end := start + 100
			if end > len(todo) {
				end = len(todo)
			}
			batch := todo[start:end]
			names := make([]string, len(batch))
			for j, i := range batch {
				names[j] = list[i]
			}
			got, err := queryOPR(ctx, names, key)
			if err != nil {
				if ae := apperr.From(err); ae.Code == apperr.Timeout {
					return nil, ae
				}
				res.Warnings = append(res.Warnings, "Open PageRank skipped: "+err.Error())
				continue
			}
			for _, i := range batch {
				e, ok := got[list[i]]
				if !ok {
					// Missing from the answer (for example listed as invalid): show null, do not cache.
					res.Warnings = append(res.Warnings, "Open PageRank returned no result for "+list[i])
					continue
				}
				applyOPR(&res.Domains[i], e)
				cache.Put(cache.Key("opr", list[i]), "openpagerank", e)
			}
		}
	}

	// Wayback CDX, two domains at a time (the web limiter paces the host too).
	var mu sync.Mutex
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i, d := range list {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var e waybackEntry
			hit := false
			if !opt.NoCache {
				_, hit = cache.Get(cache.Key("wayback", d), ttl(authorityTTL), &e)
			}
			if !hit {
				var err error
				if e, err = queryWayback(ctx, d); err != nil {
					mu.Lock()
					res.Warnings = append(res.Warnings, fmt.Sprintf("Wayback skipped for %s: %s", d, err.Error()))
					mu.Unlock()
					return
				}
				cache.Put(cache.Key("wayback", d), "wayback", e)
				mu.Lock()
				cachedAll = false
				mu.Unlock()
			}
			da := &res.Domains[i]
			da.Archived = e.FirstSeen != ""
			da.FirstSeen, da.WaybackPages = e.FirstSeen, e.Pages
			if da.Archived {
				n := e.Approx
				da.SnapshotsApprox = &n
			}
		}(i, d)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, apperr.From(err)
	}
	sort.Strings(res.Warnings)
	res.AllCache = cachedAll
	return res, nil
}

func applyOPR(d *DomainAuthority, e oprEntry) {
	d.Score, d.GlobalRank, d.ReferringDomains = e.Score, e.Rank, e.Referral
}

func queryOPR(ctx context.Context, domains []string, key string) (map[string]oprEntry, error) {
	base := baseURL("openpagerank", "https://openpagerank.keywordseverywhere.com/v1")
	body, _ := json.Marshal(map[string]any{"domains": domains, "include_history": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/domains/bulk", bytes.NewReader(body))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "building Open PageRank request")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	r, err := doReq(ctx, req, 4<<20)
	if err != nil {
		return nil, errRedact(err, key)
	}
	switch r.Status {
	case 401:
		return nil, apperr.New(apperr.NeedsAPIKey, "Open PageRank rejected the key (401). Fix: seox key set openpagerank")
	case 403:
		return nil, apperr.New(apperr.Forbidden, "Open PageRank refused the call (403): a free key only works from the IP address set on its account page")
	case 429:
		return nil, apperr.New(apperr.QuotaExceeded, "Open PageRank monthly or per-minute limit reached (429)")
	}
	if r.Status/100 != 2 {
		return nil, apperr.New(apperr.SourceError, "Open PageRank answered HTTP %d", r.Status)
	}
	var out struct {
		Results []struct {
			Domain   string   `json:"domain"`
			Found    bool     `json:"found"`
			Score    *float64 `json:"open_page_rank"`
			Rank     *int     `json:"rank"`
			Referral *float64 `json:"referring_domains"`
		} `json:"results"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		return nil, apperr.Wrap(apperr.SourceError, err, "unreadable Open PageRank answer")
	}
	got := map[string]oprEntry{}
	for _, x := range out.Results {
		got[NormalizeDomain(x.Domain)] = oprEntry{Found: x.Found, Score: x.Score, Rank: x.Rank, Referral: x.Referral}
	}
	return got, nil
}

// queryWayback does three cheap CDX calls: the earliest capture (limit=1), the page count and
// the size of the first page. Nothing larger than one index page is downloaded.
func queryWayback(ctx context.Context, domain string) (waybackEntry, error) {
	base := baseURL("wayback", "https://web.archive.org/cdx/search/cdx")
	q := func(extra string) (*string, error) {
		r, err := doGet(ctx, base+"?url="+url.QueryEscape(domain)+"&"+extra, 1<<20)
		if err != nil {
			return nil, err
		}
		if r.Status != 200 {
			return nil, apperr.New(apperr.SourceError, "Wayback answered HTTP %d", r.Status)
		}
		s := string(r.Body)
		return &s, nil
	}
	var e waybackEntry
	first, err := q("output=json&fl=timestamp&limit=1")
	if err != nil {
		return e, err
	}
	var rows [][]string
	if err := json.Unmarshal([]byte(*first), &rows); err != nil {
		if strings.TrimSpace(*first) == "" {
			return e, nil
		}
		return e, apperr.Wrap(apperr.SourceError, err, "unreadable Wayback answer")
	}
	if len(rows) < 2 || len(rows[1]) == 0 || len(rows[1][0]) < 8 {
		return e, nil // never archived
	}
	ts := rows[1][0]
	e.FirstSeen = ts[0:4] + "-" + ts[4:6] + "-" + ts[6:8]

	np, err := q("showNumPages=true")
	if err != nil {
		return e, err
	}
	e.Pages, _ = strconv.Atoi(strings.TrimSpace(*np))
	if e.Pages < 1 {
		e.Pages = 1
	}
	p0, err := q("fl=timestamp&page=0")
	if err != nil {
		return e, err
	}
	lines := 0
	for _, l := range strings.Split(*p0, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	e.Approx = lines * e.Pages
	return e, nil
}
