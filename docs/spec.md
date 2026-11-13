# seox spec

seox is a free SEO CLI for agents. It works on **any site with no account**, and adds **real ranking data
for sites the user has verified** (Google Search Console, Bing Webmaster Tools). It is built like
`~/Projects/CLI/ytx`. Read ytx's `CLAUDE.md`, `internal/ops`, `internal/cli`, `internal/apperr`,
`internal/config`, `internal/cache` and `internal/quota` before writing anything. Copy those
patterns rather than inventing new ones.

The research on free data sources (endpoints, auth, limits, docs links) is in `docs/sources.md`.

## Approved acceptance criteria

1. Go CLI `seox` in `~/Projects/CLI/seox` (module `github.com/morethancoder/seox`).
   - Built like ytx: an ops table, `seox schema`, one JSON envelope, ytx's exit codes, a Makefile
     (makefile-standards), README.md and SKILL.md.
   - It never waits for input without a terminal.
2. Every command works on any site or URL with no account. Verified sites add real data.
3. `seox audit <site>` crawls the site.
   - Crawl: default 200 pages (`--max`); follows robots.txt; keeps a polite pace; same host only
     (www and apex count as one).
   - Checks: titles, meta descriptions, headings, canonical tags, redirects, broken links (internal,
     plus external with HEAD and a cap), status codes, noindex, sitemap.xml, robots.txt, HTTPS,
     mixed content, Open Graph, structured data (JSON-LD types found and parse errors), image alt
     text, duplicate titles and descriptions, thin pages (word count), and missing or invalid
     hreflang, lang and viewport.
   - Output: a score (0-100) and issues ranked by impact (high/medium/low), each with its affected
     URLs.
4. `seox page <url>` runs the same checks on one page in more depth. It also shows the keywords that
   page targets (title, H1/H2 and body terms, weighted).
5. `seox speed <url>`
   - PageSpeed Insights v5 for mobile and desktop: score, LCP, CLS, INP/TBT, FCP and the top
     opportunities.
   - CrUX real-user Core Web Vitals (page, then origin) and the CrUX History trend, when Google has
     data.
   - With a free Google API key (`seox key set google`): PSI plus CrUX. Google allows 0 keyless PSI calls
     (checked 2026-11-12: `quota_limit_value: 0`), so without a key seox runs Lighthouse locally in Chrome
     (lab data only, labelled "local Lighthouse, no real-user data"), and the output says how to add a key.
     If PSI fails with a key, it falls back to local Lighthouse as well. (Changed with the user, 2026-11-12.)
6. `seox keywords <site>` lists the terms the site targets, taken from the crawl: n-grams weighted by
   where they appear, and which pages target each.
7. `seox ideas <keyword>` gives autocomplete ideas (Google and Bing suggest endpoints, keyless).
   - Includes questions (who/what/how/why...), A-Z expansions and related terms, grouped and
     de-duplicated.
   - Says plainly in `data.note` that there are no search volumes.
   - `--lang` and `--country` options.
8. `seox rank <site> <keyword>...` gets positions through SearXNG.
   - Each engine is shown separately, from SearXNG's `engines` and `positions` fields. Each result
     carries `google: true/false`, and is labelled "not Google" unless Google answered.
   - `unresponsive_engines` become warnings.
   - Where SearXNG comes from:
     - The config `searxng_url`, when set.
     - Otherwise seox starts its own copy from Arshafa's installed SearXNG at
       `~/Library/Application Support/app.arshafa.desktop/searxng` (`venv/bin/python -m
       searx.webapp`, cwd `src`, `PYTHONPATH=src`). It uses its own settings.yml, pid and log in seox
       home, on a free 127.0.0.1 port, in its own process group. It waits for `/healthz`, and is
       stopped when the command ends (and on Ctrl-C).
     - seox never writes inside Arshafa's folder.
     - If neither exists: a `tool_missing` error that says how to fix it (install web search in
       Arshafa, Settings → AI, or set `searxng_url`).
9. `seox authority <domain>...`
   - The Open PageRank score: free key in the Keychain (`seox key set openpagerank`); without a key,
     a warning.
   - Wayback CDX: the first-seen date and snapshot count.
10. `seox compare <site> <competitor>...` puts these side by side: audit score and top issues, speed,
    authority, page count, shared and unique target keywords, and positions for `--kw` keywords.
11. Your own site, once verified:
    - `seox login --client client_secret.json`: Google OAuth Desktop client, loopback flow like
      ytx's, scope `webmasters.readonly`, token in the Keychain.
    - `seox key set bing`: stores the Bing Webmaster key.
    - `seox mine queries|pages|drops|almost <site>`:
      - top queries and pages (clicks, impressions, CTR, position);
      - pages and queries losing ground against the previous period of the same length;
      - queries at average positions 8-20.
      - `--days` (default 28), `--limit`.
    - Bing adds inbound links (link counts) and query and keyword stats for the site.
    - The site is matched to a property: `sc-domain:` or a URL prefix, from `sites.list`.
12. `seox doctor` shows the state of every source with a fix for each: keys, sign-in, SearXNG
    reachable or Arshafa install found, report_dir. `seox setup verify <site>` gives the steps to
    verify the site in Search Console and Bing (Bing can import from GSC), and says whether the
    signed-in account already has it.
13. `seox report <site>` writes one markdown note to `report_dir` (config; default the current
    folder) with everything above: audit, speed, authority, keywords, ideas for the top keywords,
    positions for `--kw`, and real rankings when verified.
    - It accepts `--competitor` (repeatable).
    - File name `<domain>-seo-<date>.md`. It never overwrites; the second run gets `-2`.
    - The note starts with a short summary and the top 5 actions.
14. Caching and secrets:
    - Crawls and API answers are cached in seox home (`--no-cache`).
    - Quotas and rate limits are tracked across processes (flock), like ytx.
    - Secrets are kept only in the Keychain or env (`SEOX_NO_KEYCHAIN=1` in tests).
15. Tests run offline (httptest, recorded fixtures in `testdata/<area>/`). Live tests use the build
    tag `live`, names `TestLive*`, and run with `make live`.
16. Evidence: a real run on morethancoder.com and two competitors.

## Shared rules (for every builder)

- Never prompt without a terminal. Handlers return `*ops.Result` or an `apperr` error and never
  print to stdout.
- HTTP: one shared client (`internal/web`), with timeouts, a redirect limit of 10 and a size cap on
  bodies. User-Agent `seox/<version> (+https://github.com/morethancoder/seox)`. Never put the
  user's name, email or site list in requests to third parties.
- Crawling is polite: robots.txt for `seox` then `*`, at most 2 concurrent requests per host, at
  least 250 ms between requests to a host, and `Crawl-delay` when it's larger.
- Respect `ctx` everywhere. A cancel or timeout maps to the timeout error code, not network or
  internal.
- Comments are plain sentences explaining why. gofmt and vet clean. Standard library first;
  `golang.org/x/oauth2` and `golang.org/x/net/html` are allowed, plus ytx's existing deps (cobra,
  keyring/flock if ytx uses them). Anything else needs a reason in the report.
- Do not edit another area's package. If you need something shared, say so in your report.
- No commit, no push. Don't start long-running processes you leave behind.
