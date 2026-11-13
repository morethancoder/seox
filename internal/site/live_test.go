//go:build live

package site

import (
	"context"
	"testing"
	"time"
)

func TestLiveExampleCom(t *testing.T) {
	t.Setenv("SEOX_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := Audit(ctx, "example.com", AuditOptions{Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	if r.PagesCrawled < 1 || r.Score <= 0 || r.Score > 100 {
		t.Fatalf("%+v", r)
	}
	t.Logf("example.com: %d pages, score %d, %d issues", r.PagesCrawled, r.Score, len(r.Issues))
	p, err := PageAudit(ctx, "https://example.com/", PageOptions{NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Page.Title != "Example Domain" || len(p.Keywords) == 0 {
		t.Errorf("page %+v", p.Page)
	}
}

func TestLiveBlogMax20(t *testing.T) {
	t.Setenv("SEOX_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// A small, static blog that keeps its robots.txt and sitemap in order.
	r, err := Audit(ctx, "https://jvns.ca", AuditOptions{Max: 20})
	if err != nil {
		t.Fatal(err)
	}
	if r.PagesCrawled < 5 || r.PagesCrawled > 20 {
		t.Fatalf("pages %d", r.PagesCrawled)
	}
	k, err := Keywords(ctx, "https://jvns.ca", KeywordsOptions{Max: 20, Limit: 5})
	if err != nil || len(k.Keywords) == 0 || !k.Cached {
		t.Fatalf("keywords should come from the cached crawl: %v %+v", err, k)
	}
	t.Logf("%s: %d pages, score %d, %d issues, limited=%v", r.Start, r.PagesCrawled, r.Score, len(r.Issues), r.Limited)
}
