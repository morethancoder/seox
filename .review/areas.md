# seox review: site, signals, rank, mine and shared packages

Scope: internal/{site,signals,rank,mine,web,cli,config,cache,quota}. internal/report was skipped.
Checks run: `go vet ./...` is clean and `go test -race ./...` passes (with SEOX_HOME in a temp folder and SEOX_NO_KEYCHAIN=1).
Experiments ran as throwaway `_test.go` files in a scratchpad copy of the repo, which has since been deleted. Docs were checked on learn.microsoft.com (Bing Webmaster), developer.chrome.com (CrUX), the Lighthouse source on GitHub and keywordseverywhere.com (Open PageRank).

Counts: high 3, medium 7, low 10.

---

## High

### H1. The Bing API key leaks into error messages, warnings and the JSON envelope
- Where: internal/mine/bing.go:77-82 (`apikey` goes in the query string), internal/web/web.go:178-192 (`mapErr` adds `err.Error()`, and a `*url.Error` contains the full URL). The key then appears in internal/mine/verify.go:87 and internal/mine/mine.go:131, 205, 362, and in any error that reaches the envelope.
- Scenario: a DNS failure, TLS error, refused connection or client timeout on any Bing call. The JSON then shows `request failed: Get "https://ssl.bing.com/webmaster/api.svc/json/GetUserSites?apikey=<KEY>": ...`, and from there it lands in agent logs, transcripts and the report notes. PSI and CrUX redact their key with errRedact; Bing does not.
- Fix: in bingCall, run `errRedact(err, key)` on the transport error, or strip the query string from `url.Error.URL` inside web.mapErr for every caller.
- Status: VERIFIED. bingCall pointed at 127.0.0.1:1 gave `code=network_error msg=request failed: Get "http://127.0.0.1:1/json/GetUserSites?apikey=SECRETKEY123": dial tcp ... connection refused`.

### H2. Bing link counts are always empty: wrong JSON field name
- Where: internal/mine/bing.go:253-259. The code parses `Details[]`.
- Scenario: per the docs, GetLinkCounts returns `LinkCounts {Links: [{Count, Url}], TotalPages}`; `Details` belongs to `LinkDetails` (GetUrlLinks). Every real call therefore yields `TotalLinks 0, PagesWithLinks 0, Top []` with no warning, so a site with backlinks looks like it has none. The fixture testdata/mine/bing_linkcounts.json repeats the mistake (it even says `"__type":"LinkDetails"`), so the tests pass.
- Fix: change the tag to `json:"Links"`, and fix the fixture to match the docs sample.
- Status: VERIFIED in the docs (learn.microsoft.com, IWebmasterApi.GetLinkCounts JSON sample: `"Links":[{"Count":14,"Url":...}],"TotalPages":3`).

### H3. Two concurrent rank or compare runs kill each other's SearXNG
- Where: internal/rank/runner.go:207 (`killStale(home)`), plus 157-174, 216-221 and 257. Every run shares `$SEOX_HOME/searxng/{pid,settings.yml,searxng.log}`, and nothing locks them.
- Scenario: an agent runs `seox rank a.com ...` and `seox compare ...` in parallel. The second run's killStale reads the first run's pid file. The pid is a live searx.webapp process whose lstart matches, so it is SIGTERMed mid-query and the first run fails or returns partial ranks. The second run also overwrites settings.yml and truncates the first run's log (O_TRUNC). When the first run stops, it removes the second run's pid file (L257), so a crash of the second leaves an orphan that killStale can no longer find.
- Fix: take an flock on `searxng/lock` for the instance's lifetime and reuse a healthy running instance (or wait), or give each run its own folder (pid-<n>) and only kill entries whose owner process is gone.
- Status: VERIFIED by reading the code (not run, since starting SearXNG is not allowed).

## Medium

