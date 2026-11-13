package rank

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
)

// fakeSleep records the waits instead of sleeping.
func fakeSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	oldS, oldJ, oldM := sleep, jitter, jitterMax
	sleep = func(ctx context.Context, d time.Duration) bool { got = append(got, d); return ctx.Err() == nil }
	jitter = func(m time.Duration) time.Duration { return m / 2 }
	t.Cleanup(func() { sleep, jitter, jitterMax = oldS, oldJ, oldM })
	return &got
}

func TestPacerGapBetweenQueries(t *testing.T) {
	got := fakeSleep(t)
	p := &pacer{gap: 3 * time.Second}
	for i := 0; i < 3; i++ {
		if err := p.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// No wait before the first query; then gap plus jitter (1 s from the fake).
	if len(*got) != 2 || (*got)[0] != 4*time.Second || (*got)[1] != 4*time.Second {
		t.Fatalf("waits %v", *got)
	}
}

func TestPacerBacksOffOnceAndCaps(t *testing.T) {
	fakeSleep(t)
	p := &pacer{gap: 3 * time.Second}
	p.limited("google", "timeout")
	if p.slowed || p.gap != 3*time.Second {
		t.Fatal("a plain timeout must not slow down")
	}
	p.limited("google", "CAPTCHA")
	p.limited("google", "Suspended: CAPTCHA")
	if p.gap != 6*time.Second || !p.slowed {
		t.Fatalf("gap %v", p.gap)
	}
	for _, e := range []string{"bing", "brave", "ddg", "qwant", "yahoo"} {
		p.limited(e, "too many requests")
	}
	if p.gap != maxGap {
		t.Fatalf("gap %v, want cap %v", p.gap, maxGap)
	}
}

func TestPacerCancelDuringWait(t *testing.T) {
	// The real sleep must return at once on cancel.
	ctx, cancel := context.WithCancel(context.Background())
	p := &pacer{gap: time.Hour, started: true}
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	start := time.Now()
	err := p.wait(ctx)
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancel was not prompt")
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.Timeout {
		t.Fatalf("err %v", err)
	}
}
