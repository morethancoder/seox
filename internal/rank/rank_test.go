package rank

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
)

// fakeSearx serves recorded SearXNG JSON: testdata/rank/<name>_<pageno>.json,
// where name comes from the query.
func fakeSearx(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	names := map[string]string{"example domain": "example_domain", "nothing here": "no_match", "blog tips": "sub"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("OK"))
			return
		}
		hits.Add(1)
		q := r.URL.Query()
		if q.Get("format") != "json" {
			http.Error(w, "json only", 403)
			return
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "rank", names[q.Get("q")]+"_"+q.Get("pageno")+".json"))
		if err != nil {
			_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[]}`))
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(t *testing.T, srvURL string) Options {
	t.Helper()
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	return Options{Config: &config.Config{SearxngURL: srvURL}, Gap: -1, Depth: 3}
}

func TestPositions(t *testing.T) {
	var hits atomic.Int32
	srv := fakeSearx(t, &hits)
	opt := env(t, srv.URL)
	res, err := Positions(context.Background(), "https://example.com", []string{"example domain", "nothing here", "blog tips"}, opt)
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := res.Keywords[0], res.Keywords[1], res.Keywords[2]

	// Found on page 2 as the 5th merged result; the www host counts as the apex.
	if a.BestPosition == nil || *a.BestPosition != 5 || a.URL != "https://www.example.com/page" {
		t.Fatalf("a = %+v", a)
	}
	// Page 1 filled bing to 2 and google to 2, so page 2's 1 and 3 become 3 and 5.
	if want := map[string]int{"bing": 3, "google": 5}; !reflect.DeepEqual(a.ByEngine, want) {
		t.Fatalf("by_engine = %v", a.ByEngine)
	}
	if !a.Google || a.Label != "Google" {
		t.Fatalf("google flag: %+v", a)
	}

	// Not found: null position and the honest note.
	if b.BestPosition != nil || b.Note != "not in top 3" || b.Google || b.Label != "not Google" {
		t.Fatalf("b = %+v", b)
	}

	// A subdomain matches; no Google means "not Google".
	if c.BestPosition == nil || *c.BestPosition != 1 || c.Google || c.Label != "not Google" || c.ByEngine["duckduckgo"] != 4 {
		t.Fatalf("c = %+v", c)
	}

	// brave and qwant, each listed once however many pages reported them.
	want := []string{"engine brave did not answer (too many requests)", "engine qwant did not answer (CAPTCHA)"}
	if !reflect.DeepEqual(res.Warnings, want) {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestPositionsCacheAndNoCache(t *testing.T) {
	var hits atomic.Int32
	srv := fakeSearx(t, &hits)
	opt := env(t, srv.URL)
	if _, err := Positions(context.Background(), "example.com", []string{"example domain"}, opt); err != nil {
		t.Fatal(err)
	}
	n := hits.Load()
	res, err := Positions(context.Background(), "example.com", []string{"example domain"}, opt)
	if err != nil || !res.Keywords[0].Cached || hits.Load() != n {
		t.Fatalf("expected a cache hit: %v %+v hits %d->%d", err, res.Keywords[0], n, hits.Load())
	}
	// The cached answer keeps its warnings.
	if len(res.Warnings) == 0 {
		t.Fatal("cached answer lost its warnings")
	}
	opt.NoCache = true
	if _, err = Positions(context.Background(), "example.com", []string{"example domain"}, opt); err != nil || hits.Load() == n {
		t.Fatalf("no-cache must query again: %v", err)
	}
	// A different depth is a different cache entry.
	opt.NoCache, opt.Depth = false, 1
	res, _ = Positions(context.Background(), "example.com", []string{"example domain"}, opt)
	if res.Keywords[0].Cached || res.Keywords[0].BestPosition != nil {
		t.Fatalf("depth 1 must not see page 2: %+v", res.Keywords[0])
	}
}

func TestSameSite(t *testing.T) {
	for url, want := range map[string]bool{
		"https://example.com/":      true,
		"https://www.example.com/x": true,
		"https://a.b.example.com/":  true,
		"https://notexample.com/":   false,
		"https://example.com.evil/": false,
		"https://example.org/":      false,
	} {
		if got := sameSite("example.com", url); got != want {
			t.Errorf("%s: %v", url, got)
		}
	}
	if siteHost("WWW.Example.com/path") != "example.com" {
		t.Error("siteHost")
	}
}

func TestNoSearxngIsToolMissing(t *testing.T) {
	t.Setenv("SEOX_HOME", t.TempDir())
	_, err := Open(context.Background(), &config.Config{ArshafaSearxngDir: t.TempDir()})
	if e := apperr.From(err); e.Code != apperr.ToolMissing {
		t.Fatalf("got %v", err)
	}
}

// Zero results means the engines failed, not that the site is unranked.
func TestPositionsNoResultsIsNoData(t *testing.T) {
	var hits atomic.Int32
	srv := fakeSearx(t, &hits)
	res, err := Positions(context.Background(), "https://example.com", []string{"engines down"}, env(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	k := res.Keywords[0]
	if k.BestPosition != nil || !strings.HasPrefix(k.Note, "no data") {
		t.Fatalf("k = %+v", k)
	}
	// Not cached: a second run asks the engines again.
	before := hits.Load()
	if _, err := Positions(context.Background(), "https://example.com", []string{"engines down"}, env2(t, srv.URL)); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == before {
		t.Fatal("a no-results answer was served from the cache")
	}
}

// env2 keeps the current SEOX_HOME, so the cache from an earlier call is visible.
func env2(t *testing.T, srvURL string) Options {
	t.Helper()
	return Options{Config: &config.Config{SearxngURL: srvURL}, Gap: -1, Depth: 3}
}

func TestEngineWarningMergesSuspended(t *testing.T) {
	w := newWarnSet()
	w.addEngine("brave", "too many requests")
	w.addEngine("brave", "Suspended: too many requests")
	w2 := newWarnSet()
	w2.addEngine("brave", "Suspended: too many requests")
	if len(w.list) != 1 || w2.list[0] != w.list[0] {
		t.Fatalf("got %v and %v", w.list, w2.list)
	}
}
