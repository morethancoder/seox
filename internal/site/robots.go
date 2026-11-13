package site

import (
	"bufio"
	"strconv"
	"strings"
	"time"
)

// robotsRules is the part of robots.txt that applies to seox.
type robotsRules struct {
	allow, disallow []string
	CrawlDelay      time.Duration
	Sitemaps        []string
	Group           string // "seox", "*" or "" when no group matched
}

// parseRobots reads robots.txt and keeps the `seox` group when there is one,
// else the `*` group (RFC 9309: the most specific agent group wins, groups for
// the same agent are merged).
func parseRobots(body string) *robotsRules {
	type group struct {
		agents          []string
		allow, disallow []string
		delay           time.Duration
	}
	body = strings.TrimPrefix(body, "\ufeff") // a BOM would glue itself to the first key
	var groups []*group
	var cur *group
	inRules := false
	rules := &robotsRules{}
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val := strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch key {
		case "user-agent":
			if cur == nil || inRules {
				cur = &group{}
				groups = append(groups, cur)
				inRules = false
			}
			cur.agents = append(cur.agents, strings.ToLower(val))
		case "allow", "disallow":
			if cur == nil {
				continue
			}
			inRules = true
			if val == "" {
				continue // an empty Disallow allows everything
			}
			if key == "allow" {
				cur.allow = append(cur.allow, normalizeRobotsPath(val))
			} else {
				cur.disallow = append(cur.disallow, normalizeRobotsPath(val))
			}
		case "crawl-delay":
			if cur == nil {
				continue
			}
			inRules = true
			if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 {
				cur.delay = time.Duration(f * float64(time.Second))
			}
		case "sitemap":
			if val != "" {
				rules.Sitemaps = append(rules.Sitemaps, val)
			}
		}
	}
	pick := func(agent string) bool {
		found := false
		for _, g := range groups {
			for _, a := range g.agents {
				if a == agent {
					found = true
					rules.allow = append(rules.allow, g.allow...)
					rules.disallow = append(rules.disallow, g.disallow...)
					if g.delay > rules.CrawlDelay {
						rules.CrawlDelay = g.delay
					}
					break
				}
			}
		}
		return found
	}
	switch {
	case pick("seox"):
		rules.Group = "seox"
	case pick("*"):
		rules.Group = "*"
	}
	return rules
}

// allowed reports whether seox may fetch a path (with its query). The longest
// matching rule wins and Allow beats Disallow on a tie.
func (r *robotsRules) allowed(pathQuery string) bool {
	if r == nil {
		return true
	}
	pathQuery = normalizeRobotsPath(pathQuery)
	bestAllow, bestDis := -1, -1
	for _, p := range r.allow {
		if n := matchLen(p, pathQuery); n > bestAllow {
			bestAllow = n
		}
	}
	for _, p := range r.disallow {
		if n := matchLen(p, pathQuery); n > bestDis {
			bestDis = n
		}
	}
	if bestDis < 0 {
		return true
	}
	return bestAllow >= bestDis
}

// blocksAll reports whether the group closes the whole site.
func (r *robotsRules) blocksAll() bool { return !r.allowed("/") }

// matchLen returns how many characters of the pattern matched the start of s
// (so longer patterns are more specific), or -1 for no match. It supports the
// * wildcard and a trailing $ anchor.
func matchLen(pattern, s string) int {
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")
	parts := strings.Split(pattern, "*")
	pos := 0
	for i, part := range parts {
		if i == 0 {
			if !strings.HasPrefix(s, part) {
				return -1
			}
			pos = len(part)
			continue
		}
		idx := strings.Index(s[pos:], part)
		if idx < 0 {
			return -1
		}
		pos += idx + len(part)
	}
	if anchored && len(parts) > 0 {
		last := parts[len(parts)-1]
		if len(parts) == 1 {
			if pos != len(s) {
				return -1
			}
		} else if !strings.HasSuffix(s, last) {
			return -1
		}
	}
	return len(strings.ReplaceAll(pattern, "*", ""))
}

// normalizeRobotsPath puts a rule or a request path in one form so they can be
// compared (RFC 9309 2.2.2): non-ASCII bytes are percent-encoded, escapes of
// unreserved characters are decoded and the hex of the rest is uppercase.
// The * and $ of a rule pass through unchanged.
func normalizeRobotsPath(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) && isHexByte(s[i+1]) && isHexByte(s[i+2]) {
			v := unhex(s[i+1])<<4 | unhex(s[i+2])
			if isUnreserved(v) {
				b.WriteByte(v)
			} else {
				b.WriteByte('%')
				b.WriteByte(hex[v>>4])
				b.WriteByte(hex[v&15])
			}
			i += 2
			continue
		}
		if c >= 0x80 {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

func isUnreserved(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~'
}
