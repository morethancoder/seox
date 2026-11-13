package rank

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/web"
)

// Engines is the keep_only list written into seox's own SearXNG settings.
// It is a const for now; making it configurable is a later step.
const Engines = "google, bing, brave, duckduckgo, startpage, mojeek, qwant"

// MissingFix is the fix text of the tool_missing error.
const MissingFix = "install web search in Arshafa (Settings > AI > Web search), or set a SearXNG URL with `seox config set searxng_url <url>`"

// Timings; vars so tests can shorten them.
var (
	startTimeout = 40 * time.Second
	termGrace    = 3 * time.Second
	pollEvery    = 250 * time.Millisecond
)

// Instance is a SearXNG that rank can query. Stop it when done; for a
// configured searxng_url it is a no-op, for seox's own copy it ends the process.
type Instance struct {
	URL string
	// Notes explain choices worth showing as warnings (e.g. the configured URL was down).
	Notes []string

	own      bool
	stopOnce sync.Once
	stopFn   func()
}

// Own reports whether seox started this instance.
func (i *Instance) Own() bool { return i.own }

// Stop ends the instance if seox started it. It is safe to call twice.
func (i *Instance) Stop() {
	i.stopOnce.Do(func() {
		if i.stopFn != nil {
			i.stopFn()
		}
	})
}

// Open finds a SearXNG: the configured searxng_url when it answers /healthz,
// otherwise Arshafa's install, started as seox's own copy. The caller must
// Stop the result; it is also stopped when ctx ends.
func Open(ctx context.Context, cfg *config.Config) (*Instance, error) {
	var notes []string
	if u := strings.TrimRight(cfg.SearxngURL, "/"); u != "" {
		if healthy(ctx, u) {
			return &Instance{URL: u}, nil
		}
		if ctx.Err() != nil {
			return nil, apperr.From(ctx.Err())
		}
		notes = append(notes, "configured searxng_url "+u+" did not answer /healthz")
	}
	dir := cfg.ArshafaDir()
	if !installed(dir) {
		msg := "no SearXNG found: " + MissingFix
		if len(notes) > 0 {
			msg = notes[0] + " and Arshafa's SearXNG is not installed at " + dir + ": " + MissingFix
		}
		return nil, apperr.New(apperr.ToolMissing, "%s", msg)
	}
	inst, err := startOwn(ctx, dir, filepath.Join(config.Home(), "searxng"))
	if err != nil {
		return nil, err
	}
	inst.Notes = notes
	return inst, nil
}

// installed mirrors Arshafa's own check. It only reads.
func installed(dir string) bool {
	isFile := func(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
	st, err := os.Stat(filepath.Join(dir, "src", "searx"))
	return isFile(filepath.Join(dir, "installed.json")) && isFile(filepath.Join(dir, "venv", "bin", "python")) && err == nil && st.IsDir()
}

func healthy(ctx context.Context, base string) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := web.Get(cctx, base+"/healthz", 1024)
	return err == nil && r.Status >= 200 && r.Status < 300
}

// settingsYAML is SearXNG's settings: only the chosen engines, JSON on, the
// limiter off (it only suits public instances), bound to loopback on port.
func settingsYAML(secret string, port int) string {
	return fmt.Sprintf("use_default_settings:\n  engines:\n    keep_only: [%s]\ngeneral:\n  instance_name: \"seox search\"\n  debug: false\n  enable_metrics: false\nsearch:\n  safe_search: 1\n  autocomplete: \"\"\n  formats: [html, json]\nserver:\n  bind_address: \"127.0.0.1\"\n  port: %d\n  secret_key: \"%s\"\n  limiter: false\n  public_instance: false\n  image_proxy: false\nouter_network:\n  enable_ipv6: false\n", Engines, port, secret)
}

