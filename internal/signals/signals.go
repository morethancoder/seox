// Package signals is seox's area for speed, authority and ideas.
//
// It owns the ops: speed, authority, ideas. Each has a typed Go API (Speed,
// Authority, Ideas) that internal/report calls directly; the handlers are thin
// wrappers around them.
package signals

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/web"
)

func init() {
	ops.Handle("speed", handleSpeed)
	ops.Handle("authority", handleAuthority)
	ops.Handle("ideas", handleIdeas)
}

// getJSON fetches a URL and decodes a 2xx JSON body into out. It returns the status;
// a non-2xx status is returned with a nil error so callers can map it.
func getJSON(ctx context.Context, u string, max int64, out any) (int, error) {
	r, err := web.Get(ctx, u, max)
	if err != nil {
		return 0, err
	}
	if r.Status/100 != 2 {
		return r.Status, nil
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return r.Status, apperr.Wrap(apperr.SourceError, err, "unreadable answer from %s", hostOf(u))
	}
	return r.Status, nil
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return u
}

// cleanErr hides API keys from any message.
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "***")
}

func ttl(h int) time.Duration { return time.Duration(h) * time.Hour }

var nanVal = math.NaN()

func baseURL(name, def string) string { return web.BaseURL(name, def) }

func doReq(ctx context.Context, req *http.Request, max int64) (*web.Response, error) {
	return web.Do(ctx, req, max)
}

func doGet(ctx context.Context, u string, max int64) (*web.Response, error) {
	return web.Get(ctx, u, max)
}
