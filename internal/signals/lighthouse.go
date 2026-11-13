package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
	"github.com/morethancoder/seox/internal/ops"
)

// lighthousePin is the Lighthouse version npx installs when none is on PATH.
const lighthousePin = "13.6.0"

const (
	lighthouseMaxOut = 20 << 20
	macChrome        = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
)

// lighthouseCmd returns the program and its leading args. SEOX_LIGHTHOUSE overrides everything,
// then `lighthouse` on PATH, then `npx --yes lighthouse@<pin>`.
func lighthouseCmd() (string, []string, error) {
	if p := strings.TrimSpace(os.Getenv("SEOX_LIGHTHOUSE")); p != "" {
		return p, nil, nil
	}
	if p, err := exec.LookPath("lighthouse"); err == nil {
		return p, nil, nil
	}
	if p, err := exec.LookPath("npx"); err == nil {
		return p, []string{"--yes", "lighthouse@" + lighthousePin}, nil
	}
	return "", nil, apperr.New(apperr.ToolMissing, "local Lighthouse needs Node.js. Fix: install Node (brew install node) or run npm i -g lighthouse, or set a google key: seox key set google")
}

func chromePath() (string, error) {
	p := strings.TrimSpace(os.Getenv("CHROME_PATH"))
	if p == "" {
		p = macChrome
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", apperr.New(apperr.ToolMissing, "local Lighthouse needs Google Chrome (looked at %s). Fix: install Chrome or set CHROME_PATH, or set a google key: seox key set google", p)
	}
	return p, nil
}

func init() {
	// Without a google key, `speed` runs Lighthouse locally (Google allows no keyless PageSpeed calls).
	ops.DoctorCheck("lighthouse", func() ops.Check {
		detail, err := LighthouseStatus()
		if err != nil {
			return ops.Check{Detail: err.Error(), Fix: "brew install node (npx fetches Lighthouse), and install Google Chrome or set CHROME_PATH"}
		}
		return ops.Check{OK: true, Detail: detail + " (used for `speed` when there is no google key)"}
	})
}

// LighthouseStatus says how `speed` would run Lighthouse locally, for doctor. It starts nothing.
func LighthouseStatus() (detail string, err error) {
	prog, args, err := lighthouseCmd()
	if err != nil {
		return "", err
	}
	chrome, err := chromePath()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(prog+" "+strings.Join(args, " ")) + " with Chrome at " + chrome, nil
}

type capWriter struct {
	buf   bytes.Buffer
	max   int
	extra bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		w.extra = true
		return len(p), nil // keep draining so the child is not blocked
	}
	return w.buf.Write(p)
}

// runLocalLighthouse runs one Lighthouse performance audit in headless Chrome and parses it
// like a PSI lighthouseResult. The process group is killed when ctx ends.
func runLocalLighthouse(ctx context.Context, pageURL, strategy string) (*PSIResult, error) {
	if !strings.HasPrefix(pageURL, "http://") && !strings.HasPrefix(pageURL, "https://") {
		return nil, apperr.New(apperr.InvalidInput, "not an http(s) URL: %q", pageURL)
	}
	bin, lead, err := lighthouseCmd()
	if err != nil {
		return nil, err
	}
	chrome, err := chromePath()
	if err != nil {
		return nil, err
	}
	// One local Lighthouse at a time, across processes: parallel Chromes skew each other's scores.
	unlock, err := lockLighthouse(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if ctx.Err() != nil {
		return nil, apperr.From(ctx.Err())
	}
	profile, err := os.MkdirTemp("", "seox-chrome-")
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "temp profile")
	}
	defer os.RemoveAll(profile)
	// npx runs from a fresh empty folder, never the user's (no planted .npmrc or node_modules).
	work, err := os.MkdirTemp("", "seox-lh-cwd-")
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "temp folder")
	}
	defer os.RemoveAll(work)
	args := append(append([]string{}, lead...), pageURL, "--output=json", "--output-path=stdout", "--quiet",
		"--chrome-flags=--headless=new --user-data-dir="+profile, "--only-categories=performance")
	if strategy == "desktop" {
		args = append(args, "--preset=desktop")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = work
	cmd.Env = append(childEnv(), "CHROME_PATH="+chrome)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	out, errOut := &capWriter{max: lighthouseMaxOut}, &capWriter{max: 4096}
	cmd.Stdout, cmd.Stderr = out, errOut
	runErr := cmd.Run()
	// Whatever happened, make sure nothing started from this run's profile survives.
	killProfileProcs(cmd, profile)
	if ctx.Err() != nil {
		return nil, apperr.From(ctx.Err())
	}
	if out.extra {
		return nil, apperr.New(apperr.SourceError, "Lighthouse output passed %d MB", lighthouseMaxOut>>20)
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) || errors.Is(runErr, exec.ErrWaitDelay) {
			msg := strings.TrimSpace(errOut.buf.String())
			if len(msg) > 300 {
				msg = msg[len(msg)-300:]
			}
			return nil, apperr.New(apperr.SourceError, "Lighthouse failed (%v): %s", runErr, msg)
		}
		return nil, apperr.Wrap(apperr.ToolMissing, runErr, "could not start Lighthouse")
	}
	var lh lhResult
	if err := json.Unmarshal(out.buf.Bytes(), &lh); err != nil || lh.Categories.Performance.Score == nil && len(lh.Audits) == 0 {
		return nil, apperr.New(apperr.SourceError, "unreadable Lighthouse output")
	}
	r := parsePSI(&psiResponse{Lighthouse: lh}, strategy)
	r.Source = "lighthouse-local"
	return r, nil
}

// childEnv is our environment without SEOX_* (API keys must not reach npx, Lighthouse or Chrome).
func childEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "SEOX_") {
			env = append(env, e)
		}
	}
	return env
}

// lockLighthouse takes an exclusive flock on <seox home>/lighthouse.lock, polling until ctx ends.
// The kernel drops the lock if the process dies.
func lockLighthouse(ctx context.Context) (func(), error) {
	home := config.Home()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "seox home")
	}
	f, err := os.OpenFile(filepath.Join(home, "lighthouse.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "lighthouse lock")
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			f.Close()
			return nil, apperr.Wrap(apperr.Internal, err, "lighthouse lock")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, apperr.From(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// killProfileProcs kills the process group and any Chrome still using this run's temp profile
// (chrome-launcher starts Chrome detached, outside our group).
func killProfileProcs(cmd *exec.Cmd, profile string) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	_ = exec.Command("pkill", "-KILL", "-f", "--", profile).Run()
}
