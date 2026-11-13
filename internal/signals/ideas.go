package signals

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/ops"
)

const (
	ideasTTL  = 7 * 24 // hours
	ideasNote = "No search volumes: these are suggestions people type, ranked by the engines' own order."
)

var (
	questionWords = []string{"how", "what", "why", "when", "where", "who", "which", "can", "is", "does"}
	// googleQueries: seed + a-z + 13 prefixes/suffixes = 40; bingQueries: seed + the 13 = 14. Total 54 per seed.
	expansionWords = append(append([]string{}, questionWords...), "vs", "best", "for")
)

// IdeasOptions are the options of Ideas.
type IdeasOptions struct {
	Lang    string // hl, default en
	Country string // gl, default us
	Limit   int    // most ideas per group (default 20)
	NoCache bool
}

// Idea is one suggestion with the engines that gave it and its best position in their lists.
type Idea struct {
	Text    string   `json:"text"`
	Engines []string `json:"engines"`
	Rank    int      `json:"rank"` // best position in any one engine's list (1 = first)
}

// EngineStatus says how an engine did.
type EngineStatus struct {
	Requests int    `json:"requests"`
	Failed   int    `json:"failed"`
	Error    string `json:"error,omitempty"`
}

// IdeasResult is what Ideas returns.
type IdeasResult struct {
	Keyword     string                  `json:"keyword"`
	Lang        string                  `json:"lang"`
	Country     string                  `json:"country"`
	Note        string                  `json:"note"`
	Questions   []Idea                  `json:"questions"`
	Comparisons []Idea                  `json:"comparisons"`
	Modifiers   []Idea                  `json:"modifiers"`
	Related     []Idea                  `json:"related"`
	Total       int                     `json:"total"` // before the per-group limit
	Engines     map[string]EngineStatus `json:"engines"`

	Warnings  []string `json:"warnings,omitempty"`
	FromCache bool     `json:"-"`
}

func handleIdeas(ctx context.Context, in ops.Input) (*ops.Result, error) {
	res, err := Ideas(ctx, in.Str("keyword"), IdeasOptions{Lang: in.Str("lang"), Country: in.Str("country"), Limit: in.Int("limit", 20), NoCache: in.NoCache})
	if err != nil {
		return nil, err
	}
	out := *res
	out.Warnings = nil
	return &ops.Result{Data: &out, Meta: ops.Meta{Source: "google-suggest+bing-suggest", Cached: res.FromCache}, Warnings: res.Warnings}, nil
}

// Ideas collects autocomplete suggestions for a seed from Google and Bing and groups them.
// At most about 54 requests are made per seed; the web limiter paces each host.
func Ideas(ctx context.Context, keyword string, opt IdeasOptions) (*IdeasResult, error) {
	seed := strings.Join(strings.Fields(strings.ToLower(keyword)), " ")
	if seed == "" {
		return nil, apperr.New(apperr.InvalidInput, "a keyword is required")
	}
	lang := strings.ToLower(strings.TrimSpace(opt.Lang))
	if lang == "" {
		lang = "en"
	}
	country := strings.ToLower(strings.TrimSpace(opt.Country))
	if country == "" {
		country = "us"
	}
	limit := opt.Limit
	if limit <= 0 {
		limit = 20
	}
	ck := cache.Key("ideas", seed, lang, country, "all")
	var res *IdeasResult
	if !opt.NoCache {
		var c IdeasResult
		if _, ok := cache.Get(ck, ttl(ideasTTL), &c); ok {
			c.FromCache = true
			res = &c
		}
	}
	if res == nil {
		var err error
		if res, err = collectIdeas(ctx, seed, lang, country); err != nil {
			return nil, err
		}
		partial := false
		for _, st := range res.Engines {
			partial = partial || st.Failed > 0
		}
		if !partial { // a partial answer must not stick for 7 days
			cache.Put(ck, "suggest", res)
		}
	}
	// The cache holds every group in full; the limit is applied on the way out.
	out := *res
	out.Questions, out.Comparisons = capIdeas(res.Questions, limit), capIdeas(res.Comparisons, limit)
	out.Modifiers, out.Related = capIdeas(res.Modifiers, limit), capIdeas(res.Related, limit)
	return &out, nil
}

func capIdeas(in []Idea, n int) []Idea {
	if len(in) > n {
		return in[:n]
	}
	return in
}

type hit struct {
	text string
	rank int
	eng  string
}

