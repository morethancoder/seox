package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/ops"
)

type env struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	} `json:"error"`
	Meta     map[string]any `json:"meta"`
	Warnings []string       `json:"warnings"`
}

// runCLI runs seox with args and returns the exit code and the one envelope it printed.
func runCLI(t *testing.T, args ...string) (int, env) {
	t.Helper()
	t.Setenv("SEOX_HOME", t.TempDir())
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	var buf bytes.Buffer
	stdout = &buf
	t.Cleanup(func() { stdout = os.Stdout })
	code := execute(args)
	out := strings.TrimSpace(buf.String())
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("want exactly one JSON line, got:\n%s", out)
	}
	var e env
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return code, e
}

var fakeOnce sync.Once

// The cli test binary imports no area package, so a handler for ops that
// only areas own can be attached here.
func fakes() {
	fakeOnce.Do(func() {
		ops.Handle("authority", func(_ context.Context, in ops.Input) (*ops.Result, error) {
			return &ops.Result{Data: map[string]any{"domains": in.Strings("domain")}, Warnings: []string{"w1"}}, nil
		})
		ops.Handle("compare", func(_ context.Context, in ops.Input) (*ops.Result, error) {
			return &ops.Result{Data: []map[string]any{{"id": "a", "n": 1}, {"id": "b", "n": 2}}}, nil
		})
	})
}

func TestUnknownCommandGivesEnvelope(t *testing.T) {
	code, e := runCLI(t, "nope")
	if code != 2 || e.OK || e.Error.Code != "invalid_input" {
		t.Fatalf("code %d env %+v", code, e)
	}
}

func TestMissingPositionalIsInvalidInput(t *testing.T) {
	code, e := runCLI(t, "audit")
	if code != 2 || e.Error.Code != "invalid_input" || !strings.Contains(e.Error.Message, "usage: seox audit <site>") {
		t.Fatalf("code %d env %+v", code, e.Error)
	}
	code, e = runCLI(t, "authority")
	if code != 2 || !strings.Contains(e.Error.Message, "<domain>...") {
		t.Fatalf("a variadic needs at least one value: %d %+v", code, e.Error)
	}
}

func TestUnbuiltOpIsNotImplemented(t *testing.T) {
	code, e := runCLI(t, "audit", "example.com", "--json")
	if code != 1 || e.Error.Code != "not_implemented" {
		t.Fatalf("code %d env %+v", code, e.Error)
	}
}

func TestVariadicAndFields(t *testing.T) {
	fakes()
	code, e := runCLI(t, "authority", "a.com", "b.com", "c.com")
	if code != 0 || string(e.Data) != `{"domains":["a.com","b.com","c.com"]}` || len(e.Warnings) != 1 {
		t.Fatalf("code %d env %s %v", code, e.Data, e.Warnings)
	}
	code, e = runCLI(t, "compare", "x.com", "y.com", "--fields", "id")
	if code != 0 || string(e.Data) != `[{"id":"a"},{"id":"b"}]` {
		t.Fatalf("fields filter: %d %s", code, e.Data)
	}
}

func TestSchemaListsEveryOp(t *testing.T) {
	code, e := runCLI(t, "schema", "--no-skill")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	var s struct {
		Ops []struct {
			Name   string `json:"name"`
			Params []struct {
				Name     string `json:"name"`
				Variadic bool   `json:"variadic"`
			} `json:"params"`
		} `json:"ops"`
	}
	if err := json.Unmarshal(e.Data, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Ops) != len(ops.All) || len(s.Ops) != 22 {
		t.Fatalf("schema has %d ops, table has %d (want 22)", len(s.Ops), len(ops.All))
	}
	if len(Commands()) != len(ops.All)+1 {
		t.Fatalf("commands %v", Commands())
	}
}

