// Package cache keeps read results on disk for a while (search a day, stats an
// hour) so repeated research doesn't spend quota or hit YouTube again.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morethancoder/seox/internal/config"
)

type entry struct {
	Saved  time.Time       `json:"saved"`
	Source string          `json:"source"`
	Data   json.RawMessage `json:"data"`
}

// Dir is the cache folder.
func Dir() string { return config.Path("cache") }

// Key builds a cache key from parts.
func Key(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

func path(key string) string { return filepath.Join(Dir(), key[:2], key+".json") }

// Get fills out with the cached value when it is younger than ttl. It returns
// the source that produced the value and whether it was found.
func Get(key string, ttl time.Duration, out any) (source string, ok bool) {
	b, err := os.ReadFile(path(key))
	if err != nil {
		return "", false
	}
	var e entry
	if json.Unmarshal(b, &e) != nil || time.Since(e.Saved) > ttl {
		return "", false
	}
	if json.Unmarshal(e.Data, out) != nil {
		return "", false
	}
	return e.Source, true
}

// Put stores a value. Errors are ignored: a cache that can't write just misses next time.
func Put(key, source string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	b, _ := json.Marshal(entry{Saved: time.Now(), Source: source, Data: data})
	p := path(key)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = config.WriteFileAtomic(p, b, 0o600)
}

// Clear deletes the whole cache and returns how many entries it removed.
func Clear() (int, error) {
	n := 0
	_ = filepath.WalkDir(Dir(), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n, os.RemoveAll(Dir())
}