func collectIdeas(ctx context.Context, seed, lang, country string) (*IdeasResult, error) {
	queries := func(all bool) []string {
		qs := []string{seed}
		if all {
			for c := 'a'; c <= 'z'; c++ {
				qs = append(qs, seed+" "+string(c))
			}
		}
		for _, w := range expansionWords {
			switch w {
			case "vs", "for":
				qs = append(qs, seed+" "+w)
			default:
				qs = append(qs, w+" "+seed)
			}
		}
		return qs
	}
	type engine struct {
		name  string
		all   bool
		fetch func(context.Context, string) ([]string, error)
	}
	engines := []engine{
		{"google", true, func(c context.Context, q string) ([]string, error) { return suggest(c, googleURL(q, lang, country)) }},
		{"bing", false, func(c context.Context, q string) ([]string, error) { return suggest(c, bingURL(q, lang, country)) }},
	}
	res := &IdeasResult{Keyword: seed, Lang: lang, Country: country, Note: ideasNote, Engines: map[string]EngineStatus{}}
	hits := make([][]hit, len(engines))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, e := range engines {
		wg.Add(1)
		go func(i int, e engine) {
			defer wg.Done()
			st := EngineStatus{}
			for _, q := range queries(e.all) {
				if ctx.Err() != nil {
					break
				}
				sugg, err := e.fetch(ctx, q)
				st.Requests++
				if err != nil {
					st.Failed++
					st.Error = err.Error()
					if ae := apperr.From(err); ae.Code == apperr.QuotaExceeded || ae.Code == apperr.Forbidden || st.Failed >= 3 {
						break // blocked or unhealthy: stop asking
					}
					continue
				}
				for r, s := range sugg {
					hits[i] = append(hits[i], hit{s, r + 1, e.name})
				}
			}
			mu.Lock()
			res.Engines[e.name] = st
			mu.Unlock()
		}(i, e)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, apperr.From(err)
	}

	// Merge: keep first-seen order (Google's seed list first), remember engines and best rank.
	idx := map[string]*Idea{}
	var order []*Idea
	for _, hs := range hits {
		for _, h := range hs {
			t := strings.Join(strings.Fields(strings.ToLower(h.text)), " ")
			if t == "" || t == seed {
				continue
			}
			id, ok := idx[t]
			if !ok {
				id = &Idea{Text: t, Rank: h.rank}
				idx[t] = id
				order = append(order, id)
			}
			if h.rank < id.Rank {
				id.Rank = h.rank
			}
			if !contains(id.Engines, h.eng) {
				id.Engines = append(id.Engines, h.eng)
			}
		}
	}
	res.Questions, res.Comparisons, res.Modifiers, res.Related = []Idea{}, []Idea{}, []Idea{}, []Idea{}
	for _, id := range order {
		switch groupOf(id.Text) {
		case "questions":
			res.Questions = append(res.Questions, *id)
		case "comparisons":
			res.Comparisons = append(res.Comparisons, *id)
		case "modifiers":
			res.Modifiers = append(res.Modifiers, *id)
		default:
			res.Related = append(res.Related, *id)
		}
	}
	res.Total = len(order)
	for _, n := range []string{"google", "bing"} {
		if st := res.Engines[n]; st.Error != "" {
			res.Warnings = append(res.Warnings, n+" suggest: "+st.Error)
		}
	}
	if res.Total == 0 {
		if g, b := res.Engines["google"], res.Engines["bing"]; g.Failed > 0 || b.Failed > 0 {
			return nil, apperr.New(apperr.SourceError, "no suggestions: %s", strings.Join(res.Warnings, "; "))
		}
	}
	return res, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var (
	questionRe   = regexp.MustCompile(`^(how|what|why|when|where|who|whom|whose|which|can|could|is|are|was|were|do|does|did|will|would|should|am|have|has)\b`)
	comparisonRe = regexp.MustCompile(`\b(vs|vs\.|versus|compared to|or)\b|\balternatives?\b`)
	modifierRe   = regexp.MustCompile(`\b(best|top|free|cheap|cheapest|near me|online|tools?|template|templates|checklist|price|pricing|cost|review|reviews|for|20\d\d)\b`)
)

func groupOf(t string) string {
	switch {
	case questionRe.MatchString(t) || strings.HasSuffix(t, "?"):
		return "questions"
	case comparisonRe.MatchString(t):
		return "comparisons"
	case modifierRe.MatchString(t):
		return "modifiers"
	}
	return "related"
}

func googleURL(q, lang, country string) string {
	base := baseURL("google-suggest", "https://suggestqueries.google.com/complete/search")
	return base + "?" + url.Values{"client": {"firefox"}, "q": {q}, "hl": {lang}, "gl": {country}, "oe": {"utf-8"}}.Encode()
}

func bingURL(q, lang, country string) string {
	base := baseURL("bing-suggest", "https://api.bing.com/osjson.aspx")
	return base + "?" + url.Values{"query": {q}, "mkt": {lang + "-" + strings.ToUpper(country)}}.Encode()
}

// suggest reads the OpenSearch JSON both engines return: ["query", ["s1", "s2", ...], ...].
func suggest(ctx context.Context, u string) ([]string, error) {
	r, err := doGet(ctx, u, 1<<20)
	if err != nil {
		return nil, err
	}
	switch {
	case r.Status == 429:
		return nil, apperr.New(apperr.QuotaExceeded, "%s is rate limiting suggestions (429): try again later", hostOf(u))
	case r.Status == 403:
		return nil, apperr.New(apperr.Forbidden, "%s refused the request (403)", hostOf(u))
	case r.Status/100 != 2:
		return nil, apperr.New(apperr.SourceError, "%s answered HTTP %d", hostOf(u), r.Status)
	}
	body := r.Body
	if !utf8.Valid(body) && !strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "utf-8") {
		// Legacy single-byte charset (ISO-8859-1 family): widen each byte to a rune.
		rs := make([]rune, len(body))
		for i, c := range body {
			rs[i] = rune(c)
		}
		body = []byte(string(rs))
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil || len(arr) < 2 {
		return nil, apperr.New(apperr.SourceError, "unreadable suggestions from %s", hostOf(u))
	}
	var out []string
	if err := json.Unmarshal(arr[1], &out); err != nil {
		return nil, apperr.New(apperr.SourceError, "unreadable suggestions from %s", hostOf(u))
	}
	return out, nil
}
