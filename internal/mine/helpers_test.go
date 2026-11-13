package mine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/web"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) Get(a string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[a], nil
}
func (s *memStore) Set(a, v string) error { s.mu.Lock(); defer s.mu.Unlock(); s.m[a] = v; return nil }
func (s *memStore) Delete(a string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, a)
	return nil
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mine", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake stands in for Google (token, revoke, Search Console) and Bing.
type fake struct {
	t   *testing.T
	srv *httptest.Server
	mem *memStore

	mu         sync.Mutex
	refreshes  int
	revoked    []string
	queryBody  []map[string]any
	authHeader []string
	bingCalls  []string
	bingKeys   []string
	bingQuery  []url.Values

	sitesFile string // gsc_sites.json by default
	gscStatus int    // when set, every Search Console call answers this with gsc_error_403.json
	tokenFail bool   // the token endpoint answers invalid_grant
	bingFail  bool   // every Bing call answers InvalidApiKey
	codeCheck func(url.Values) string
}

// newEnv wires the package to a fake server and an in-memory Keychain, with the clock fixed to 2026-11-12.
func newEnv(t *testing.T) *fake {
	t.Helper()
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	t.Setenv("SEOX_BING_KEY", "")
	t.Setenv("SEOX_LOGIN", "")
	f := &fake{t: t, mem: &memStore{m: map[string]string{}}, sitesFile: "gsc_sites.json"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	oldStore, oldEP, oldRev, oldNow, oldTTY, oldOpen, oldErr, oldMax := store, endpoint, revokeURL, now, hasTTY, openBrowser, stderr, maxGSCRows
	store = f.mem
	endpoint.TokenURL = f.srv.URL + "/token"
	revokeURL = f.srv.URL + "/revoke"
	now = func() time.Time { return time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC) }
	hasTTY = func() bool { return true }
	stderr = io.Discard
	openBrowser = func(string) error { return nil }
	t.Cleanup(func() {
		store, endpoint, revokeURL, now, hasTTY, openBrowser, stderr, maxGSCRows = oldStore, oldEP, oldRev, oldNow, oldTTY, oldOpen, oldErr, oldMax
	})
	r1 := web.OverrideBase("gsc", f.srv.URL+"/webmasters/v3")
	r2 := web.OverrideBase("bing", f.srv.URL+"/bing")
	r3 := web.SetMinGap(0)
	t.Cleanup(func() { r1(); r2(); r3() })
	return f
}

// signIn stores a client and an expired access token with a refresh token.
func (f *fake) signIn() {
	var c json.RawMessage = fixture(f.t, "client_secret.json")
	b, _ := json.Marshal(json.RawMessage(c))
	_ = f.mem.Set(config.SecretOAuthClient, string(b))
	tok, _ := json.Marshal(&oauth2.Token{AccessToken: "old-access", RefreshToken: "refresh-1", TokenType: "Bearer", Expiry: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
	_ = f.mem.Set(config.SecretOAuthToken, string(tok))
}

func (f *fake) bingKey(t *testing.T) { t.Setenv("SEOX_BING_KEY", "bing-test-key") }

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.EscapedPath()
	send := func(status int, b []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(b)
	}
	switch {
	case p == "/token":
		_ = r.ParseForm()
		if f.tokenFail {
			send(400, []byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
			return
		}
		switch r.Form.Get("grant_type") {
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				send(400, []byte(`{"error":"invalid_grant"}`))
				return
			}
			f.refreshes++
			send(200, []byte(`{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`))
		case "authorization_code":
			if f.codeCheck != nil {
				if msg := f.codeCheck(r.Form); msg != "" {
					f.t.Error(msg)
					send(400, []byte(`{"error":"invalid_grant"}`))
					return
				}
			}
			send(200, []byte(`{"access_token":"login-access","refresh_token":"refresh-1","token_type":"Bearer","expires_in":3600,"scope":"`+Scope+`"}`))
		default:
			send(400, []byte(`{"error":"unsupported_grant_type"}`))
		}
	case p == "/revoke":
		_ = r.ParseForm()
		f.revoked = append(f.revoked, r.Form.Get("token"))
		send(200, []byte(`{}`))
	case strings.HasPrefix(p, "/webmasters/v3/"):
		f.authHeader = append(f.authHeader, r.Header.Get("Authorization"))
		if f.gscStatus != 0 {
			send(f.gscStatus, fixture(f.t, "gsc_error_403.json"))
			return
		}
		if p == "/webmasters/v3/sites" {
			send(200, fixture(f.t, f.sitesFile))
			return
		}
		if strings.HasSuffix(p, "/searchAnalytics/query") && r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["_property"], _ = url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(p, "/webmasters/v3/sites/"), "/searchAnalytics/query"))
			f.queryBody = append(f.queryBody, body)
			dim := body["dimensions"].([]any)[0].(string)
			win := "cur"
			if body["startDate"] != "2026-10-14" {
				win = "prev"
			}
			var all struct {
				Rows []json.RawMessage `json:"rows"`
			}
			_ = json.Unmarshal(fixture(f.t, "gsc_"+dim+"_"+win+".json"), &all)
			start, n := int(body["startRow"].(float64)), int(body["rowLimit"].(float64))
			if start > len(all.Rows) {
				start = len(all.Rows)
			}
			end := min(start+n, len(all.Rows))
			out, _ := json.Marshal(map[string]any{"rows": all.Rows[start:end]})
			send(200, out)
			return
		}
		send(404, []byte(`{"error":{"code":404,"message":"no such route"}}`))
	case strings.HasPrefix(p, "/bing/"):
		m := strings.TrimPrefix(p, "/bing/")
		f.bingCalls = append(f.bingCalls, m)
		f.bingKeys = append(f.bingKeys, r.URL.Query().Get("apikey"))
		f.bingQuery = append(f.bingQuery, r.URL.Query())
		if f.bingFail {
			send(400, fixture(f.t, "bing_error.json"))
			return
		}
		files := map[string]string{"GetUserSites": "bing_sites.json", "GetQueryStats": "bing_querystats.json",
			"GetPageStats": "bing_pagestats.json", "GetLinkCounts": "bing_linkcounts.json", "GetKeywordStats": "bing_keywordstats.json"}
		if name, ok := files[m]; ok {
			send(200, fixture(f.t, name))
			return
		}
		send(404, []byte(`{"Message":"unknown method"}`))
	default:
		send(404, []byte(`{}`))
	}
}

func (f *fake) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.bingCalls {
		if m == method {
			n++
		}
	}
	return n
}
