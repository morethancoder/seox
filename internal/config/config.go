// Package config holds seox's settings file and its secrets (API keys, the
// Google OAuth token and client) in the macOS Keychain. Secrets never go in a
// file in the repo or in config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/morethancoder/seox/internal/apperr"
)

// KeychainService is the Keychain service name every seox secret is stored under.
const KeychainService = "seox"

// Secret account names in the Keychain.
const (
	SecretGoogle       = "google_api_key"
	SecretBing         = "bing_key"
	SecretOpenPageRank = "openpagerank_key"
	SecretOAuthToken   = "oauth_token"  // JSON oauth2.Token from `seox login`
	SecretOAuthClient  = "oauth_client" // the Google Cloud OAuth client JSON
)

// KeyNames are the names `seox key set <name>` accepts, in display order.
var KeyNames = []string{"google", "bing", "openpagerank"}

type keyInfo struct{ Secret, Env, Help string }

var keyInfos = map[string]keyInfo{
	"google":       {SecretGoogle, "SEOX_GOOGLE_API_KEY", "free Google Cloud API key (PageSpeed Insights, CrUX)"},
	"bing":         {SecretBing, "SEOX_BING_KEY", "Bing Webmaster Tools API key (Bing Webmaster > Settings > API access)"},
	"openpagerank": {SecretOpenPageRank, "SEOX_OPENPAGERANK_KEY", "free Open PageRank key (domain authority score)"},
}

// Defaults.
const (
	DefaultCrawlMax          = 200
	DefaultArshafaSearxngDir = "~/Library/Application Support/app.arshafa.desktop/searxng"
)

// Config is ~/.seox/config.json.
type Config struct {
	// SearxngURL is a SearXNG instance to use for `rank`; empty means start Arshafa's copy.
	SearxngURL string `json:"searxng_url,omitempty"`
	// ReportDir is where `report` writes its note; empty means the current folder.
	ReportDir string `json:"report_dir,omitempty"`
	// CrawlMax is the default page cap for crawls (0 = DefaultCrawlMax).
	CrawlMax int `json:"crawl_max,omitempty"`
	// ArshafaSearxngDir is Arshafa's installed SearXNG (read only; seox never writes there).
	ArshafaSearxngDir string `json:"arshafa_searxng_dir,omitempty"`
}

// Keys lists the settable keys with their help, for `config set` and errors.
var Keys = map[string]string{
	"searxng_url":         "URL of a SearXNG instance for `rank` (default: start Arshafa's copy)",
	"report_dir":          "folder where `report` writes notes (default: the current folder)",
	"crawl_max":           "default page cap for crawls (default 200)",
	"arshafa_searxng_dir": "Arshafa's SearXNG install (default " + DefaultArshafaSearxngDir + ")",
}

// Home is seox's state folder: $SEOX_HOME, else ~/.seox.
func Home() string {
	if h := os.Getenv("SEOX_HOME"); h != "" {
		return h
	}
	d, err := os.UserHomeDir()
	if err != nil {
		return ".seox"
	}
	return filepath.Join(d, ".seox")
}

// Path returns a path inside Home, creating Home when needed.
func Path(parts ...string) string {
	_ = os.MkdirAll(Home(), 0o700)
	// MkdirAll leaves an existing folder as it was, so tighten one made earlier with a wider mode.
	_ = os.Chmod(Home(), 0o700)
	return filepath.Join(append([]string{Home()}, parts...)...)
}

func file() string { return Path("config.json") }

// Load reads the config; a missing file is an empty config.
func Load() (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(file())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("read %s: %w", file(), err)
	}
	return c, nil
}

// Save writes the config.
func (c *Config) Save() error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return writeFileAtomic(file(), append(b, '\n'), 0o600)
}

// Crawl returns the effective default crawl cap.
func (c *Config) Crawl() int {
	if c.CrawlMax > 0 {
		return c.CrawlMax
	}
	return DefaultCrawlMax
}

// ArshafaDir returns the effective Arshafa SearXNG folder, with ~ expanded.
func (c *Config) ArshafaDir() string {
	if c.ArshafaSearxngDir != "" {
		return ExpandHome(c.ArshafaSearxngDir)
	}
	return ExpandHome(DefaultArshafaSearxngDir)
}

// Effective is every key with the value seox will actually use.
func (c *Config) Effective() map[string]any {
	return map[string]any{
		"searxng_url":         c.SearxngURL,
		"report_dir":          c.ReportDir,
		"crawl_max":           c.Crawl(),
		"arshafa_searxng_dir": c.ArshafaDir(),
	}
}

// Set changes one key.
func (c *Config) Set(key, value string) error {
	switch key {
	case "searxng_url":
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value != "" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return apperr.New(apperr.InvalidInput, "searxng_url must start with http:// or https://")
		}
		c.SearxngURL = value
	case "report_dir":
		if value != "" {
			value = ExpandHome(value)
		}
		c.ReportDir = value
	case "crawl_max":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return apperr.New(apperr.InvalidInput, "crawl_max must be a whole number (0 = default %d)", DefaultCrawlMax)
		}
		c.CrawlMax = n
	case "arshafa_searxng_dir":
		c.ArshafaSearxngDir = value
	default:
		names := make([]string, 0, len(Keys))
		for k := range Keys {
			names = append(names, k)
		}
		sort.Strings(names)
		return apperr.New(apperr.InvalidInput, "unknown config key %q (one of: %s)", key, strings.Join(names, ", "))
	}
	return nil
}

// ExpandHome turns a leading ~ into the home folder.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteFileAtomic writes a file through a temp file and a rename.
func WriteFileAtomic(path string, b []byte, mode os.FileMode) error {
	return writeFileAtomic(path, b, mode)
}

// ---- secrets

// keychainOff is true when SEOX_NO_KEYCHAIN is set (tests, CI): secrets then come only from env.
func keychainOff() bool { return os.Getenv("SEOX_NO_KEYCHAIN") != "" }

// Secret reads a Keychain secret ("" when absent).
func Secret(account string) (string, error) {
	if keychainOff() {
		return "", nil
	}
	s, err := keyring.Get(KeychainService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return s, err
}

// SetSecret stores a Keychain secret.
func SetSecret(account, value string) error {
	if keychainOff() {
		return apperr.New(apperr.InvalidInput, "the Keychain is off (SEOX_NO_KEYCHAIN is set)")
	}
	return keyring.Set(KeychainService, account, value)
}

// DeleteSecret removes a Keychain secret; a missing one is not an error.
func DeleteSecret(account string) error {
	if keychainOff() {
		return nil
	}
	err := keyring.Delete(KeychainService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// ValidKeyName reports whether name is one of KeyNames.
func ValidKeyName(name string) bool { _, ok := keyInfos[name]; return ok }

// KeySecret is the Keychain account for a key name ("" when the name is unknown).
func KeySecret(name string) string { return keyInfos[name].Secret }

// KeyEnv is the environment variable that overrides a key name ("" when unknown).
func KeyEnv(name string) string { return keyInfos[name].Env }

// KeyHelp says what a key is for.
func KeyHelp(name string) string { return keyInfos[name].Help }

// Key returns a key by name ("google", "bing", "openpagerank"): the environment
// variable first, then the Keychain. where is "env", "keychain" or "".
func Key(name string) (key, where string) {
	info, ok := keyInfos[name]
	if !ok {
		return "", ""
	}
	if k := strings.TrimSpace(os.Getenv(info.Env)); k != "" {
		return k, "env"
	}
	if k, _ := Secret(info.Secret); k != "" {
		return strings.TrimSpace(k), "keychain"
	}
	return "", ""
}
