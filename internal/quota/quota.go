// Package quota counts calls to a rate-limited source per day, one named bucket
// per source (for example "psi", "crux", "gsc"), with a limit you pass in.
// The count is seox's own estimate: the sources have no API to read it. It is
// shared between processes through a file lock, so several agents running seox
// at once don't lose each other's counts. The day is a Pacific day, which is
// when Google's quotas reset.
package quota

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
)

type state struct {
	Day  string `json:"day"`
	Used int    `json:"used"`
}

// Tracker is the daily counter. It is safe for concurrent use in one process.
type Tracker struct {
	mu    *sync.Mutex // shared by every Tracker on the same file, so parallel calls don't lose counts
	path  string
	limit int
	now   func() time.Time
}

var pacific = func() *time.Location {
	if l, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return l
	}
	return time.FixedZone("PT", -8*3600)
}()

var (
	locksMu sync.Mutex
	locks   = map[string]*sync.Mutex{}
)

// lockFor returns the one mutex for a quota file in this process.
func lockFor(path string) *sync.Mutex {
	locksMu.Lock()
	defer locksMu.Unlock()
	if m := locks[path]; m != nil {
		return m
	}
	m := &sync.Mutex{}
	locks[path] = m
	return m
}

// New returns the tracker for a named bucket, stored in seox's home, with the
// given daily limit.
func New(bucket string, limit int) *Tracker {
	p := config.Path("quota-" + bucket + ".json")
	return &Tracker{mu: lockFor(p), path: p, limit: limit, now: time.Now}
}

// NewAt is New with an explicit file and clock (tests).
func NewAt(path string, limit int, now func() time.Time) *Tracker {
	return &Tracker{mu: lockFor(path), path: path, limit: limit, now: now}
}

// lock takes the in-process mutex, then an flock on "<file>.lock", so parallel ytx
// processes (several agents at once) don't lose each other's units in a read-add-write.
// It returns the unlock. If the lock file can't be opened, the mutex alone still guards
// this process: losing a count is better than refusing to run.
func (t *Tracker) lock() func() {
	t.mu.Lock()
	_ = os.MkdirAll(filepath.Dir(t.path), 0o700)
	f, err := os.OpenFile(t.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil && syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		f = nil
	}
	return func() {
		if err == nil && f != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		}
		t.mu.Unlock()
	}
}

func (t *Tracker) day() string { return t.now().In(pacific).Format("2006-01-02") }

func (t *Tracker) load() state {
	var s state
	if b, err := os.ReadFile(t.path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.Day != t.day() {
		s = state{Day: t.day()}
	}
	return s
}

func (t *Tracker) save(s state) {
	b, _ := json.Marshal(s)
	_ = config.WriteFileAtomic(t.path, b, 0o600)
}

// Used is today's count.
func (t *Tracker) Used() int { defer t.lock()(); return t.load().Used }

// Limit is the daily limit.
func (t *Tracker) Limit() int { return t.limit }

// Left is today's calls still available.
func (t *Tracker) Left() int {
	defer t.lock()()
	if l := t.limit - t.load().Used; l > 0 {
		return l
	}
	return 0
}

// Spend reserves n calls, or returns quota_exceeded without spending when they don't fit.
func (t *Tracker) Spend(n int) error {
	defer t.lock()()
	s := t.load()
	if s.Used+n > t.limit {
		return apperr.New(apperr.QuotaExceeded, "daily limit would be passed (%d of %d used today, this call needs %d)", s.Used, t.limit, n)
	}
	s.Used += n
	t.save(s)
	return nil
}

// Exhaust marks today's quota as used up (the source answered that it is).
func (t *Tracker) Exhaust() {
	defer t.lock()()
	s := t.load()
	s.Used = t.limit
	t.save(s)
}

// Resets is when the quota next resets.
func (t *Tracker) Resets() time.Time {
	n := t.now().In(pacific)
	return time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, pacific)
}
