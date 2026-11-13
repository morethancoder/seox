package site

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Field weights: where a term appears says how much the page means it.
const (
	wTitle       = 5.0
	wH1          = 4.0
	wH23         = 2.0
	wDescription = 2.0
	wAnchor      = 1.5
	wBody        = 1.0
)

var englishStop = wordSet(`a about above after again against all also am an and any are aren as at be because been before being
below between both but by can cannot could couldn did didn do does doesn doing don down during each few for from further had hadn has
hasn have haven having he her here hers herself him himself his how i if in into is isn it its itself just let ll me more most mustn
my myself no nor not now of off on once only or other ought our ours ourselves out over own re same shan she should shouldn so some
such than that the their theirs them themselves then there these they this those through to too under until up ve very was wasn we
were weren what when where which while who whom why will with won would wouldn you your yours yourself yourselves s t d m o y get
got us use used using may might many much one two like via per etc ever every even still yet however also can new`)

// arabicStop is a small list of the most frequent Arabic function words.
var arabicStop = wordSet(`في من على إلى الى عن مع هذا هذه ذلك تلك هو هي هم هن أنا نحن أنت انت أن ان إن أو او ثم لكن لا ما لم لن قد كل
بعض غير بين حتى إذا اذا كان كانت يكون تكون التي الذي الذين اللذين هناك هنا كما أي اي عند عندما بعد قبل منذ خلال فقط أيضا ايضا
بل إلا الا لقد وقد به بها له لها لهم فيه فيها منه منها عليه عليها إليه اليه و ب ل ك`)

// russianStop is a list of the most frequent Russian function words.
var russianStop = wordSet(`и в во не что он на я с со как а то все она так его но да ты к у же вы за бы по только ее мне было вот от
меня еще нет о из ему теперь когда даже ну вдруг ли если уже или ни быть был него до вас нибудь опять уж вам ведь там потом себя ничего
ей может они тут где есть надо ней для мы тебя их чем была сам чтоб без будто чего раз тоже себе под будет ж тогда кто этот того потому
этого какой совсем ним здесь этом один почти мой тем чтобы нее сейчас были куда зачем всех никогда можно при наконец два об другой хоть
после над больше тот через эти нас про всего них какая много разве три эту моя впрочем свою этой перед иногда лучше чуть том нельзя
такой им более всегда конечно всю между это эта эти этих также либо ваш ваша ваше ваши наш наша наше наши свой своя свои мои твой
который которая которое которые которых которым которой всё ещё чём всем нам вас вами ими нами`)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[normalizeWord(w)] = true
	}
	return m
}

func isStop(w string) bool { return englishStop[w] || arabicStop[w] || russianStop[w] }

// normalizeWord lowercases and strips Arabic diacritics and tatweel so the
// same word spelled with and without vowel marks counts once.
func normalizeWord(w string) string {
	w = strings.ToLower(w)
	return strings.Map(func(r rune) rune {
		if (r >= 0x064B && r <= 0x0652) || r == 0x0640 {
			return -1
		}
		return r
	}, w)
}

// stripMarks removes Arabic diacritics and tatweel but keeps the case, which
// the keyword filter needs to spot all-caps words such as SEO.
func stripMarks(w string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 0x064B && r <= 0x0652) || r == 0x0640 {
			return -1
		}
		return r
	}, w)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// segments splits text into runs of words. A sentence break (punctuation or a
// line break) ends a run so that n-grams never span it.
func segments(s string) [][]string {
	var out [][]string
	var cur []string
	var word []rune
	flushWord := func() {
		if len(word) > 0 {
			cur = append(cur, stripMarks(string(word)))
			word = word[:0]
		}
	}
	flushSeg := func() {
		flushWord()
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}
	for _, r := range s {
		switch {
		case isWordRune(r):
			word = append(word, r)
		case strings.ContainsRune(".!?;:,|()[]{}\"“”«»—–·•\n\r\t…؟،", r):
			flushSeg()
		default: // space, hyphen, apostrophe, slash and the like end a word only
			flushWord()
		}
	}
	flushSeg()
	return out
}

// splitWords is every word of a text, used for the word count.
func splitWords(s string) []string {
	var out []string
	for _, seg := range segments(s) {
		out = append(out, seg...)
	}
	return out
}

func sortStrings(s []string) { sort.Strings(s) }

type termAcc struct {
	score float64
	in    map[string]bool
}

type termCounter struct{ m map[string]*termAcc }

func newTermCounter() *termCounter { return &termCounter{m: map[string]*termAcc{}} }

// noiseWords are dates and interface words that top a crawl without meaning
// anything: month and day names and their abbreviations, and common UI text.
var noiseWords = wordSet(`jan feb mar apr jun jul aug sep sept oct nov dec january february march april june july august
september october november december mon tue tues wed thu thur thurs fri sat sun monday tuesday wednesday thursday friday saturday
sunday read more share menu min mins ago comment comments reply login log sign subscribe click here next previous prev home
skip search tweet`)