// secretIn keeps one random secret in the folder so the settings stay stable.
func secretIn(home string) (string, error) {
	f := filepath.Join(home, "secret")
	if b, err := os.ReadFile(f); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	s := hex.EncodeToString(buf)
	return s, config.WriteFileAtomic(f, []byte(s), 0o600)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// procInfo is what ps says about a pid: its command line and start time.
func procInfo(pid int) (command, started string) {
	run := func(col string) string {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-ww", "-o", col+"=").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return run("command"), run("lstart")
}

// Each run lives in its own folder base/run-<seox pid>, so two seox processes
// never share a pid file, settings or log. The pid file has four lines: the
// SearXNG pid and its start time, then the owning seox pid and its start time.
// macOS ps does not show a process's environment, so start times are what tie
// a pid to the process seox started (a reused pid has a different one).
func writePid(file string, child, owner int) {
	_, cs := procInfo(child)
	_, ost := procInfo(owner)
	_ = os.WriteFile(file, []byte(fmt.Sprintf("%d\n%s\n%d\n%s\n", child, cs, owner, ost)), 0o600)
}

// reapStale cleans run folders whose owning seox process is gone: it stops a
// SearXNG left behind (only when ps shows searx.webapp that started at the
// recorded time) and removes the folder. A run whose owner is alive is never touched.
func reapStale(base string) {
	dirs, _ := filepath.Glob(filepath.Join(base, "run-*"))
	for _, d := range dirs {
		reapDir(d)
	}
}

func reapDir(dir string) {
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	var f []string
	if err == nil {
		f = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
	if len(f) >= 4 {
		if owner, err := strconv.Atoi(strings.TrimSpace(f[2])); err == nil {
			if _, st := procInfo(owner); st != "" && st == strings.TrimSpace(f[3]) {
				return // its seox is still running
			}
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(f[0])); err == nil && pid > 1 {
			command, started := procInfo(pid)
			if strings.Contains(command, "searx.webapp") && started != "" && started == strings.TrimSpace(f[1]) {
				terminate(pid, nil)
			}
		}
	} else if err == nil {
		// A pid file that cannot be trusted: leave the process alone, drop the folder.
		_ = os.RemoveAll(dir)
		return
	} else if _, serr := os.Stat(dir); serr == nil {
		// No pid file: a run that has not written it yet belongs to a live seox; only
		// remove folders that are clearly old.
		if st, _ := os.Stat(dir); st != nil && time.Since(st.ModTime()) < time.Minute {
			return
		}
	}
	_ = os.RemoveAll(dir)
}

// terminate sends SIGTERM to the process group (the pid leads its own group),
// then SIGKILL after the grace period. done, when given, closes on exit.
func terminate(pid int, done <-chan struct{}) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.After(termGrace)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			_ = syscall.Kill(-pid, syscall.SIGKILL) // stragglers in the group
			return
		case <-tick.C:
			if done == nil && syscall.Kill(pid, 0) != nil {
				return
			}
		case <-deadline:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
			return
		}
	}
}

// childEnv is the environment for SearXNG: ours without SEOX_* (API keys must
// not reach the child's process listing), plus what SearXNG needs.
func childEnv(settings, src string) []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "SEOX_") {
			env = append(env, e)
		}
	}
	return append(env, "SEARXNG_SETTINGS_PATH="+settings, "PYTHONPATH="+src, "PYTHONDONTWRITEBYTECODE=1")
}

// errBind marks a start that lost the port to another process.
var errBind = errors.New("port taken by another process")

// startOwn runs Arshafa's SearXNG with seox's own settings, pid and log in a
// run folder under base. Nothing is written inside arshafa. A lost port race
// is retried once on a new port.
func startOwn(ctx context.Context, arshafa, base string) (*Instance, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "cannot create %s", base)
	}
	reapStale(base)
	dir := filepath.Join(base, "run-"+strconv.Itoa(os.Getpid()))
	// A folder with our own pid is a leftover of an earlier process with this pid.
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "cannot create %s", dir)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		inst, err := startOnce(ctx, arshafa, dir)
		if err == nil {
			return inst, nil
		}
		lastErr = err
		if !errors.Is(err, errBind) {
			break
		}
	}
	_ = os.RemoveAll(dir)
	var ae *apperr.Error
	if errors.As(lastErr, &ae) {
		return nil, ae
	}
	return nil, apperr.Wrap(apperr.SourceError, lastErr, "SearXNG could not bind a port")
}

