package quota

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSpendLimitAndNewDay(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	tr := NewAt(filepath.Join(t.TempDir(), "q.json"), 3, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if err := tr.Spend(1); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Spend(1); err == nil || tr.Left() != 0 || tr.Used() != 3 {
		t.Fatalf("4th call should be refused (left %d used %d)", tr.Left(), tr.Used())
	}
	now = now.Add(48 * time.Hour)
	if tr.Used() != 0 || tr.Left() != 3 {
		t.Fatal("a new day should reset the count")
	}
	tr.Exhaust()
	if tr.Left() != 0 {
		t.Fatal("Exhaust should use up the day")
	}
}

func TestBucketsAreSeparateFiles(t *testing.T) {
	t.Setenv("SEOX_HOME", t.TempDir())
	a, b := New("psi", 10), New("crux", 10)
	_ = a.Spend(4)
	if b.Used() != 0 || New("psi", 10).Used() != 4 {
		t.Fatal("buckets must not share a count, a fresh handle must read the same file")
	}
}

// Two handles on one bucket must not lose counts when used in parallel.
func TestParallelHandlesShareCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.json")
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = NewAt(path, 10000, time.Now).Spend(1) }()
	}
	wg.Wait()
	if got := NewAt(path, 10000, time.Now).Used(); got != 40 {
		t.Fatalf("used %d, want 40", got)
	}
}

// Separate processes share the count through the file lock. The test re-runs itself
// as child processes that each spend 25 on the same file.
func TestProcessesShareCount(t *testing.T) {
	if p := os.Getenv("SEOX_QUOTA_CHILD"); p != "" {
		tr := NewAt(p, 10000, time.Now)
		for i := 0; i < 25; i++ {
			if err := tr.Spend(1); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	path := filepath.Join(t.TempDir(), "q.json")
	var cmds []*exec.Cmd
	for i := 0; i < 4; i++ {
		c := exec.Command(os.Args[0], "-test.run=^TestProcessesShareCount$")
		c.Env = append(os.Environ(), "SEOX_QUOTA_CHILD="+path)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if got := NewAt(path, 10000, time.Now).Used(); got != 100 {
		t.Fatalf("used = %d, want 100 (4 processes x 25)", got)
	}
}
