# seox: rules for anyone working here

seox is a free SEO CLI for agents, modelled on `~/Projects/CLI/ytx`. Read `docs/spec.md` (approved
criteria and shared rules) and `docs/sources.md` (the data sources) first, then
`internal/ops/list.go`: the ops table is the single source of truth for every command.

## How the code is laid out

- `internal/ops`: the table of commands (`list.go`), `Input`, `Result`, `Meta`, `Handle()`.
  A variadic last positional (`Variadic: true`) arrives as `in.Strings(name)`.
- `internal/cli`: cobra commands built from the table, the JSON envelope, `schema`, `doctor`,
  `key`, `config`, `cache`. Owned by the lead.
- `internal/apperr`: error codes and exit statuses. Return `apperr.New(code, ...)`; never invent a
  code without adding it there. Context cancel and deadline map to `timeout`.
- `internal/config`: `SEOX_HOME`, config keys, Keychain secrets. `config.Key("google"|"bing"|"openpagerank")`
  returns the key (env first, then Keychain).
- `internal/cache`: disk cache with a TTL (`Key`, `Get`, `Put`, `Clear`). `--no-cache` is `in.NoCache`.
- `internal/quota`: per-day counter per named bucket, flock across processes (`quota.New(bucket, limit)`,
  `Spend`, `Left`, `Exhaust`).
- `internal/web`: the one shared HTTP client (`Get`, `GetNoRedirect`, `Do`, `SetCrawlDelay`,
  `BaseURL`/`OverrideBase` and `RewriteHost` for tests, `SetMinGap(0)` to skip the pacing in tests).
- Area packages, each attaches handlers with `ops.Handle("<op name>", fn)` in its own `init()`;
  `cmd/seox/main.go` blank-imports them. An op with no handler answers `not_implemented`.
  An area adds its own `seox doctor` line with `ops.DoctorCheck` in `init()`; `internal/cli` never
  imports an area package (its test fakes register the same handlers).
  - `internal/site`: crawler, audit, page, keywords
  - `internal/signals`: speed, authority, ideas
  - `internal/rank`: SearXNG runner, rank
  - `internal/mine`: login, logout, GSC, Bing, mine, setup verify
  - `internal/report`: compare, report

## Rules

- Do not edit another area's package. If you need something shared (web, cache, ops, cli), say so in
  your report.
- Every command prints one envelope (`internal/cli/output.go`). Handlers return `*ops.Result` or an
  `apperr` error and never print to stdout. Progress text goes to stderr only when stderr is a terminal.
- seox has no remote writes. Kind is `read` or `local` (files, settings, Keychain on this machine).
- Nothing may wait for input without a terminal. Secrets go in the Keychain via `internal/config`
  (`SEOX_NO_KEYCHAIN=1` turns it off in tests) or come from env (`SEOX_GOOGLE_API_KEY`,
  `SEOX_BING_KEY`, `SEOX_OPENPAGERANK_KEY`). Never write them to a file or log them.
- HTTP only through `internal/web`: timeouts, 10-redirect limit, body size cap, one retry on 429/5xx.
  Crawls honour robots.txt (`seox`, then `*`), at most 2 concurrent requests and 250 ms between
  requests per host, and `Crawl-delay` when larger. Never put the user's name, email or site list
  in requests to third parties.
- Respect `ctx` everywhere. A cancel or timeout is the `timeout` code.
- Tests: offline by default. Use recorded responses in `testdata/<area>/` and an `httptest.Server`
  or a swappable base URL. Live tests carry the build tag `live`, have names starting `TestLive`, and
  run with `make live`.
- Keep `gofmt` and `go vet` clean (`make lint`). Run `make test` before reporting.
- Write code comments as plain sentences explaining why.
- Standard library first. Allowed: cobra, go-keyring, `golang.org/x/oauth2`, `golang.org/x/net/html`.
  Anything else needs a reason in the report.
- Don't commit. The lead merges and commits.