### M1. robots.txt rules with non-ASCII or differently cased percent-encoding are ignored
- Where: internal/site/robots.go:133-165 (matchLen compares raw strings) and internal/site/crawl.go:217-226 (pathQuery uses EscapedPath).
- Scenario: an Arabic site's robots.txt says `Disallow: /مدونة/`. The crawler compares it against `/%D9%85.../post`, finds no match, and crawls the disallowed section. `Disallow: /a%3cb` also fails to match `/a%3Cb`. RFC 9309 2.2.2 requires both sides to be normalised before comparison.
- Fix: before comparing, percent-encode non-ASCII bytes in the patterns, and decode unreserved %XX and uppercase the hex on both sides.
- Status: VERIFIED. allowed() returned true for the Arabic path and for the case mismatch.

### M2. A UTF-8 BOM at the start of robots.txt drops the first group
- Where: internal/site/robots.go:33-43. The key becomes `"﻿user-agent"`.
- Scenario: a robots.txt saved by Windows tools with a BOM, where the first group is the `*` group. Its rules are lost and seox crawls disallowed paths. The result also reports `Group ""`.
- Fix: `body = strings.TrimPrefix(body, "﻿")` before scanning.
- Status: VERIFIED. `"\xef\xbb\xbfUser-agent: *\nDisallow: /private"` gave `group="" allowed(/private/x)=true`.

### M3. PSI opportunities are mostly empty under Lighthouse 13
- Where: internal/signals/speed.go:356-360 (only `details.type=="opportunity" && overallSavingsMs>0`).
- Scenario: Lighthouse 13 (in PSI since October 2025) replaced render-blocking-resources, the image audits, document latency and others with `*-insight` audits. Those build `makeTableDetails` and put the savings in `metricSavings`, not `overallSavingsMs`. The top fixes (render blocking, image delivery, LCP request discovery) then never appear; only leftovers such as unused-javascript do. The output looks clean when it isn't.
- Fix: also collect audits with `score<1` and a non-empty `metricSavings` (LCP/FCP ms), ranked by metricSavings.LCP. Keep overallSavingsMs as a fallback.
- Status: VERIFIED against the Lighthouse source (core/audits/insights/insight-audit.js sets metricSavings and no overallSavingsMs; render-blocking-insight.js uses makeTableDetails). The live PSI shape was not fetched.

### M4. Cancelling or timing out during CrUX returns success and caches a partial result for 12 h
- Where: internal/signals/speed.go:462-464 and 488-489 (errors become warnings) and 216 (`cache.Put`).
- Scenario: the user presses Ctrl-C, or the op deadline hits during queryRecord or queryHistoryRecord. The result is `ok` with the warning "CrUX skipped: cancelled", not the `timeout` code (exit 7), and the incomplete result is cached, so the next 12 hours of runs return no CrUX. A one-strategy PSI failure is cached the same way.
- Fix: in runCrux, return ctx errors (apperr Timeout) and propagate them; skip cache.Put when ctx.Err()!=nil or when any part failed.
- Status: VERIFIED by reading the code.

### M5. A single-page audit sends HEAD/GET to internal URLs that robots.txt disallows
- Where: internal/site/api.go:276-290 (PageAudit gives every internal link to CheckLinks).
- Scenario: `seox audit-page https://x.com/p` links to /admin and /cart?add=..., which robots.txt disallows. seox requests them anyway, breaking its own politeness rule, and a GET fallback on action URLs can have side effects.
- Fix: filter internal targets through `c.rules.allowed(pathQuery(u))` and report the skipped ones the way the site crawl's BlockedRobots does.
- Status: VERIFIED by reading the code.

### M6. The crawl cache ignores the path and scheme of the given URL
- Where: internal/site/crawl.go:95-98 (key = hostKey + max + depth).
- Scenario: `seox audit example.com` followed within the TTL by `seox audit http://example.com/blog/` (or the reverse) returns the first crawl, with the wrong start page, the wrong https-missing verdict and the wrong section. Nothing says the cached result came from a different start URL.
- Fix: include the scheme and the normalised start path in the key, or normalise every start URL to the root and say so.
- Status: VERIFIED by reading the code.

