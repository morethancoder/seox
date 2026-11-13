//go:build live

package signals

import (
	"context"
	"testing"
	"time"
)

func liveSetup(t *testing.T) context.Context {
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	t.Setenv("SEOX_GOOGLE_API_KEY", "")
	t.Setenv("SEOX_OPENPAGERANK_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLiveSpeedLocalLighthouse(t *testing.T) {
	res, err := Speed(liveSetup(t), "https://example.com", SpeedOptions{Strategy: "mobile", NoCache: true})
	if err != nil {
		t.Skipf("local Lighthouse unavailable: %v", err)
	}
	if res.Mobile == nil || res.Mobile.Score == nil || res.Mobile.Source != "lighthouse-local" {
		t.Fatalf("no score: %+v", res)
	}
	t.Logf("mobile score %d, lab %+v, opps %+v, warnings %v", *res.Mobile.Score, res.Mobile.Lab, res.Mobile.Opportunities, res.Warnings)
}

func TestLiveAuthorityWayback(t *testing.T) {
	res, err := Authority(liveSetup(t), []string{"example.com"}, AuthorityOptions{NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Domains[0]
	if d.FirstSeen == "" || d.Score != nil {
		t.Fatalf("%+v warnings %v", d, res.Warnings)
	}
	t.Logf("%+v approx=%v", d, *d.SnapshotsApprox)
}

func TestLiveIdeas(t *testing.T) {
	res, err := Ideas(liveSetup(t), "seo audit", IdeasOptions{NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total == 0 {
		t.Fatalf("no ideas: %+v", res.Engines)
	}
	t.Logf("total %d q%d c%d m%d r%d engines %+v warnings %v", res.Total, len(res.Questions), len(res.Comparisons), len(res.Modifiers), len(res.Related), res.Engines, res.Warnings)
}
