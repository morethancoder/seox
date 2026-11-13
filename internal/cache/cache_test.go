package cache

import (
	"testing"
	"time"
)

func TestPutGetExpiryClear(t *testing.T) {
	t.Setenv("SEOX_HOME", t.TempDir())
	k := Key("Video", "abc")
	if k == Key("Video", "abd") || k != Key("Video", "abc") {
		t.Fatal("keys must be stable and distinct")
	}
	var v map[string]int
	if _, ok := Get(k, time.Hour, &v); ok {
		t.Fatal("empty cache hit")
	}
	Put(k, "keyless", map[string]int{"n": 7})
	src, ok := Get(k, time.Hour, &v)
	if !ok || src != "keyless" || v["n"] != 7 {
		t.Fatalf("got %v %q %v", ok, src, v)
	}
	if _, ok := Get(k, -time.Second, &v); ok {
		t.Fatal("expired entry was returned")
	}
	n, err := Clear()
	if err != nil || n != 1 {
		t.Fatalf("Clear removed %d (%v), want 1", n, err)
	}
	if _, ok := Get(k, time.Hour, &v); ok {
		t.Fatal("hit after Clear")
	}
}