### M7. Bing keyword stats ignore `--days`
- Where: internal/mine/bing.go:294-305.
- Scenario: the documented signature is `GetKeywordStats(q, country, language)`, so startDate and endDate are ignored by the server and the result is never filtered by date. The user asks for 30 days and gets Bing's full history, labelled as if it were the window.
- Fix: drop the unused params and filter `out` by `per.startT..per.endT` locally, or document the fixed window.
- Status: VERIFIED in the docs (learn.microsoft.com, IWebmasterApi.GetKeywordStats(String, String, String)).

## Low

- L1. internal/signals/speed.go:283-284. Any PSI 429, including a keyless one or a per-minute burst limit, calls `q.Exhaust()` on the shared daily bucket. Later keyed calls are then refused locally until Pacific midnight. Fix: exhaust only for keyed daily-quota 429s (check the error reason); otherwise back off briefly. VERIFIED by reading the code.
- L2. internal/site/crawl.go:109 and 315-333. robots.txt is read only for the start host. When https://example.com/ redirects to https://www.example.com/, the www host's robots.txt and its Crawl-delay are never read, and all pages are crawled under the apex rules. Fix: reload robots when the start URL's final host differs. VERIFIED by reading the code.
- L3. internal/rank/runner.go:125-132. freePort closes the listener before SearXNG binds, so another process can take the port. healthy() then accepts whatever answers /healthz, or startup fails with "did not answer". Fix: retry on a bind failure in the log, or check that the child pid owns the port. SUSPECTED.
- L4. internal/rank/runner.go:230. `os.Environ()` passes SEOX_GOOGLE_API_KEY, SEOX_BING_KEY and SEOX_OPENPAGERANK_KEY to the python child, where they show up in `ps eww` and any crash dump. Fix: filter `SEOX_*` out of the child environment. VERIFIED by reading the code.
- L5. internal/config (Path/MkdirAll 0700). If $SEOX_HOME already exists with mode 0755, it is never tightened. The files are 0600, so the impact is only directory listing. Fix: chmod 0700 after MkdirAll. VERIFIED by reading the code.
- L6. internal/signals/speed.go (CrUX 400 handling). The 400 is mapped to Forbidden with "check the key", but CrUX returns 400 for a malformed URL or origin. The advice is misleading. SUSPECTED.
- L7. internal/signals/authority.go:153-156. Domains missing from the OPR `results` (for example ones in `invalid`) are cached for 7 days as empty "not found" entries. Fix: cache only the domains present in the results. (The field names domain, found, open_page_rank, rank and referring_domains match the docs.) VERIFIED by reading the code and the docs.
- L8. internal/signals/authority.go:256-259. The Wayback CDX query `url=<domain>` is an exact match on the homepage, but the result is shown as the domain's first-seen date and size. The base URL is also plain http. Fix: use `url=<domain>&matchType=domain` for the size, or label the result "homepage". SUSPECTED.
- L9. internal/signals/ideas.go. Google suggest with `client=firefox` can answer in a legacy charset for some `hl` values, and the body is used as UTF-8 without checking Content-Type, so ideas can come out garbled. Partial engine failures are also cached for 7 days. SUSPECTED.
- L10. internal/mine/auth.go. The login prints the auth URL to stderr even when stderr is not a terminal, which breaks the "progress only on a TTY" rule. Low impact because login already needs a TTY or --yes. VERIFIED by reading the code.

