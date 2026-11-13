package report

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morethancoder/seox/internal/apperr"
)

// slug turns a domain, or any text, into a safe file name part: lower-case
// letters, digits, dots and dashes only. A domain only ever reaches the file
// system through this, so "../../x" or "a/b" cannot leave the folder.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := b.String()
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", ".")
	}
	out = strings.Trim(out, "-.")
	if len(out) > 100 {
		out = strings.Trim(out[:100], "-.")
	}
	if out == "" {
		return "site"
	}
	return out
}

// writeNew writes content to <dir>/<domain>-seo-<date>.md without ever
// replacing a file: O_EXCL decides, and a taken name gets -2, -3 and so on.
func writeNew(dir, domain, date, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", apperr.Wrap(apperr.Internal, err, "cannot create the report folder %s", dir)
	}
	base := slug(domain) + "-seo-" + date
	for n := 1; n < 10000; n++ {
		name := base + ".md"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.md", base, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", apperr.Wrap(apperr.Internal, err, "cannot write %s", path)
		}
		_, werr := f.WriteString(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(path)
			return "", apperr.Wrap(apperr.Internal, werr, "cannot write %s", path)
		}
		return path, nil
	}
	return "", apperr.New(apperr.Internal, "no free file name for %s in %s", base, dir)
}
