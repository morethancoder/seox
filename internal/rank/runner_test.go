package rank

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
)

// fakePython stands in for venv/bin/python: it records how it was started,
// then becomes a tiny server for /healthz on the port from the settings file.
const fakePython = `#!/bin/sh
if [ -n "$FAKE_BINDFAIL_ONCE" ] && [ ! -e "$FAKE_BINDFAIL_ONCE" ]; then
  touch "$FAKE_BINDFAIL_ONCE"; echo "OSError: [Errno 48] Address already in use" >&2; exit 1
fi
{ echo "seoxenv=$(env | grep -c '^SEOX_')"; echo "cwd=$PWD"; echo "pythonpath=$PYTHONPATH"; echo "nobytecode=$PYTHONDONTWRITEBYTECODE"; echo "args=$*"; } > "$FAKE_RECORD"
[ -n "$FAKE_IGNORE_TERM" ] && trap '' TERM
port=$(sed -n 's/^  port: \([0-9]*\)$/\1/p' "$SEARXNG_SETTINGS_PATH")
exec python3 -c '
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"OK")
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
' "$port" "$@"
`

// fakeArshafa builds a folder that passes the "installed" check.
func fakeArshafa(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available for the fake server")
	}
	d := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(d, "venv", "bin"), 0o755))
	must(os.MkdirAll(filepath.Join(d, "src", "searx"), 0o755))
	must(os.WriteFile(filepath.Join(d, "installed.json"), []byte(`{"commit":"x"}`), 0o644))
	must(os.WriteFile(filepath.Join(d, "venv", "bin", "python"), []byte(fakePython), 0o755))
	return d
}

// snapshot lists every path under dir with size and mtime.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			st, _ := d.Info()
			m[p] = strconv.FormatInt(st.Size(), 10) + "/" + st.ModTime().String()
		}
		return nil
	})
	return m
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func runDir(t *testing.T, home string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(home, "searxng", "run-*"))
	if len(m) != 1 {
		t.Fatalf("run folders: %v", m)
	}
	return m[0]
}

func readPid(t *testing.T, home string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir(t, home), "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.Fields(string(b))[0])
	return pid
}

func waitGone(pid int) bool {
	for i := 0; i < 60; i++ {
		if !alive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func setup(t *testing.T) (home, arshafa string, cfg *config.Config) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("SEOX_HOME", home)
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	arshafa = fakeArshafa(t)
	rec := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", rec)
	return home, arshafa, &config.Config{ArshafaSearxngDir: arshafa}
}

func TestRunnerStartHealthStop(t *testing.T) {
	home, arshafa, cfg := setup(t)
	before := snapshot(t, arshafa)

	inst, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			inst.Stop()
		}
	}()
	if !inst.Own() || !strings.HasPrefix(inst.URL, "http://127.0.0.1:") || !healthy(context.Background(), inst.URL) {
		t.Fatalf("instance %+v", inst)
	}
	pid := readPid(t, home)
	if !alive(pid) {
		t.Fatal("process not running")
	}
	if pgid, _ := syscall.Getpgid(pid); pgid != pid {
		t.Fatalf("not its own process group: pgid %d pid %d", pgid, pid)
	}

	// Files live in seox home; the settings carry what rank needs.
	sd := runDir(t, home)
	yml, _ := os.ReadFile(filepath.Join(sd, "settings.yml"))
	for _, want := range []string{"keep_only: [google, bing, brave, duckduckgo, startpage, mojeek, qwant]", "formats: [html, json]", "limiter: false", `bind_address: "127.0.0.1"`, "port: " + inst.URL[strings.LastIndex(inst.URL, ":")+1:]} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("settings.yml lacks %q", want)
		}
	}
	secret, _ := os.ReadFile(filepath.Join(sd, "secret"))
	if len(secret) < 32 || !strings.Contains(string(yml), string(secret)) {
		t.Error("secret not saved or not in settings")
	}
	if _, err := os.Stat(filepath.Join(sd, "searxng.log")); err != nil {
		t.Error("no log file")
	}

	rec, _ := os.ReadFile(os.Getenv("FAKE_RECORD"))
	for _, want := range []string{"cwd=" + resolve(filepath.Join(arshafa, "src")), "pythonpath=" + filepath.Join(arshafa, "src"), "nobytecode=1", "seoxenv=0", "args=-m searx.webapp"} {
		if !strings.Contains(string(rec), want) {
			t.Errorf("start record lacks %q:\n%s", want, rec)
		}
	}

	inst.Stop()
	stopped = true
	if !waitGone(pid) {
		t.Fatal("process still alive after Stop")
	}
	if healthy(context.Background(), inst.URL) {
		t.Error("still healthy after Stop")
	}
	if _, err := os.Stat(sd); err == nil {
		t.Error("run folder left behind")
	}
	// seox never writes inside Arshafa's folder.
	after := snapshot(t, arshafa)
	if len(after) != len(before) {
		t.Fatalf("Arshafa dir changed: %d -> %d entries", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("Arshafa file changed: %s", p)
		}
	}
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func TestRunnerStopsOnContextCancel(t *testing.T) {
	home, _, cfg := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()
	pid := readPid(t, home)
	cancel()
	if !waitGone(pid) {
		t.Fatal("process survived ctx cancel")
	}
}