## Checked and fine
- killStale's `ps lstart` and `searx.webapp` match protects against a reused pid. The process group is SIGTERMed and then SIGKILLed on ctx cancel and on Stop (deferred, so it also runs during a panic). Nothing is written inside the Arshafa folder (PYTHONDONTWRITEBYTECODE, settings and logs live in SEOX_HOME).
- OAuth: 32-byte state compared in constant time, PKCE S256, listener on 127.0.0.1:0, refreshed tokens saved through savingSource, no token in the envelope, and no login without a TTY unless --yes or SEOX_LOGIN=1 is given.
- Bing GetUserSites (Url, IsVerified) and GetQueryStats/GetPageStats (Query, Date as /Date(ms-0700)/, Clicks, Impressions, AvgImpressionPosition) match the docs. The CrUX metric names, including experimental_time_to_first_byte, `percentiles.p75` with CLS as a string, and `percentilesTimeseries.p75s` match the docs. PSI loadingExperience CLS divided by 100 is correct.
- robots.txt grouping (merging groups for the same agent, falling back from seox to *), longest match, Allow winning a tie, the `$` anchor and wildcards behaved correctly in experiments. A 5xx disallows and a 4xx allows all.
- mapErr turns a ctx cancel or deadline into timeout. Quota is spent before the PSI call. The "not Google" and "no search volumes" labels are present.

---

## Round 2

Scope: the fixes for H1-H3, M1-M7 and L1-L10, plus the new code (internal/signals/lighthouse.go, the PSI to Lighthouse fallback in speed.go, rank.Missing in report/gather.go, ops.DoctorCheck).
Checks run: `go vet ./...` is clean and `go test -race -count=1 ./...` passes (SEOX_HOME in a temp folder, SEOX_NO_KEYCHAIN=1). Experiments ran as throwaway `_test.go` files in a scratchpad copy of the repo, using fake Lighthouse scripts through SEOX_LIGHTHOUSE. No real SearXNG, Lighthouse or Chrome was started.

Counts: reopened 0. New: medium 1, low 6, info 3.

### Status of round 1 findings
- H1 FIXED, VERIFIED. bingCall pointed at 127.0.0.1:1 now gives `request failed: Get "http://127.0.0.1:1/GetUserSites?apikey=REDACTED": ... connection refused`. The same holds for the JSON form and for the wrapped Err. web.go:88/98 (bad URL), :209 (timeout) and :211 (network) all go through `redacted`. The regex covers apikey, key and access_token, which are all the query-string secrets seox sends (Bing, PSI, CrUX).
- H2 FIXED. bing.go:254-259 parses `Links`, and the fixture now matches the documented `LinkCounts` sample.
- H3 FIXED, VERIFIED by tests and reading. runner.go:248 gives each run its own folder `run-<pid>`. reapDir (:168-198) returns early while the owner seox's lstart still matches. It kills only a `searx.webapp` whose lstart matches, and it leaves a folder under a minute old that has no pid file. TestRunnerStaleRuns covers a dead owner, a live owner, a reused pid and a mismatched start time. Port retry: bindFailed (:372) and ownsPort (:380) feed one retry in the same folder. Gap: only the "child exits with errno 48" path is tested. The "healthy but lsof names another pid" path (:353-358) has no test.
- M1 FIXED, VERIFIED. `Disallow: /مدونة/` blocks both the upper and lowercase %XX forms. `/a%3cb` blocks `/a%3Cb`, and `/~user` blocks `/%7Euser/x`.
- M2 FIXED, VERIFIED. With a BOM, group="*" and /private is blocked.
- M3 FIXED (speed.go:420-436). See N2 for a unit side effect.
- M4 FIXED, by reading. runCrux returns Timeout (:540, :568), Speed propagates it (:246-247), and cache.Put is skipped on partial or ctx.Err (:255).
- M5 FIXED. api.go:286-303 filters, and TestPageAuditSkipsRobotsBlockedLinks passes.
- M6 FIXED. crawl.go:95-99 builds the key from scheme + host + pathQuery + max + depth.
- M7 FIXED. The method no longer takes dates; the Note plus From/To describe the real window.
- L1 FIXED (speed.go:343). Only a keyed 429 whose body contains "PerDay" exhausts the bucket. That Google's body holds "PerDay" (ErrorInfo quota_limit) is SUSPECTED and was not fetched live.
- L2 FIXED (crawl.go:142-146). See N5 and N6.
- L3 FIXED (see H3).
- L4 FIXED for SearXNG (runner.go:227-235, and the test checks seoxenv=0). The same leak returns in the new Lighthouse code: see N3.
- L5 FIXED (config.go:86 chmod 0700).
- L6 FIXED. A CrUX 400 now maps to InvalidInput with "URL or origin is malformed".
- L7 FIXED. authority.go:157-161 shows null with a warning and does not cache.
- L8 FIXED, by relabelling. The fields and the Note say "homepage only".
- L9 FIXED. ideas.go:281 adds oe=utf-8, :304 has a Latin-1 fallback, and :110-115 skips the cache when partial.
- L10 FIXED. auth.go:359-368 prints only on a TTY, otherwise auth_url goes into the error detail.

