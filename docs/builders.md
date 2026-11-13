# Notes for area builders (stage 2)

Read `docs/spec.md` (the criteria and shared rules), `docs/sources.md` (endpoints and limits),
`CLAUDE.md`, `internal/ops/list.go` (your ops and their params) and `internal/web`, `internal/cache`,
`internal/quota` and `internal/config`.

## Helpers you must use

- **web:**
  - `web.Get(ctx, url, max)`, `web.GetNoRedirect`, `web.Do(ctx, req, max)` → `*Response{Status, Header, Body, URL, Truncated}`. A non-2xx status is not an error.
  - `web.SetCrawlDelay(host, d)`.
  - Tests: `web.BaseURL(name, default)` with `web.OverrideBase`, `web.RewriteHost(from, hostport)`, `web.SetMinGap(0)`.
  - Never build your own `http.Client`.
- **cache:** `cache.Key(parts...)`, `cache.Get(key, ttl, &out)`, `cache.Put(key, source, v)`. Skip it when `in.NoCache`.
- **quota:** `quota.New(bucket, limit)` with `Spend` (before the call), `Left`, `Used` and `Exhaust`.
- **config:**
  - `config.Key("google"|"bing"|"openpagerank")` → (key, where).
  - `config.SecretOAuthToken` and `config.SecretOAuthClient`.
  - `cfg.Crawl()` and `cfg.ArshafaDir()`.
- **apperr:** codes and exit statuses. A ctx cancel or deadline maps to `timeout`.

## Boundaries

- Register your handlers with `ops.Handle` in your package's `init()`. Edit **only your own package**
  under `internal/<area>/` and your own `testdata/<area>/`.
- Do not edit `internal/ops`, `internal/cli`, `internal/web`, `internal/cache`, `internal/quota`,
  `internal/config`, `apperr`, README.md, SKILL.md or the Makefile. If you need a change there
  (a missing param, a helper), list it in your report with the exact change; the lead applies it.
- Export a typed Go API for stage 3. `internal/report` builds `compare` and `report` by calling your
  functions directly, not the CLI. Each command gets a function like
  `func Audit(ctx context.Context, site string, opt AuditOptions) (*AuditResult, error)`, with
  JSON-tagged result structs. The handler is a thin wrapper around it. Put the signatures in your
  report.
- Results carry `Meta.Source` (e.g. "crawl", "pagespeed", "crux", "searxng", "gsc", "bing") and
  honest labels in the data.
- Tests run offline: httptest plus recorded fixtures in `testdata/<area>/`. Add at least one live test
  (`//go:build live`, `TestLive*`) against a real public site or endpoint.
- Run `make test` (and `RACE=1 make test`) and `make lint` before reporting. You may run `make live`
  for your own live tests. Anything you start must be stopped before you report.
- No commit. Report in under 300 words: what was built, the exported API, test results, any shared
  changes needed, and open questions.