func TestRunnerKillsAfterGrace(t *testing.T) {
	home, _, cfg := setup(t)
	t.Setenv("FAKE_IGNORE_TERM", "1")
	old := termGrace
	termGrace = 400 * time.Millisecond
	defer func() { termGrace = old }()
	inst, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pid := readPid(t, home)
	start := time.Now()
	inst.Stop()
	if !waitGone(pid) {
		t.Fatal("SIGKILL did not end the process")
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Error("killed before the grace period")
	}
}

// startDecoy runs a sleeping process whose command line and environment look
// like (or not like) a seox SearXNG, in its own group.
func startDecoy(t *testing.T, mark, settings string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60; true", mark)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd.Process.Pid
}

// writeRun makes searxng/run-<name> the way seox would, owned by owner.
func writeRun(t *testing.T, base, name string, child int, started string, owner int) string {
	t.Helper()
	d := filepath.Join(base, "run-"+name)
	_ = os.MkdirAll(d, 0o700)
	if started == "" {
		_, started = procInfo(child)
	}
	_, ost := procInfo(owner)
	_ = os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(child)+"\n"+started+"\n"+strconv.Itoa(owner)+"\n"+ost+"\n"), 0o600)
	return d
}

func TestRunnerStaleRuns(t *testing.T) {
	home, _, cfg := setup(t)
	base := filepath.Join(home, "searxng")
	_ = os.MkdirAll(base, 0o700)

	// The owner seox is gone: its leftover SearXNG is killed and its folder removed.
	dead := startDecoy(t, "searx.webapp", "")
	d1 := writeRun(t, base, "dead", dead, "", 2147483000)
	// The owner is alive (another seox run): hands off.
	live := startDecoy(t, "searx.webapp", "")
	owner := startDecoy(t, "seox-owner", "")
	d2 := writeRun(t, base, "live", live, "", owner)
	// Owner gone, but the pid now belongs to something else: not killed, folder removed.
	innocent := startDecoy(t, "something-else", "")
	d3 := writeRun(t, base, "reused", innocent, "", 2147483000)
	// Owner gone and the start time differs (a reused pid): not killed.
	other := startDecoy(t, "searx.webapp", "")
	d4 := writeRun(t, base, "reused2", other, "Thu Jan  1 00:00:00 1970", 2147483000)

	inst, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()
	if !waitGone(dead) {
		t.Fatal("stale SearXNG not killed")
	}
	if !alive(live) {
		t.Fatal("killed a SearXNG whose seox is still running")
	}
	if !alive(innocent) || !alive(other) {
		t.Fatal("killed a process that is not seox's SearXNG")
	}
	for _, d := range []string{d1, d3, d4} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("stale folder %s not removed", d)
		}
	}
	if _, err := os.Stat(d2); err != nil {
		t.Error("a live run's folder was removed")
	}
}

func TestRunnerRetriesLostPort(t *testing.T) {
	home, _, cfg := setup(t)
	t.Setenv("FAKE_BINDFAIL_ONCE", filepath.Join(t.TempDir(), "once"))
	inst, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("no retry after a bind failure: %v", err)
	}
	defer inst.Stop()
	if !healthy(context.Background(), inst.URL) || readPid(t, home) == 0 {
		t.Fatal("retry did not give a running instance")
	}
}

func TestRunnerFailsFastWhenProcessDies(t *testing.T) {
	_, arshafa, cfg := setup(t)
	_ = os.WriteFile(filepath.Join(arshafa, "venv", "bin", "python"), []byte("#!/bin/sh\nexit 3\n"), 0o755)
	_, err := Open(context.Background(), cfg)
	if e := apperr.From(err); e.Code != apperr.SourceError {
		t.Fatalf("got %v", err)
	}
}

func TestConfiguredURLWinsAndFallsBack(t *testing.T) {
	var hits atomic.Int32
	srv := fakeSearx(t, &hits)
	home, _, cfg := setup(t)
	cfg.SearxngURL = srv.URL + "/"
	inst, err := Open(context.Background(), cfg)
	if err != nil || inst.Own() || inst.URL != srv.URL {
		t.Fatalf("configured URL not used as is: %+v %v", inst, err)
	}
	if _, err := os.Stat(filepath.Join(home, "searxng")); err == nil {
		t.Error("started its own copy although searxng_url is healthy")
	}
	// A dead configured URL falls back to the install, with a note.
	cfg.SearxngURL = "http://127.0.0.1:1"
	inst, err = Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop()
	if !inst.Own() || len(inst.Notes) != 1 {
		t.Fatalf("fallback: %+v", inst)
	}
}
