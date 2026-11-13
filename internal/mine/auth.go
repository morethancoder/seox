package mine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/term"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/web"
)

// Scope is read-only Search Console access; seox never changes a property.
const Scope = "https://www.googleapis.com/auth/webmasters.readonly"

// LoginFix is the command that fixes a missing or expired sign-in.
const LoginFix = "seox login --client client_secret.json"

// The Google endpoints are pinned here rather than read from the client file,
// so a tampered client file can't send the code or token elsewhere. They are
// vars so tests can point them at an httptest server.
var (
	endpoint = oauth2.Endpoint{
		AuthURL:   "https://accounts.google.com/o/oauth2/auth",
		TokenURL:  "https://oauth2.googleapis.com/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	revokeURL = "https://oauth2.googleapis.com/revoke"
	// loginWait is how long login waits for the browser to come back.
	loginWait = 5 * time.Minute
	// stderr gets the sign-in URL and what login is waiting for.
	stderr io.Writer = os.Stderr
	// openBrowser opens the sign-in page; tests replace it with a fake browser.
	openBrowser = defaultOpenBrowser
	// hasTTY says whether a person is at the terminal; tests replace it.
	hasTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// ---- secret store

// secretStore is where the sign-in lives. The real one is the Keychain through
// internal/config; tests swap in a map so they never touch the Keychain.
type secretStore interface {
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
}

type keychain struct{}

func (keychain) Get(a string) (string, error) { return config.Secret(a) }
func (keychain) Set(a, v string) error        { return config.SetSecret(a, v) }
func (keychain) Delete(a string) error        { return config.DeleteSecret(a) }

var store secretStore = keychain{}

// storeReady fails before the browser opens when the sign-in could not be
// saved afterwards (the Keychain is turned off).
func storeReady() error {
	if _, real := store.(keychain); real && os.Getenv("SEOX_NO_KEYCHAIN") != "" {
		return apperr.New(apperr.InvalidInput, "the Keychain is off (SEOX_NO_KEYCHAIN is set), so a sign-in could not be stored")
	}
	return nil
}

// ---- OAuth client file

type clientInfo struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// parseClient checks a Google Cloud OAuth client JSON and returns its id and secret.
func parseClient(b []byte) (*clientInfo, error) {
	var f struct {
		Installed *clientInfo     `json:"installed"`
		Web       json.RawMessage `json:"web"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file is not JSON from Google Cloud (download it from APIs & Services > Credentials)")
	}
	if f.Installed == nil {
		if f.Web != nil {
			return nil, apperr.New(apperr.InvalidInput, "this is a Web application client; create an OAuth client of type Desktop app and download its JSON")
		}
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file has no \"installed\" section; create an OAuth client of type Desktop app and download its JSON")
	}
	ci := f.Installed
	ci.ClientID = strings.TrimSpace(ci.ClientID)
	ci.ClientSecret = strings.TrimSpace(ci.ClientSecret)
	if !strings.HasSuffix(ci.ClientID, ".apps.googleusercontent.com") {
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file's client_id doesn't look like a Google client id")
	}
	if ci.ClientSecret == "" {
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file has no client_secret")
	}
	return ci, nil
}

func oauthConfig(ci *clientInfo, redirect string) *oauth2.Config {
	return &oauth2.Config{ClientID: ci.ClientID, ClientSecret: ci.ClientSecret, Endpoint: endpoint, RedirectURL: redirect, Scopes: []string{Scope}}
}

// httpCtx makes oauth2's own token calls use seox's shared client.
func httpCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, web.Client())
}

// ---- token

func loadToken() (*oauth2.Token, error) {
	s, err := store.Get(config.SecretOAuthToken)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't read the sign-in from the Keychain")
	}
	if s == "" {
		return nil, nil
	}
	t := &oauth2.Token{}
	if err := json.Unmarshal([]byte(s), t); err != nil {
		return nil, apperr.New(apperr.LoginRequired, "the stored sign-in is unreadable; run `%s`", LoginFix)
	}
	return t, nil
}

func saveToken(t *oauth2.Token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return store.Set(config.SecretOAuthToken, string(b))
}

// savingSource writes every new access token back to the store, so a refresh
// made by one command is reused by the next instead of refreshing again.
type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.src.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.ErrorCode == "unauthorized_client" || re.ErrorCode == "invalid_client") {
			return nil, apperr.New(apperr.LoginRequired, "the Google sign-in expired or was revoked (%s); run `%s`", re.ErrorCode, LoginFix)
		}
		return nil, apperr.From(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		// A failed save only costs one extra refresh next time, so it doesn't fail the call.
		_ = saveToken(t)
	}
	return t, nil
}

// notSignedInMessage is the text of every login_required answer, with the steps.
func notSignedInMessage() string {
	return "not signed in to Google Search Console. Steps: (1) in Google Cloud console create an OAuth client of type Desktop app " +
		"and download client_secret.json; (2) in a terminal run `" + LoginFix + "`; (3) run `seox setup verify <site>` if the site isn't verified yet"
}

// accessToken returns a fresh Google access token, refreshing and saving it
// when needed. It returns login_required, wrapping ErrNotSignedIn, when nobody
// is signed in.
func accessToken(ctx context.Context) (string, error) {
	tok, err := loadToken()
	if err != nil {
		return "", err
	}
	if tok == nil || (tok.AccessToken == "" && tok.RefreshToken == "") {
		return "", notSignedIn(notSignedInMessage())
	}
	raw, err := store.Get(config.SecretOAuthClient)
	if err != nil {
		return "", apperr.Wrap(apperr.Internal, err, "couldn't read the OAuth client from the Keychain")
	}
	ci, perr := parseClient([]byte(raw))
	if raw == "" || perr != nil {
		return "", notSignedIn("no OAuth client is stored; run `" + LoginFix + "`")
	}
	src := &savingSource{src: oauthConfig(ci, "").TokenSource(httpCtx(ctx), tok), last: tok.AccessToken}
	t, err := src.Token()
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Code == apperr.LoginRequired {
			return "", &apperr.Error{Code: apperr.LoginRequired, Message: ae.Message, Err: ErrNotSignedIn}
		}
		return "", err
	}
	return t.AccessToken, nil
}

// signedIn reports whether a token is stored, without calling Google.
func signedIn() bool {
	t, err := loadToken()
	return err == nil && t != nil && (t.AccessToken != "" || t.RefreshToken != "")
}

// ---- login

func login(ctx context.Context, in ops.Input) (*ops.Result, error) {
	// A login opens a browser and waits for a person, so it never starts from a
	// pipe unless the caller opted in with --yes (or SEOX_LOGIN=1).
	if !hasTTY() && !in.Bool("yes") && os.Getenv("SEOX_LOGIN") != "1" {
		return nil, &apperr.Error{Code: apperr.LoginRequired, Message: "login opens a browser and needs a terminal; run `" + LoginFix +
			"` in a terminal (or add --yes to start it from a script)", Detail: map[string]string{"command": LoginFix}}
	}
	if err := storeReady(); err != nil {
		return nil, err
	}
	var raw []byte
	if p := in.Str("client"); p != "" {
		b, err := os.ReadFile(config.ExpandHome(p))
		if err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "couldn't read the OAuth client file")
		}
		if len(b) > 64<<10 {
			return nil, apperr.New(apperr.InvalidInput, "the OAuth client file is too big to be a client_secret.json")
		}
		raw = b
	} else {
		s, err := store.Get(config.SecretOAuthClient)
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "couldn't read the OAuth client from the Keychain")
		}
		if s == "" {
			return nil, apperr.New(apperr.InvalidInput, "no OAuth client stored yet: pass --client client_secret.json")
		}
		raw = []byte(s)
	}
	ci, err := parseClient(raw)
	if err != nil {
		return nil, err
	}
	tok, err := loopbackLogin(ctx, ci)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "the OAuth client file is not valid JSON")
	}
	if err := store.Set(config.SecretOAuthClient, compact.String()); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't store the OAuth client in the Keychain")
	}
	if err := saveToken(tok); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't store the sign-in in the Keychain")
	}
	res := &ops.Result{Meta: ops.Meta{Source: "oauth"}}
	data := map[string]any{"signed_in": true, "stored": "keychain", "scope": Scope}
	if tok.RefreshToken == "" {
		res.Warnings = append(res.Warnings, "Google sent no refresh token, so the sign-in lasts about an hour; remove seox's access at myaccount.google.com/permissions and log in again")
	}
	if s, _ := tok.Extra("scope").(string); s != "" && !strings.Contains(s, Scope) {
		res.Warnings = append(res.Warnings, "Google didn't grant the Search Console scope; tick every box when you log in again")
	}
	// Counting the properties confirms the sign-in works.
	if sites, err := listSites(ctx, tok.AccessToken); err == nil {
		data["properties"] = len(sites)
	} else {
		res.Warnings = append(res.Warnings, "signed in, but couldn't list your Search Console properties: "+apperr.From(err).Message)
	}
	res.Data = data
	return res, nil
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// loopbackLogin runs the installed-app flow: a one-shot server on
// 127.0.0.1:<random port> receives Google's redirect. The state must match (a
// forged or stale redirect is refused and the wait goes on), and the code is
// exchanged with the PKCE verifier only this process knows. It never reads
// stdin: it waits for the browser, at most loginWait.
func loopbackLogin(ctx context.Context, ci *clientInfo) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't open a local port for the sign-in")
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	cfg := oauthConfig(ci, redirect)
	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		ln.Close()
		return nil, apperr.Wrap(apperr.Internal, err, "no randomness for the sign-in state")
	}
	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"), oauth2.S256ChallengeOption(verifier))

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	finish := func(r result) { once.Do(func() { done <- r }) }
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			// Not ours: refuse it but keep waiting, so a stray request can't end a real sign-in.
			http.Error(w, "seox: this sign-in reply doesn't match the one seox started. Run `seox login` again.", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			page(w, "Sign-in was not completed ("+e+"). You can close this tab.")
			finish(result{err: apperr.New(apperr.LoginRequired, "Google sign-in was not completed (%s)", e)})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "seox: the reply has no code", http.StatusBadRequest)
			return
		}
		page(w, "seox is signed in. You can close this tab and go back to the terminal.")
		finish(result{code: code})
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	// Progress text goes to a terminal only; otherwise the URL travels in the error detail.
	tty := stderrIsTTY()
	if tty {
		fmt.Fprintf(stderr, "Sign in with Google in the browser. If it didn't open, visit:\n\n  %s\n\n", authURL)
	}
	if err := openBrowser(authURL); err != nil && tty {
		fmt.Fprintf(stderr, "(couldn't open the browser: %v)\n", err)
	}
	if tty {
		fmt.Fprintf(stderr, "Waiting up to %s for the browser sign-in to come back to %s (no terminal input needed)...\n", loginWait, redirect)
	}
	withURL := func(err error) error {
		if ae, ok := err.(*apperr.Error); ok && !tty && ae.Detail == nil {
			ae.Detail = map[string]string{"auth_url": authURL}
		}
		return err
	}

	timer := time.NewTimer(loginWait)
	defer timer.Stop()
	var code string
	select {
	case r := <-done:
		if r.err != nil {
			return nil, withURL(r.err)
		}
		code = r.code
	case <-timer.C:
		return nil, withURL(apperr.New(apperr.Timeout, "no sign-in came back within %s; run `seox login` again", loginWait))
	case <-ctx.Done():
		return nil, apperr.From(ctx.Err())
	}
	tok, err := cfg.Exchange(httpCtx(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return nil, apperr.New(apperr.LoginRequired, "Google refused the sign-in code (%s); run `seox login` again", re.ErrorCode)
		}
		return nil, apperr.From(err)
	}
	return tok, nil
}

