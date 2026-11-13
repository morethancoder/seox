// Package web is the one shared HTTP client for everything seox fetches: sites
// it crawls and the free APIs it calls. It sets the User-Agent, caps body
// sizes, follows at most 10 redirects, retries once on 429/5xx, keeps a polite
// per-host pace and maps ctx cancel or deadline to the timeout error code.
//
// Area packages never build their own http.Client. Use Get for a plain GET,
// Do for anything else (POST, HEAD, custom headers), GetNoRedirect when the
// redirect itself is the thing you want to see, and BaseURL so tests can point
// a source at an httptest server.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
)

// Version goes into the User-Agent; the cli package sets it from its build version.
var Version = "dev"

// DefaultMaxBytes caps a body when the caller passes maxBytes <= 0.
const DefaultMaxBytes = 5 << 20

// MaxRedirects is how many redirects one request may follow.
const MaxRedirects = 10

// UserAgent is sent on every request that has none of its own.
func UserAgent() string {
	return "seox/" + Version + " (+https://github.com/morethancoder/seox)"
}

// Response is a fetched page. A non-2xx status is not an error: the crawler
// needs to see 404s and redirects. Check Status.
type Response struct {
	Status    int
	Header    http.Header
	Body      []byte
	URL       string // the final URL after redirects
	Truncated bool   // the body was longer than maxBytes and was cut
	Redirects int    // how many redirects were followed
}

var (
	transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 25 * time.Second,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	client = &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: checkRedirect}
	// noRedirect shares the transport but hands back the first response as is.
	noRedirect = &http.Client{Transport: transport, Timeout: 45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
)

func checkRedirect(_ *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}
	return nil
}

// Client returns the shared client, for the rare caller that needs it directly
// (the OAuth library takes one). It does not apply the host pacing or retry.
func Client() *http.Client { return client }

// retryWait is the pause before the one retry (a var so tests are fast).
var retryWait = 500 * time.Millisecond

// Get fetches a URL with GET and reads at most maxBytes of the body.
func Get(ctx context.Context, rawURL string, maxBytes int64) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, redacted(err), "bad URL %q", RedactURL(rawURL))
	}
	return Do(ctx, req, maxBytes)
}

// GetNoRedirect is Get that does not follow redirects: Status is 301/302 and
// Header has the Location.
func GetNoRedirect(ctx context.Context, rawURL string, maxBytes int64) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, redacted(err), "bad URL %q", RedactURL(rawURL))
	}
	return do(ctx, noRedirect, req, maxBytes)
}

// Do sends a request through the shared client.
func Do(ctx context.Context, req *http.Request, maxBytes int64) (*Response, error) {
	return do(ctx, client, req, maxBytes)
}

func do(ctx context.Context, c *http.Client, req *http.Request, maxBytes int64) (*Response, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent())
	}
	req = req.WithContext(ctx)
	rewriteForTest(req)
	var resp *Response
	for attempt := 0; ; attempt++ {
		r, err := once(ctx, c, req, maxBytes)
		if err != nil {
			return nil, mapErr(ctx, err)
		}
		resp = r
		if attempt == 0 && (r.Status == 429 || r.Status >= 500) && replayable(req) {
			if !sleep(ctx, backoff(r.Header)) {
				return nil, mapErr(ctx, ctx.Err())
			}
			if req.GetBody != nil {
				b, err := req.GetBody()
				if err != nil {
					return resp, nil
				}
				req = req.Clone(ctx)
				req.Body = b
			}
			continue
		}
		return resp, nil
	}
}

func replayable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// backoff is the short wait before the retry: Retry-After (in seconds) when it
// is small, else retryWait.
func backoff(h http.Header) time.Duration {
	if n, err := strconv.Atoi(h.Get("Retry-After")); err == nil && n >= 0 && n <= 3 {
		return time.Duration(n) * time.Second
	}
	return retryWait
}

func once(ctx context.Context, c *http.Client, req *http.Request, maxBytes int64) (*Response, error) {
	release, err := acquire(ctx, req.URL.Host)
	if err != nil {
		return nil, err
	}
	defer release()
	hr, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer hr.Body.Close()
	body, err := io.ReadAll(io.LimitReader(hr.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	out := &Response{Status: hr.StatusCode, Header: hr.Header, URL: hr.Request.URL.String()}
	if int64(len(body)) > maxBytes {
		body, out.Truncated = body[:maxBytes], true
	}
	out.Body = body
	return out, nil
}

// mapErr turns a transport error into an apperr: ctx cancel or deadline is
// timeout (never network or internal), anything else is network_error.
var secretParam = regexp.MustCompile(`(?i)([?&](?:apikey|key|access_token)=)[^&\s"']*`)

// RedactURL hides the values of apikey, key and access_token query params in
// any text that holds a URL, so a key never reaches an error, a warning or the
// envelope. Area packages use it for their own messages too.
func RedactURL(s string) string { return secretParam.ReplaceAllString(s, "${1}REDACTED") }

// redacted is err with its URLs cleaned; the original chain is dropped because
// *url.Error carries the full URL.
func redacted(err error) error {
	if err == nil {
		return nil
	}
	if m := RedactURL(err.Error()); m != err.Error() {
		return errors.New(m)
	}
	return err
}

func mapErr(ctx context.Context, err error) error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return apperr.From(firstCtxErr(ctx, err))
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return apperr.Wrap(apperr.Timeout, redacted(err), "request timed out")
	}
	return apperr.Wrap(apperr.NetworkError, redacted(err), "request failed")
}

func firstCtxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
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

// ---- test hook: swappable base URLs

var (
	baseMu    sync.RWMutex
	baseOver  = map[string]string{}
	rewriteTo = map[string]string{}
)

// BaseURL returns the base URL for a named source: def normally, or the
// override a test set with OverrideBase. Source packages build their URLs from
// it, e.g. web.BaseURL("psi", "https://www.googleapis.com/pagespeedonline/v5").
func BaseURL(name, def string) string {
	baseMu.RLock()
	defer baseMu.RUnlock()
	if v, ok := baseOver[name]; ok {
		return v
	}
	return def
}

// OverrideBase points a named source at another URL (an httptest server) and
// returns the function that puts it back. For tests only.
func OverrideBase(name, to string) (restore func()) {
	baseMu.Lock()
	old, had := baseOver[name]
	baseOver[name] = to
	baseMu.Unlock()
	return func() {
		baseMu.Lock()
		defer baseMu.Unlock()
		if had {
			baseOver[name] = old
		} else {
			delete(baseOver, name)
		}
	}
}

// RewriteHost sends every request for a host ("example.com", "www.example.com:443")
// to another host:port over plain HTTP, so a crawl test can use real-looking
// URLs against an httptest server. It returns the function that undoes it. For tests only.
func RewriteHost(from, toHostPort string) (restore func()) {
	baseMu.Lock()
	old, had := rewriteTo[from]
	rewriteTo[from] = toHostPort
	baseMu.Unlock()
	return func() {
		baseMu.Lock()
		defer baseMu.Unlock()
		if had {
			rewriteTo[from] = old
		} else {
			delete(rewriteTo, from)
		}
	}
}

func rewriteForTest(req *http.Request) {
	baseMu.RLock()
	defer baseMu.RUnlock()
	if len(rewriteTo) == 0 {
		return
	}
	if to, ok := rewriteTo[req.URL.Host]; ok {
		req.URL.Scheme = "http"
		req.URL.Host = to
	}
}
