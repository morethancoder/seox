package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/web"
)

func TestLocalLighthouseNeverOverlaps(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	// mkdir is atomic: a failing mkdir means another run is inside the script at the same time.
	body := fmt.Sprintf("if mkdir %[1]s/busy 2>/dev/null; then sleep 0.3; rmdir %[1]s/busy; else echo overlap >> %[1]s/overlaps; fi\n%[2]s", dir, catFixture(t))
	fakeLighthouse(t, body)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runLocalLighthouse(context.Background(), "https://example.com", "mobile"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b, _ := os.ReadFile(filepath.Join(dir, "overlaps")); len(b) > 0 {
		t.Fatalf("local Lighthouse runs overlapped %d times", strings.Count(string(b), "overlap"))
	}
}

func TestLighthouseLockWaitRespectsCtx(t *testing.T) {
	setup(t)
	unlock, err := lockLighthouse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = lockLighthouse(ctx)
	if apperr.From(err).Code != apperr.Timeout || time.Since(start) > 3*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

func TestLighthouseChildEnvAndCwd(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_BING_KEY", "BINGSECRET")
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	fakeLighthouse(t, fmt.Sprintf("echo \"seoxenv=$(env | grep -c '^SEOX_')\" > %s; pwd >> %s\n%s", rec, rec, catFixture(t)))
	cwd, _ := os.Getwd()
	if _, err := runLocalLighthouse(context.Background(), "https://example.com", "mobile"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(rec)
	if !strings.Contains(string(b), "seoxenv=0") {
		t.Errorf("SEOX_* leaked: %s", b)
	}
	if strings.Contains(string(b), cwd) {
		t.Errorf("ran in the current folder: %s", b)
	}
}

func TestSpeedKeyedOneStrategyFailsFallbackMissingKeepsOther(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_GOOGLE_API_KEY", "K")
	t.Setenv("SEOX_LIGHTHOUSE", "/bin/true")
	t.Setenv("CHROME_PATH", "/nonexistent/chrome")
	psi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("strategy") == "desktop" {
			w.WriteHeader(500)
			return
		}
		w.Write(fixture(t, "psi.json"))
	}))
	defer psi.Close()
	crux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer crux.Close()
	defer web.OverrideBase("psi", psi.URL)()
	defer web.OverrideBase("crux", crux.URL)()
	res, err := Speed(context.Background(), "https://example.com", SpeedOptions{Strategy: "both"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mobile == nil || res.Desktop != nil {
		t.Fatalf("mobile %v desktop %v", res.Mobile, res.Desktop)
	}
	w := strings.Join(res.Warnings, " ")
	if !strings.Contains(w, "desktop") || !strings.Contains(w, "local Lighthouse") {
		t.Errorf("warnings %v", res.Warnings)
	}
}

func TestOpportunityCLSNotMilliseconds(t *testing.T) {
	var raw psiResponse
	js := `{"lighthouseResult":{"audits":{
	 "cls-culprits-insight":{"title":"CLS","score":0,"metricSavings":{"CLS":0.31}},
	 "tbt-only":{"title":"TBT","score":0.2,"metricSavings":{"TBT":50}},
	 "lcp":{"title":"LCP","score":0.1,"metricSavings":{"LCP":900,"CLS":0.1}}}}}`
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		t.Fatal(err)
	}
	r := parsePSI(&raw, "mobile")
	got := map[string]Opportunity{}
	for _, o := range r.Opportunities {
		got[o.ID] = o
	}
	if c := got["cls-culprits-insight"]; c.SavingsMs != 0 || c.SavingsCLS != 0.31 {
		t.Errorf("cls opp %+v", c)
	}
	if l := got["lcp"]; l.SavingsMs != 900 {
		t.Errorf("lcp opp %+v", l)
	}
	if r.Opportunities[0].ID != "lcp" {
		t.Errorf("order %+v", r.Opportunities)
	}
}

func TestSpeedNoCruxAnswerIsCached(t *testing.T) {
	setup(t)
	t.Setenv("SEOX_GOOGLE_API_KEY", "K")
	var calls int32
	psi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(fixture(t, "psi.json"))
	}))
	defer psi.Close()
	crux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer crux.Close()
	defer web.OverrideBase("psi", psi.URL)()
	defer web.OverrideBase("crux", crux.URL)()
	for i := 0; i < 2; i++ {
		if _, err := Speed(context.Background(), "https://example.com", SpeedOptions{Strategy: "mobile"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("PSI called %d times, want 1 (no-CrUX answer cached)", calls)
	}
}

func TestTailWriterKeepsLast(t *testing.T) {
	w := &tailWriter{max: 4}
	w.Write([]byte("abc"))
	w.Write([]byte("defg"))
	if n, _ := w.Write([]byte("h")); n != 1 {
		t.Fatal("short write")
	}
	if got := w.String(); got != "efgh" {
		t.Fatalf("got %q", got)
	}
}

func TestProfilePatternExact(t *testing.T) {
	re := regexp.MustCompile(profilePattern("/tmp/seox-chrome-12345678"))
	if !re.MatchString("chrome --headless --user-data-dir=/tmp/seox-chrome-12345678 --foo") ||
		!re.MatchString("chrome --user-data-dir=/tmp/seox-chrome-12345678") {
		t.Fatal("should match own profile")
	}
	if re.MatchString("chrome --user-data-dir=/tmp/seox-chrome-123456789 --foo") {
		t.Fatal("must not match a longer name")
	}
	if re.MatchString("chrome --user-data-dir=/tmp/seox-chromeX12345678") {
		t.Fatal("dot-like metacharacters must be escaped")
	}
}