// stderrIsTTY says whether progress text may be printed; a non-file writer (a test buffer) counts as not a terminal.
func stderrIsTTY() bool {
	f, ok := stderr.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func page(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>seox</title><body style=\"font:16px system-ui;margin:3em\"><p>%s</p>", html.EscapeString(msg))
}

func defaultOpenBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		return errors.New("no known way to open a browser here")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// ---- logout

// logout deletes the token and the client. The refresh token is revoked at
// Google on a best effort basis.
func logout(ctx context.Context, _ ops.Input) (*ops.Result, error) {
	tok, _ := loadToken()
	if err := store.Delete(config.SecretOAuthToken); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't remove the sign-in from the Keychain")
	}
	if err := store.Delete(config.SecretOAuthClient); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't remove the OAuth client from the Keychain")
	}
	data := map[string]any{"signed_out": true, "was_signed_in": tok != nil}
	res := &ops.Result{Data: data, Meta: ops.Meta{Source: "local"}}
	if tok == nil {
		return res, nil
	}
	t := tok.RefreshToken
	if t == "" {
		t = tok.AccessToken
	}
	err := revoke(ctx, t)
	data["revoked"] = err == nil
	if err != nil {
		res.Warnings = append(res.Warnings, "forgot the sign-in here, but couldn't revoke it at Google ("+err.Error()+"); remove seox at myaccount.google.com/permissions if you want")
	}
	return res, nil
}

func revoke(ctx context.Context, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := web.Do(ctx, req, 4096)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Google answered %d", resp.Status)
	}
	return nil
}
