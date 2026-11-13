package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAndSet(t *testing.T) {
	t.Setenv("SEOX_HOME", t.TempDir())
	c, err := Load()
	if err != nil || c.Crawl() != 200 || c.ArshafaDir() == "" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	if err := c.Set("crawl_max", "50"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("searxng_url", "http://127.0.0.1:8888/"); err != nil {
		t.Fatal(err)
	}
	if c.Set("nope", "x") == nil || c.Set("crawl_max", "x") == nil || c.Set("searxng_url", "ftp://x") == nil {
		t.Fatal("bad input accepted")
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, _ := Load()
	if c2.Crawl() != 50 || c2.SearxngURL != "http://127.0.0.1:8888" {
		t.Fatalf("not saved: %+v", c2)
	}
}

func TestKeyFromEnv(t *testing.T) {
	t.Setenv("SEOX_NO_KEYCHAIN", "1")
	t.Setenv("SEOX_BING_KEY", " abc ")
	if k, w := Key("bing"); k != "abc" || w != "env" {
		t.Fatalf("%q %q", k, w)
	}
	if k, _ := Key("google"); k != "" {
		t.Fatal("google key should be empty")
	}
	if !ValidKeyName("openpagerank") || ValidKeyName("x") {
		t.Fatal("ValidKeyName")
	}
}

func TestPathTightensExistingHome(t *testing.T) {
	h := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(h, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEOX_HOME", h)
	Path("x")
	if st, _ := os.Stat(h); st.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
}