### New findings

N1. MEDIUM. With a google key, missing Node or Chrome throws away good PSI and CrUX data. VERIFIED.
- Where: internal/signals/speed.go:221 (`ae.Code == apperr.ToolMissing || key == ""` → return).
- Scenario: a key is set and Node or Chrome is not installed (doctor even calls Lighthouse optional when there is a key). The mobile PSI call succeeds, but desktop PSI gets a transient 500 or 429. The fallback hits ToolMissing and the whole `speed` call fails with "local Lighthouse needs Google Chrome". The mobile result and CrUX are lost. Before the fallback existed, this case was a warning. Experiment: PSI fake returns 500 for desktop only, SEOX_LIGHTHOUSE=/bin/true, CHROME_PATH missing → res=nil, err=tool_missing.
- Fix: return ToolMissing only when key == "". With a key, add a warning ("desktop: PSI failed and local Lighthouse is unavailable: ...") and continue. The existing "no speed data" check already covers the case where both strategies fail.

N2. LOW. CLS savings are reported as milliseconds. VERIFIED.
- Where: speed.go:422-431. When metricSavings has no LCP, the largest value of any metric is taken.
- Scenario: cls-culprits-insight or layout-shifts with score 0 and `metricSavings {"CLS":0.31}` becomes `{id: cls-culprits-insight, savings_ms: 0.31}`. That reads as a 0.31 ms saving, and the item ranks below trivial items. TBT savings are also mixed with LCP ms in one ranking.
- Fix: use only the LCP and FCP (and possibly TBT) keys for savings_ms, and put CLS in its own field (savings_cls) or drop it from the ranking.

N3. LOW. API keys reach the Lighthouse, npx and Chrome processes. VERIFIED.
- Where: lighthouse.go:115 (`cmd.Env = append(os.Environ(), ...)`).
- Scenario: SEOX_BING_KEY, SEOX_OPENPAGERANK_KEY and, in the keyed fallback, SEOX_GOOGLE_API_KEY are passed to npx, which runs code downloaded from npm (Lighthouse and its unpinned dependencies), and on to Chrome. They are visible in `ps eww`. The experiment's child saw `SEOX_BING_KEY=BINGSECRET`. This is the same issue as L4 in a new place.
- Fix: reuse a filtered environment like rank.childEnv (drop SEOX_*), shared through one helper.

N4. LOW. npx resolves from the user's current folder. SUSPECTED.
- Where: lighthouse.go:114 (cmd.Dir is not set) together with :35-36.
- Scenario: `seox speed` run inside an untrusted checkout. npx reads that folder's .npmrc (registry=, scripts) and node_modules/.bin. A planted `.npmrc` registry, or a local `lighthouse@13.6.0` in node_modules, then runs instead of the npm package. The pin fixes the version, not the source. 13.6.0 itself exists on npm and is `latest`.
- Fix: set `cmd.Dir` to the temp profile's parent (or a fresh temp dir) and pass `--registry=https://registry.npmjs.org/`, or set `npm_config_userconfig=/dev/null`.

