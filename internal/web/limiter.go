package web

import (
	"context"
	"strings"
	"sync"
	"time"
)

// The politeness limiter: per host, at most MaxConcurrent requests in flight
// and at least a minimum gap between request starts. It applies to every
// request through this package, crawls and API calls alike.
const (
	// MaxConcurrent is the in-flight limit per host.
	MaxConcurrent = 2
	// DefaultGap is the least time between two request starts to one host.
	DefaultGap = 250 * time.Millisecond
)

type hostState struct {
	sem  chan struct{}
	mu   sync.Mutex
	next time.Time     // earliest start of the next request
	gap  time.Duration // 0 = the default gap
}

var (
	hostsMu    sync.Mutex
	hosts      = map[string]*hostState{}
	defaultGap = DefaultGap
)

func hostKey(h string) string { return strings.ToLower(h) }

func stateFor(host string) *hostState {
	hostsMu.Lock()
	defer hostsMu.Unlock()
	k := hostKey(host)
	s := hosts[k]
	if s == nil {
		s = &hostState{sem: make(chan struct{}, MaxConcurrent)}
		hosts[k] = s
	}
	return s
}

// SetCrawlDelay raises the gap for one host to its robots.txt Crawl-delay. A
// delay below the default is ignored: the gap never drops under 250 ms. Pass
// the host as in the URL ("example.com" or "example.com:8080").
func SetCrawlDelay(host string, d time.Duration) {
	s := stateFor(host)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d > defaultGap {
		s.gap = d
	} else {
		s.gap = 0
	}
}

// SetMinGap changes the default gap for all hosts and returns the function
// that puts it back. Tests use SetMinGap(0) so they don't wait. Hosts with a
// Crawl-delay keep theirs.
func SetMinGap(d time.Duration) (restore func()) {
	hostsMu.Lock()
	old := defaultGap
	defaultGap = d
	hostsMu.Unlock()
	return func() {
		hostsMu.Lock()
		defaultGap = old
		hostsMu.Unlock()
	}
}

// acquire waits for a slot on the host and for its gap, and returns the release.
func acquire(ctx context.Context, host string) (func(), error) {
	s := stateFor(host)
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-s.sem }

	hostsMu.Lock()
	gap := defaultGap
	hostsMu.Unlock()
	s.mu.Lock()
	if s.gap > gap {
		gap = s.gap
	}
	now := time.Now()
	start := s.next
	if start.Before(now) {
		start = now
	}
	s.next = start.Add(gap)
	s.mu.Unlock()

	if !sleep(ctx, time.Until(start)) {
		release()
		return nil, ctx.Err()
	}
	return release, nil
}
