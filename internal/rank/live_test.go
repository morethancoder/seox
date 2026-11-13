//go:build live

package rank

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/config"
)

// TestLiveRankExampleCom runs Arshafa's real SearXNG and checks it is gone afterwards.
func TestLiveRankExampleCom(t *testing.T) {
	cfg := &config.Config{}
	if !installed(cfg.ArshafaDir()) {
		t.Skip("Arshafa's SearXNG is not installed")
	}
	count := func() int {
		out, _ := exec.Command("pgrep", "-f", "searx.webapp").Output()
		return len(strings.Fields(string(out)))
	}
	before := count()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := Positions(ctx, "example.com", []string{"example domain"}, Options{Config: cfg, NoCache: true, Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	k := res.Keywords[0]
	t.Logf("result: %+v best=%v warnings=%v", k, k.BestPosition, res.Warnings)
	if k.BestPosition == nil && k.Note == "" {
		t.Fatal("neither a position nor a note")
	}
	// Another app may run its own SearXNG; what matters is that seox left none behind.
	if after := count(); after != before {
		t.Fatalf("searx.webapp processes: %d before, %d after", before, after)
	}
}
