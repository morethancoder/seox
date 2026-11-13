//go:build live

package mine

import (
	"context"
	"testing"
	"time"
)

// TestLiveMine runs against the real Search Console when this machine is
// signed in (seox login). Without a sign-in it skips, so `make live` is clean
// before any site is verified.
func TestLiveMine(t *testing.T) {
	if !signedIn() {
		t.Skip("not signed in to Google (run `seox login`); skipping the live Search Console test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	token, err := accessToken(ctx)
	if err != nil {
		t.Skipf("sign-in unusable: %v", err)
	}
	sites, err := listSites(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) == 0 {
		t.Skip("signed in but the account has no Search Console properties")
	}
	rep, err := Mine(ctx, "queries", sites[0].URL, Options{Limit: 5, NoCache: true})
	if err != nil {
		t.Fatalf("%s: %v", sites[0].URL, err)
	}
	t.Logf("%s: source %s, %d warnings", sites[0].URL, rep.Source, len(rep.Warnings))
}
