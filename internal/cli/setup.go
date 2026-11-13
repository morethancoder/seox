package cli

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/web"
)

func init() {
	ops.Handle("doctor", doctor)
	ops.Handle("config.show", func(context.Context, ops.Input) (*ops.Result, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"file": config.Path("config.json"), "config": cfg, "effective": cfg.Effective(), "keys": config.Keys},
			Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("config.set", func(_ context.Context, in ops.Input) (*ops.Result, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		if err := cfg.Set(in.Str("key"), in.Str("value")); err != nil {
			return nil, err
		}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return &ops.Result{Data: cfg, Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("cache.clear", func(context.Context, ops.Input) (*ops.Result, error) {
		n, err := cache.Clear()
		if err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"removed": n}, Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("key.set", func(_ context.Context, in ops.Input) (*ops.Result, error) {
		name, err := keyName(in)
		if err != nil {
			return nil, err
		}
		key, err := readKey(name)
		if err != nil {
			return nil, err
		}
		if err := config.SetSecret(config.KeySecret(name), key); err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"name": name, "stored": "keychain", "key": mask(key)}, Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("key.remove", func(_ context.Context, in ops.Input) (*ops.Result, error) {
		name, err := keyName(in)
		if err != nil {
			return nil, err
		}
		if err := config.DeleteSecret(config.KeySecret(name)); err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"name": name, "removed": true}, Meta: ops.Meta{Source: "local"}}, nil
	})
}

func keyName(in ops.Input) (string, error) {
	name := strings.ToLower(in.Str("name"))
	if !config.ValidKeyName(name) {
		return "", apperr.New(apperr.InvalidInput, "unknown key %q (one of: %s)", in.Str("name"), strings.Join(config.KeyNames, ", "))
	}
	return name, nil
}

// stdinIsTTY and the secret prompt are vars so tests can pin them.
var (
	stdinIsTTY = func() bool { return isTTY(os.Stdin) }
	promptKey  = readSecretTTY
)

// readKey takes the key from piped stdin, or a hidden prompt on a terminal.
// It never prompts without a terminal.
func readKey(name string) (string, error) {
	var key string
	if !stdinIsTTY() {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return "", err
		}
		key = string(b)
	} else {
		s, err := promptKey(name + " key (" + config.KeyHelp(name) + "): ")
		if err != nil {
			return "", err
		}
		key = s
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", apperr.New(apperr.InvalidInput, "no key given: pipe it in, e.g. `printf %%s \"$KEY\" | seox key set %s`", name)
	}
	if strings.ContainsAny(key, " \t\n") || len(key) < 16 {
		return "", apperr.New(apperr.InvalidInput, "that doesn't look like an API key")
	}
	return key, nil
}

func mask(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

type check struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// doctor reports the state of every source. Nothing here is required: every
// command works with no account, and each missing item only adds a feature.
func doctor(ctx context.Context, _ ops.Input) (*ops.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	out := map[string]check{}

	for _, name := range config.KeyNames {
		fix := "printf %s \"$KEY\" | seox key set " + name + " (or export " + config.KeyEnv(name) + "=...)"
		if k, where := config.Key(name); k != "" {
			out["key_"+name] = check{true, "set (" + where + ", " + mask(k) + ")", ""}
		} else {
			out["key_"+name] = check{false, "not set: " + config.KeyHelp(name), fix}
		}
	}

	if tok, _ := config.Secret(config.SecretOAuthToken); tok != "" {
		out["signed_in"] = check{true, "Google sign-in stored in the Keychain", ""}
	} else {
		out["signed_in"] = check{false, "not signed in: `mine` needs it (or a Bing key)", "seox login --client client_secret.json"}
	}

	out["searxng"] = searxngCheck(ctx, cfg)

	switch dir := cfg.ReportDir; {
	case dir == "":
		out["report_dir"] = check{false, "not set: reports go to the current folder", "seox config set report_dir <folder>"}
	default:
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			out["report_dir"] = check{false, dir + " is not a folder", "seox config set report_dir <folder>"}
		} else {
			out["report_dir"] = check{true, dir, ""}
		}
	}
	mergeAreaChecks(out, ops.DoctorChecks())
	return &ops.Result{Data: map[string]any{"version": Version, "home": config.Home(), "checks": out}, Meta: ops.Meta{Source: "local"}}, nil
}

// mergeAreaChecks adds area checks after all built-in ones. A clash panics (a programming error
// caught by any doctor test) rather than letting an area silently replace a built-in line.
func mergeAreaChecks(out map[string]check, area map[string]ops.Check) {
	for name, c := range area {
		if _, dup := out[name]; dup {
			panic("seox doctor: area check name clashes with a built-in check: " + name)
		}
		out[name] = check{c.OK, c.Detail, c.Fix}
	}
}

// searxngCheck looks for the configured SearXNG, else Arshafa's install. It never starts anything.
func searxngCheck(ctx context.Context, cfg *config.Config) check {
	fix := "install web search in Arshafa (Settings > AI), or `seox config set searxng_url <url>`"
	if u := cfg.SearxngURL; u != "" {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u+"/healthz", nil)
		resp, err := web.Do(cctx, req, 1024)
		switch {
		case err != nil:
			return check{false, u + " is not reachable: " + err.Error(), "start it, or `seox config set searxng_url <url>`"}
		case resp.Status >= 400:
			return check{false, u + " answered " + http.StatusText(resp.Status), "check searxng_url"}
		}
		return check{true, "configured instance " + u + " is reachable", ""}
	}
	py := filepath.Join(cfg.ArshafaDir(), "venv", "bin", "python")
	if _, err := exec.LookPath(py); err != nil {
		return check{false, "no searxng_url and no Arshafa install at " + cfg.ArshafaDir(), fix}
	}
	return check{true, "Arshafa's SearXNG found at " + cfg.ArshafaDir() + " (seox starts its own copy for `rank`)", ""}
}
