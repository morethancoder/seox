package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
)

func fast(t *testing.T) {
	t.Helper()
	t.Cleanup(SetMinGap(0))
	old := retryWait
	retryWait = time.Millisecond
	t.Cleanup(func() { retryWait = old })
}

func TestGetSendsUserAgentAndCapsBody(t *testing.T) {
	fast(t)
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		io.WriteString(w, strings.Repeat("a", 1000))
	}))
	defer srv.Close()
	resp, err := Get(context.Background(), srv.URL, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Body) != 100 || !resp.Truncated || resp.Status != 200 {
		t.Fatalf("body %d truncated %v status %d", len(resp.Body), resp.Truncated, resp.Status)
	}
	if !strings.HasPrefix(ua, "seox/") || !strings.Contains(ua, "(+https://github.com/morethancoder/seox)") {
		t.Fatalf("user agent %q", ua)
	}
}

func TestRetriesOnceOn5xxAnd429(t *testing.T) {
	fast(t)
	for _, code := range []int{503, 429} {
		var n int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&n, 1) == 1 {
				w.WriteHeader(code)
				return
			}
			io.WriteString(w, "ok")
		}))
		resp, err := Get(context.Background(), srv.URL, 0)
		srv.Close()
		if err != nil || string(resp.Body) != "ok" || n != 2 {
			t.Fatalf("code %d: %v %v calls %d", code, resp, err, n)
		}
	}
	// A second failure is returned as is, with no third try.
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	resp, err := Get(context.Background(), srv.URL, 0)
	if err != nil || resp.Status != 500 || n != 2 {
		t.Fatalf("%v %v calls %d", resp, err, n)
	}
}

func TestRetryReplaysPostBody(t *testing.T) {
	fast(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.WriteHeader(502)
		}
	}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("hello"))
	if _, err := Do(context.Background(), req, 0); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[1] != "hello" {
		t.Fatalf("bodies %v", bodies)
	}
}

func TestRedirectLimitAndNoRedirect(t *testing.T) {
	fast(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()
	_, err := Get(context.Background(), srv.URL, 0)
	if e := apperr.From(err); e.Code != apperr.NetworkError || !strings.Contains(e.Message, "redirects") {
		t.Fatalf("want network_error about redirects, got %v", err)
	}
	resp, err := GetNoRedirect(context.Background(), srv.URL, 0)
	if err != nil || resp.Status != 302 || resp.Header.Get("Location") != "/next" {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestFollowsRedirectsAndReportsFinalURL(t *testing.T) {
	fast(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
			return
		}
		io.WriteString(w, "done")
	}))
	defer srv.Close()
	resp, err := Get(context.Background(), srv.URL, 0)
	if err != nil || !strings.HasSuffix(resp.URL, "/final") || string(resp.Body) != "done" {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestContextMapsToTimeout(t *testing.T) {
	fast(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Get(ctx, srv.URL, 0)
	if e := apperr.From(err); e.Code != apperr.Timeout {
		t.Fatalf("deadline -> %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = Get(ctx2, srv.URL, 0)
	if e := apperr.From(err); e.Code != apperr.Timeout {
		t.Fatalf("cancel -> %v", err)
	}
}

func TestConnectionRefusedIsNetworkError(t *testing.T) {
	fast(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	_, err := Get(context.Background(), u, 0)
	if e := apperr.From(err); e.Code != apperr.NetworkError {
		t.Fatalf("got %v", err)
	}
}

func TestBadURLIsInvalidInput(t *testing.T) {
	_, err := Get(context.Background(), "http://a b/", 0)
	if e := apperr.From(err); e.Code != apperr.InvalidInput {
		t.Fatalf("got %v", err)
	}
}

func TestPerHostConcurrencyLimit(t *testing.T) {
	fast(t)
	var cur, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
	}))
	defer srv.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = Get(context.Background(), srv.URL, 0) }()
	}
	wg.Wait()
	if peak > MaxConcurrent || peak < 2 {
		t.Fatalf("peak concurrency %d, want 2", peak)
	}
}

func TestMinGapAndCrawlDelay(t *testing.T) {
	old := retryWait
	retryWait = time.Millisecond
	defer func() { retryWait = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// Default gap: 4 sequential requests take at least 3 gaps.
	defer SetMinGap(60 * time.Millisecond)()
	start := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := Get(context.Background(), srv.URL, 0); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 170*time.Millisecond {
		t.Fatalf("4 requests took %v, gap not kept", d)
	}

	// A larger Crawl-delay wins; a smaller one never drops the gap below the default.
	SetCrawlDelay(host, 120*time.Millisecond)
	defer SetCrawlDelay(host, 0)
	start = time.Now()
	for i := 0; i < 3; i++ {
		_, _ = Get(context.Background(), srv.URL, 0)
	}
	if d := time.Since(start); d < 230*time.Millisecond {
		t.Fatalf("crawl-delay not kept: %v", d)
	}
}

func TestWaitForGapIsCancellable(t *testing.T) {
	defer SetMinGap(time.Hour)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if _, err := Get(context.Background(), srv.URL, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := Get(ctx, srv.URL, 0)
	if e := apperr.From(err); e.Code != apperr.Timeout || !errors.Is(err, context.DeadlineExceeded) && e.Code != apperr.Timeout {
		t.Fatalf("got %v", err)
	}
}

func TestBaseURLAndRewriteHooks(t *testing.T) {
	fast(t)
	if BaseURL("x", "https://real") != "https://real" {
		t.Fatal("default")
	}
	restore := OverrideBase("x", "http://test")
	if BaseURL("x", "https://real") != "http://test" {
		t.Fatal("override")
	}
	restore()
	if BaseURL("x", "https://real") != "https://real" {
		t.Fatal("restore")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Host) }))
	defer srv.Close()
	defer RewriteHost("example.test", strings.TrimPrefix(srv.URL, "http://"))()
	resp, err := Get(context.Background(), "https://example.test/page", 0)
	if err != nil || string(resp.Body) != strings.TrimPrefix(srv.URL, "http://") && string(resp.Body) != "example.test" {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestErrorsRedactSecretQueryParams(t *testing.T) {
	defer SetMinGap(0)()
	// Port 1 refuses the connection, so the transport error holds the full URL.
	_, err := Get(context.Background(), "http://127.0.0.1:1/x?apikey=SECRET1&q=a&Key=SECRET2&access_token=SECRET3", 0)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, s := range []string{"SECRET1", "SECRET2", "SECRET3"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %s: %v", s, err)
		}
	}
	if !strings.Contains(err.Error(), "q=a") {
		t.Errorf("other params should stay: %v", err)
	}
	if got := RedactURL(`Get "https://h/p?x=1&APIKEY=abc"`); strings.Contains(got, "abc") {
		t.Errorf("got %s", got)
	}
}
