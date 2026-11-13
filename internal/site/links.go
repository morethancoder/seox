package site

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/cache"
	"github.com/morethancoder/seox/internal/web"
)

const extTTL = 6 * time.Hour

// linkTimeout caps one outside-link attempt. A var so tests can shorten it.
var linkTimeout = 20 * time.Second

// retryPause is the wait before the one slower retry of a timed-out request.
var retryPause = 3 * time.Second

// isTimeout says whether err is a timeout rather than a real refusal.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ae *apperr.Error
	if errors.As(err, &ae) && ae.Code == apperr.Timeout {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// checkHTTPRedirect asks whether the plain http:// home page sends visitors to https.
func (c *crawler) checkHTTPRedirect() {
	if !c.res.HTTPSHome || c.ctx.Err() != nil {
		return
	}
	u, err := url.Parse(c.start)
	if err != nil || u.Port() != "" {
		return
	}
	r, err := web.GetNoRedirect(c.ctx, "http://"+u.Host+"/", 64<<10)
	if err != nil {
		return
	}
	ok := r.Status >= 300 && r.Status < 400 && len(r.Header.Get("Location")) >= 8 && r.Header.Get("Location")[:8] == "https://"
	c.res.HTTPRedirects = &ok
}

// checkExternalLinks tests the first maxExternal distinct outside links.
func (c *crawler) checkExternalLinks() {
	var urls []string
	seen := map[string]bool{}
	for _, p := range c.res.Pages {
		for _, l := range p.Links {
			if l.Internal || seen[l.URL] {
				continue
			}
			seen[l.URL] = true
			urls = append(urls, l.URL)
		}
	}
	c.res.ExternalTotal = len(urls)
	if len(urls) > maxExternal {
		urls = urls[:maxExternal]
		c.res.Warnings = append(c.res.Warnings, "more outside links than the check limit; only the first 100 were tested")
	}
	c.res.External = CheckLinks(c.ctx, urls, false)
}

// CheckLinks tests each URL with HEAD and falls back to GET when HEAD fails
// or is refused (some servers answer HEAD with 403, 404 or 405). Answers are
// cached for 6 hours per URL unless noCache is set.
func CheckLinks(ctx context.Context, urls []string, noCache bool) map[string]ExtStatus {
	out := make(map[string]ExtStatus, len(urls))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, u := range urls {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			st := checkOne(ctx, u, noCache)
			mu.Lock()
			out[u] = st
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	if ctx.Err() != nil {
		// Answers cut short by a cancel are not "broken", so drop them.
		for u, st := range out {
			if st.Status == 0 {
				delete(out, u)
			}
		}
	}
	return out
}

func checkOne(ctx context.Context, u string, noCache bool) ExtStatus {
	key := cache.Key("site-ext", u)
	if !noCache {
		var st ExtStatus
		if _, ok := cache.Get(key, extTTL, &st); ok && !st.Timeout {
			return st
		}
	}
	st := attempt(ctx, u)
	if st.Timeout && ctx.Err() == nil {
		// One slower retry, after a pause so a busy host is not hit again at once.
		select {
		case <-ctx.Done():
		case <-time.After(retryPause):
			st = attempt(ctx, u)
		}
	}
	if ctx.Err() != nil {
		return ExtStatus{Error: ctx.Err().Error()}
	}
	cache.Put(key, "crawl", st)
	return st
}

// attempt tries HEAD, then GET when HEAD fails or is refused.
func attempt(parent context.Context, u string) ExtStatus {
	ctx, cancel := context.WithTimeout(parent, linkTimeout)
	defer cancel()
	st := ExtStatus{Method: "HEAD"}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err == nil {
		r, err := web.Do(ctx, req, 1<<10)
		if err == nil {
			st.Status = r.Status
		} else {
			st.Error, st.Timeout = err.Error(), isTimeout(err)
		}
	} else {
		st.Error = err.Error()
	}
	if st.Status == 0 || st.Status >= 400 {
		if parent.Err() != nil {
			return ExtStatus{Error: parent.Err().Error()}
		}
		if ctx.Err() != nil {
			// HEAD used the whole budget; a GET would only time out too.
			return st
		}
		r, err := web.Get(ctx, u, 64<<10)
		st = ExtStatus{Method: "GET"}
		if err != nil {
			st.Error, st.Timeout = err.Error(), isTimeout(err)
		} else {
			st.Status = r.Status
		}
	}
	return st
}

// linkBroken says whether an outside link status means the target is gone.
// 401, 403, 429 and 999 are bot blocks, not broken links.
func linkBroken(st ExtStatus) bool {
	if st.Status == 0 {
		return st.Error != "" && !st.Timeout
	}
	switch st.Status {
	case 401, 403, 429, 999:
		return false
	}
	return st.Status >= 400
}

// linkTimedOut says whether a link could not be checked because it timed out.
func linkTimedOut(st ExtStatus) bool { return st.Status == 0 && st.Timeout }
