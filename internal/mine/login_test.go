package mine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
)

func clientFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "client_secret.json")
	if err := os.WriteFile(p, fixture(t, "client_secret.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func loginIn(t *testing.T, extra map[string]any) ops.Input {
	vals := map[string]any{"client": clientFile(t)}
	for k, v := range extra {
		vals[k] = v
	}
	return ops.Input{Op: ops.Find("login"), Values: vals}
}

func TestLoginWithoutTTYNeverRuns(t *testing.T) {
	f := newEnv(t)
	hasTTY = func() bool { return false }
	opened := false
	openBrowser = func(string) error { opened = true; return nil }
	_, err := login(context.Background(), loginIn(t, nil))
	ae := apperr.From(err)
	if ae.Code != apperr.LoginRequired || !strings.Contains(ae.Message, LoginFix) {
		t.Fatalf("got %v", err)
	}
	if d, _ := ae.Detail.(map[string]string); d["command"] != LoginFix {
		t.Errorf("detail %v", ae.Detail)
	}
	if opened || len(f.mem.m) != 0 {
		t.Error("no browser and nothing stored without a terminal")
	}
}

// browserFlow plays the user: it reads the sign-in URL, first sends a forged
// reply (wrong state), then the real one.
func browserFlow(t *testing.T, f *fake, calls *int) {
	f.codeCheck = func(form url.Values) string {
		if form.Get("code") != "the-code" || form.Get("client_secret") != "test-secret" || form.Get("client_id") != "123-abc.apps.googleusercontent.com" {
			return "bad exchange form: " + form.Encode()
		}
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			return "PKCE verifier does not match the challenge"
		}
		return ""
	}
	openBrowser = func(u string) error {
		*calls++
		pu, _ := url.Parse(u)
		q := pu.Query()
		if q.Get("scope") != Scope || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" ||
			q.Get("access_type") != "offline" || !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
			t.Errorf("auth URL %s", u)
		}
		challenge = q.Get("code_challenge")
		go func() {
			bad, _ := http.Get(q.Get("redirect_uri") + "?state=forged&code=evil")
			if bad != nil {
				if bad.StatusCode != http.StatusBadRequest {
					t.Errorf("forged reply got %d", bad.StatusCode)
				}
				bad.Body.Close()
			}
			ok, err := http.Get(q.Get("redirect_uri") + "?state=" + url.QueryEscape(q.Get("state")) + "&code=the-code")
			if err == nil {
				ok.Body.Close()
			}
		}()
		return nil
	}
}

var challenge string

func TestLoginLoopbackFlowStoresInKeychain(t *testing.T) {
	f := newEnv(t)
	loginWait = 10 * time.Second
	t.Cleanup(func() { loginWait = 5 * time.Minute })
	calls := 0
	browserFlow(t, f, &calls)
	res, err := login(context.Background(), loginIn(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("browser opened %d times", calls)
	}
	d := res.Data.(map[string]any)
	if d["signed_in"] != true || d["stored"] != "keychain" || d["properties"] != 5 {
		t.Errorf("data %v", d)
	}
	tok, _ := f.mem.Get(config.SecretOAuthToken)
	cl, _ := f.mem.Get(config.SecretOAuthClient)
	if !strings.Contains(tok, "login-access") || !strings.Contains(tok, "refresh-1") || !strings.Contains(cl, "test-secret") || strings.Contains(cl, "\n") {
		t.Errorf("token %q client %q", tok, cl)
	}
}

func TestLoginWithYesFlagWithoutTTY(t *testing.T) {
	f := newEnv(t)
	hasTTY = func() bool { return false }
	loginWait = 10 * time.Second
	t.Cleanup(func() { loginWait = 5 * time.Minute })
	calls := 0
	browserFlow(t, f, &calls)
	if _, err := login(context.Background(), loginIn(t, map[string]any{"yes": true})); err != nil || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestLoginRejectsBadClientFiles(t *testing.T) {
	newEnv(t)
	for name, body := range map[string]string{
		"web":  `{"web":{"client_id":"x.apps.googleusercontent.com","client_secret":"s"}}`,
		"id":   `{"installed":{"client_id":"nope","client_secret":"s"}}`,
		"junk": `hello`,
	} {
		p := filepath.Join(t.TempDir(), name+".json")
		_ = os.WriteFile(p, []byte(body), 0o600)
		if _, err := login(context.Background(), ops.Input{Values: map[string]any{"client": p}}); apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLoginTimesOut(t *testing.T) {
	newEnv(t)
	loginWait = 50 * time.Millisecond
	t.Cleanup(func() { loginWait = 5 * time.Minute })
	_, err := login(context.Background(), loginIn(t, nil))
	if apperr.From(err).Code != apperr.Timeout {
		t.Errorf("got %v", err)
	}
	if d, _ := apperr.From(err).Detail.(map[string]string); !strings.HasPrefix(d["auth_url"], "https://accounts.google.com") {
		t.Errorf("auth URL missing from detail: %v", apperr.From(err).Detail)
	}
}

func TestKeychainOffRefusesLogin(t *testing.T) {
	newEnv(t)
	store = keychain{} // the real adapter, with SEOX_NO_KEYCHAIN set by newEnv
	if _, err := login(context.Background(), loginIn(t, nil)); apperr.From(err).Code != apperr.InvalidInput {
		t.Errorf("got %v", err)
	}
}

func TestLogoutDeletesBothAndRevokes(t *testing.T) {
	f := newEnv(t)
	f.signIn()
	res, err := logout(context.Background(), ops.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.mem.m) != 0 {
		t.Errorf("left in store: %v", f.mem.m)
	}
	d := res.Data.(map[string]any)
	if d["signed_out"] != true || d["revoked"] != true || len(f.revoked) != 1 || f.revoked[0] != "refresh-1" {
		t.Errorf("%v revoked %v", d, f.revoked)
	}
	// Logging out twice is fine.
	if res, err = logout(context.Background(), ops.Input{}); err != nil || res.Data.(map[string]any)["was_signed_in"] != false {
		t.Errorf("second logout: %v %+v", err, res)
	}
}