func startOnce(ctx context.Context, arshafa, dir string) (*Instance, error) {
	secret, err := secretIn(dir)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "cannot save the SearXNG secret")
	}
	port, err := freePort()
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "no free port")
	}
	settings := filepath.Join(dir, "settings.yml")
	if err := config.WriteFileAtomic(settings, []byte(settingsYAML(secret, port)), 0o600); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "cannot write %s", settings)
	}
	logPath := filepath.Join(dir, "searxng.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "cannot open %s", logPath)
	}
	defer logf.Close() // the child keeps its own copy

	// exec.Command, not CommandContext: stopping is ours, so the whole group gets SIGTERM first.
	cmd := exec.Command(filepath.Join(arshafa, "venv", "bin", "python"), "-m", "searx.webapp")
	cmd.Dir = filepath.Join(arshafa, "src")
	cmd.Env = childEnv(settings, filepath.Join(arshafa, "src"))
	cmd.Stdin = nil // /dev/null
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // its own group: a terminal Ctrl-C does not hit it twice
	if err := cmd.Start(); err != nil {
		return nil, apperr.Wrap(apperr.ToolMissing, err, "cannot start SearXNG from %s", arshafa)
	}
	pid := cmd.Process.Pid
	writePid(filepath.Join(dir, "pid"), pid, os.Getpid())

	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	inst := &Instance{URL: "http://127.0.0.1:" + strconv.Itoa(port), own: true}
	stopped := make(chan struct{})
	inst.stopFn = func() {
		defer close(stopped)
		select {
		case <-exited:
		default:
			terminate(pid, exited)
			<-exited
		}
		_ = os.RemoveAll(dir)
	}
	// Ctrl-C or a timeout cancels ctx; the server must not outlive it.
	go func() {
		select {
		case <-ctx.Done():
			inst.Stop()
		case <-stopped:
		}
	}()
	// stopKeep ends the child but leaves the folder, for a retry in the same one.
	stopKeep := func() {
		select {
		case <-exited:
		default:
			terminate(pid, exited)
			<-exited
		}
	}

	deadline := time.Now().Add(startTimeout)
	for {
		select {
		case <-exited:
			if bindFailed(logPath) {
				inst.stopOnce.Do(func() { close(stopped) })
				return nil, errBind
			}
			inst.Stop()
			return nil, apperr.New(apperr.SourceError, "SearXNG stopped right after starting; see %s", logPath)
		default:
		}
		if healthy(ctx, inst.URL) {
			// Whatever answers must be our child, not another process that took the port.
			if ownsPort(pid, port) && !bindFailed(logPath) {
				return inst, nil
			}
			stopKeep()
			inst.stopOnce.Do(func() { close(stopped) })
			return nil, errBind
		}
		if ctx.Err() != nil {
			inst.Stop()
			return nil, apperr.From(ctx.Err())
		}
		if time.Now().After(deadline) {
			inst.Stop()
			return nil, apperr.New(apperr.SourceError, "SearXNG did not answer /healthz within %s; see %s", startTimeout, logPath)
		}
		time.Sleep(pollEvery)
	}
}

func bindFailed(logPath string) bool {
	b, _ := os.ReadFile(logPath)
	l := strings.ToLower(string(b))
	return strings.Contains(l, "address already in use") || strings.Contains(l, "errno 48") || strings.Contains(l, "errno 98")
}

// ownsPort asks lsof who listens on the port. It only says no when lsof names
// other processes and not the child; when lsof is missing or silent it accepts.
func ownsPort(pid, port int) bool {
	out, err := exec.Command("lsof", "-nP", "-t", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN").Output()
	if err != nil && len(out) == 0 {
		return true
	}
	pids := strings.Fields(string(out))
	if len(pids) == 0 {
		return true
	}
	for _, p := range pids {
		if p == strconv.Itoa(pid) {
			return true
		}
	}
	return false
}
