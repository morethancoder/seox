//go:build live

package report

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/ops"
)

func TestLiveReport(t *testing.T) {
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out := t.TempDir()
	res, err := handleReport(ctx, ops.Input{Op: ops.Find("report"), Given: map[string]bool{},
		Values: map[string]any{"site": "example.com", "max": 5, "out": out}})
	if err != nil {
		t.Fatal(err)
	}
	data := res.Data.(*ReportResult)
	b, err := os.ReadFile(data.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "type: seo-report") || data.Pages == 0 {
		t.Fatalf("bad report: %+v", data)
	}
	t.Logf("wrote %s: score %d, %d pages, warnings %v", data.Path, data.Score, data.Pages, res.Warnings)
}