func TestTimeoutFlagOverridesTable(t *testing.T) {
	if timeoutFor(ops.Find("audit")) != 10*time.Minute || timeoutFor(ops.Find("login")) != 0 {
		t.Fatal("table timeouts")
	}
	gTimeout = time.Second
	defer func() { gTimeout = 0 }()
	if timeoutFor(ops.Find("audit")) != time.Second {
		t.Fatal("--timeout should win")
	}
}

func TestConfigSetShowAndCacheClear(t *testing.T) {
	code, e := runCLI(t, "config", "set", "crawl_max", "25")
	if code != 0 || !strings.Contains(string(e.Data), `"crawl_max":25`) {
		t.Fatalf("%d %s", code, e.Data)
	}
	code, e = runCLI(t, "config", "set", "bogus", "1")
	if code != 2 || e.Error.Code != "invalid_input" {
		t.Fatalf("%d %+v", code, e.Error)
	}
	code, e = runCLI(t, "config")
	if code != 0 || !strings.Contains(string(e.Data), `"crawl_max":200`) {
		t.Fatalf("effective default missing: %d %s", code, e.Data)
	}
	if code, _ = runCLI(t, "cache", "clear"); code != 0 {
		t.Fatal("cache clear")
	}
}

// key set never prompts without a terminal: with empty stdin it fails fast.
func TestKeySetNeverPromptsWithoutTerminal(t *testing.T) {
	stdinIsTTY = func() bool { return false }
	prompted := false
	promptKey = func(string) (string, error) { prompted = true; return "", nil }
	defer func() { stdinIsTTY = func() bool { return isTTY(os.Stdin) }; promptKey = readSecretTTY }()

	r, w, _ := os.Pipe()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	w.Close()
	code, e := runCLI(t, "key", "set", "bing")
	if code != 2 || e.Error.Code != "invalid_input" || prompted {
		t.Fatalf("code %d prompted %v %+v", code, prompted, e.Error)
	}
	if code, e = runCLI(t, "key", "set", "nope"); code != 2 || !strings.Contains(e.Error.Message, "google, bing, openpagerank") {
		t.Fatalf("%d %+v", code, e.Error)
	}
	if code, _ = runCLI(t, "key", "remove", "bing"); code != 0 {
		t.Fatal("key remove")
	}
}

func TestKeySetFromPipedStdinNeedsKeychain(t *testing.T) {
	stdinIsTTY = func() bool { return false }
	defer func() { stdinIsTTY = func() bool { return isTTY(os.Stdin) } }()
	r, w, _ := os.Pipe()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	w.WriteString("abcdefghijklmnopqrstuvwx\n")
	w.Close()
	// SEOX_NO_KEYCHAIN is set by runCLI, so storing is refused after the key was read and checked.
	code, e := runCLI(t, "key", "set", "google")
	if code != 2 || !strings.Contains(e.Error.Message, "Keychain is off") {
		t.Fatalf("%d %+v", code, e.Error)
	}
}

func TestDoctorRuns(t *testing.T) {
	t.Setenv("SEOX_GOOGLE_API_KEY", "abcdefghijklmnopqrstuvwx")
	code, e := runCLI(t, "doctor")
	if code != 0 {
		t.Fatalf("%d %+v", code, e.Error)
	}
	for _, k := range []string{"key_google", "key_bing", "key_openpagerank", "signed_in", "searxng", "report_dir"} {
		if !strings.Contains(string(e.Data), `"`+k+`"`) {
			t.Errorf("doctor lacks %s: %s", k, e.Data)
		}
	}
}

func TestMergeAreaChecksRefusesBuiltinName(t *testing.T) {
	out := map[string]check{"searxng": {OK: true}}
	mergeAreaChecks(out, map[string]ops.Check{"lighthouse": {OK: true}})
	if _, ok := out["lighthouse"]; !ok {
		t.Fatal("area check not merged")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on name clash")
		}
	}()
	mergeAreaChecks(out, map[string]ops.Check{"searxng": {}})
}