N5. LOW. The Crawl-delay warning is added twice, or three times after an apex to www redirect. VERIFIED by simulating the call order.
- Where: crawl.go:340 (loadRobots → applyDelay), then crawl.go:139 (applyDelay again, unconditionally), then :143 (loadRobots of the new host).
- Scenario: robots.txt has `Crawl-delay: 60`, and the warnings list has the same "waits at most 30s" line twice.
- Fix: drop the call at :139, since loadRobots already applies the delay, or de-duplicate the warnings.

N6. LOW. The robots summary is stale after a redirect to a host with no robots.txt. VERIFIED by reading.
- Where: crawl.go:330-338. c.rules is reset, but c.res.Robots is only overwritten on a 2xx.
- Scenario: the apex robots.txt has `Disallow: /`, but the apex redirects to www, whose robots.txt returns 404. The crawl proceeds (correctly, under allow-all), yet the result still reports Found=true and BlocksAll=true from the apex, and the audit can flag "robots blocks the whole site".
- Fix: reset c.res.Robots at the top of loadRobots.

N7. LOW. A page without CrUX data is never cached. VERIFIED by reading.
- Where: speed.go:251 (`len(warns) > 0 && c == nil` → partial).
- Scenario: most small sites have no CrUX data. Every keyed `speed` call then spends 2 PSI quota units and about 30 s again, and the 12 h cache never applies.
- Fix: treat "CrUX has no data" as a complete answer and mark partial only on a real "skipped" error (which hasSkip already does).

I1. INFO. The Lighthouse stderr capture keeps the first 4 KB (lighthouse.go:119), but the message takes the tail of that (:133-136). On a first npx install, npm warnings can fill the 4 KB, so the real Lighthouse error is cut off. SUSPECTED. Fix: keep a ring of the last 4 KB.

I2. INFO. pkill uses the profile path as an unanchored regex (lighthouse.go:156). MkdirTemp suffixes are decimal numbers of varying length, so run A's `seox-chrome-12345678` is a prefix of a concurrent run B's `seox-chrome-123456789` and would kill B's Chrome. The chance is about 1e-9 per pair. The user's own Chrome and Playwright's Chrome can never match. Hardening: match `--user-data-dir=<profile>( |$)`.

I3. INFO. DoctorChecks are merged after the built-in checks (cli/setup.go:156), so an area check named "searxng" or "key_google" would silently replace the built-in one. ops.go:269 only guards against duplicates among area checks. Fix: also panic on built-in names, or prefix area checks.

### Checked and fine (new code)
- argv: Speed's normalizeURL plus the http(s) prefix check (lighthouse.go:93) keep the URL as one positional argv element. The fake recorded `https://x.com/a b --output-path=/tmp/zz` as one element, and no shell is involved.
- Process group: Setpgid with Cancel sending SIGTERM to -pid, WaitDelay 5 s, then SIGKILL to the group and `pkill -f <profile>`. Experiment: the fake started a TERM-ignoring child in the group and a detached (setpgrp) process with the profile in its argv. After a 1.5 s deadline, both were gone, the profile folder was removed, the code was Timeout, and the call took 6.5 s (deadline + WaitDelay).
- The stdout cap of 20 MB keeps draining, sets a flag and returns source_error, never a truncated parse. The stdout and stderr writers are separate, so there is no data race.
- SEOX_LIGHTHOUSE runs any program, with the same trust as PATH. doctor shows what will run, and `LighthouseStatus` starts nothing.
- PSI to Lighthouse fallback: it runs only for strategies without a PSI result, adds a warning with the redacted PSI error and is never cached (partial). A ctx timeout returns before the fallback (:206).
- rank.Missing uses the same cacheKey, withDefaults and siteHost as Positions. gather passes Domain and Positions gets Site, but siteHost normalises both. If an entry expires between Missing and Positions, Positions opens its own instance lazily, so nothing breaks.
- ops.DoctorCheck: mutex, duplicate panic, and the checks run outside the lock.