func isAllDigits(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func isAllCaps(w string) bool {
	letters := 0
	for _, r := range w {
		if unicode.IsLetter(r) {
			if !unicode.IsUpper(r) {
				return false
			}
			letters++
		}
	}
	return letters >= 2
}

// usefulWord says whether a raw (cased) word may start or end a term. Stopwords
// and the noise list never can. A word under 3 characters is kept only when it
// is all caps (AI, UI, SEO). A pure number is kept only inside a longer term
// ("iphone 15"), never as a term of its own (unigram says which).
func usefulWord(raw string, unigram bool) bool {
	w := normalizeWord(raw)
	if isStop(w) || noiseWords[w] {
		return false
	}
	if isAllDigits(w) {
		return !unigram
	}
	if utf8.RuneCountInString(w) < 3 && !isAllCaps(raw) {
		return false
	}
	return true
}

// lengthBoost makes phrases compete with single words: a 2-word term counts
// 1.25x and a 3-word term 1.5x, because a phrase appears less often but says more.
func lengthBoost(n int) float64 { return 1 + 0.25*float64(n-1) }

// addText adds every 1-3 word n-gram of the text with the field's weight. An
// n-gram may not start or end with a stopword, so "tips for seo" counts but
// "for seo" does not.
func (c *termCounter) addText(text string, weight float64, field string) {
	for _, seg := range segments(text) {
		for i := range seg {
			for n := 1; n <= 3 && i+n <= len(seg); n++ {
				if !usefulWord(seg[i], n == 1) || !usefulWord(seg[i+n-1], n == 1) {
					continue
				}
				words := make([]string, n)
				for k := range words {
					words[k] = normalizeWord(seg[i+k])
				}
				c.add(strings.Join(words, " "), weight*lengthBoost(n), field)
			}
		}
	}
}

func (c *termCounter) add(term string, weight float64, field string) {
	a := c.m[term]
	if a == nil {
		a = &termAcc{in: map[string]bool{}}
		c.m[term] = a
	}
	a.score += weight
	a.in[field] = true
}

var fieldOrder = []string{"title", "h1", "h2", "h3", "description", "anchor", "body"}

// top returns the n best terms, highest score first. Ties break on the term so
// results are stable between runs.
func (c *termCounter) top(n int) []Term {
	out := make([]Term, 0, len(c.m))
	for t, a := range c.m {
		term := Term{Term: t, Score: a.score}
		for _, f := range fieldOrder {
			if a.in[f] {
				term.In = append(term.In, f)
			}
		}
		out = append(out, term)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Term < out[j].Term
	})
	if len(out) > n {
		out = out[:n]
	}
	for i := range out {
		out[i].Score = round1(out[i].Score)
	}
	return out
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// TermPage is one page that targets a term.
type TermPage struct {
	URL   string  `json:"url"`
	Score float64 `json:"score"`
}

// SiteKeyword is a term aggregated across the crawl.
type SiteKeyword struct {
	Term      string     `json:"term"`
	Words     int        `json:"words"`
	Score     float64    `json:"score"`
	PageCount int        `json:"page_count"`
	In        []string   `json:"in,omitempty"`
	Pages     []TermPage `json:"pages"` // best first, at most 5
}

// aggregateKeywords adds up each page's weighted terms. A page "targets" a term
// when the term is among that page's top terms (the crawl keeps the top 40).
func aggregateKeywords(pages []*Page, limit int) []SiteKeyword {
	type acc struct {
		score float64
		in    map[string]bool
		pages []TermPage
	}
	m := map[string]*acc{}
	for _, p := range pages {
		for _, t := range p.Terms {
			a := m[t.Term]
			if a == nil {
				a = &acc{in: map[string]bool{}}
				m[t.Term] = a
			}
			a.score += t.Score
			for _, f := range t.In {
				a.in[f] = true
			}
			a.pages = append(a.pages, TermPage{URL: p.URL, Score: t.Score})
		}
	}
	out := make([]SiteKeyword, 0, len(m))
	for t, a := range m {
		sort.Slice(a.pages, func(i, j int) bool {
			if a.pages[i].Score != a.pages[j].Score {
				return a.pages[i].Score > a.pages[j].Score
			}
			return a.pages[i].URL < a.pages[j].URL
		})
		k := SiteKeyword{Term: t, Words: len(strings.Fields(t)), Score: round1(a.score), PageCount: len(a.pages)}
		for _, f := range fieldOrder {
			if a.in[f] {
				k.In = append(k.In, f)
			}
		}
		k.Pages = a.pages
		if len(k.Pages) > 5 {
			k.Pages = k.Pages[:5]
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Term < out[j].Term
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
