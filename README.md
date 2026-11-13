# seox

A free SEO CLI for agents. It works on any site with no account, and adds real search data for
sites you verified in Google Search Console and Bing Webmaster Tools. Built like ytx.

Every command works with no account and no key. Keys and sign-in only add data; `seox doctor`
says what each one adds.

```
go install github.com/morethancoder/seox/cmd/seox@latest   # or, from a clone:
make install        # puts seox in ~/go/bin
seox schema         # every command, its arguments and exit codes, as JSON
seox doctor         # state of every source, with a fix for each
```

## Commands

| Command | What it does |
|---|---|
| `seox audit <site>` | crawl a site, score and rank issues |
| `seox page <url>` | one page in depth, with target keywords |
| `seox keywords <site>` | terms the site targets |
| `seox speed <url>` | Lighthouse scores and Core Web Vitals (local Lighthouse, or PageSpeed Insights and CrUX with a Google key) |
| `seox ideas <keyword>` | autocomplete ideas (no volumes) |
| `seox rank <site> <keyword>...` | positions through SearXNG (per engine, labelled "not Google" unless Google answered); queries are paced 3 s + 0-2 s jitter apart, doubled (max 30 s) if an engine rate-limits; `--gap <s>` changes it |
| `seox authority <domain>...` | Open PageRank and Wayback |
| `seox compare <site> <competitor>...` | side by side |
| `seox report <site>` | one markdown note in `report_dir` |
| `seox login --client client_secret.json`, `seox logout` | Google Search Console sign-in |
| `seox mine queries\|pages\|drops\|almost <site>` | your real rankings |
| `seox setup verify <site>` | how to verify a site |
| `seox doctor`, `seox config [set]`, `seox cache clear`, `seox key set\|remove <name>` | setup |

Design: `docs/spec.md`. Data sources: `docs/sources.md`. Rules for contributors: `CLAUDE.md`.
Agent usage: `SKILL.md`.

## Home and secrets

State lives in `$SEOX_HOME` (default `~/.seox`). Keys go in the macOS Keychain
(`printf %s "$KEY" | seox key set google|bing|openpagerank`) or the environment
(`SEOX_GOOGLE_API_KEY`, `SEOX_BING_KEY`, `SEOX_OPENPAGERANK_KEY`). `SEOX_NO_KEYCHAIN=1` turns the Keychain off.

## What each key or tool adds

| Item | Without it | With it |
|---|---|---|
| Google API key (free, `seox key set google`) | `speed` runs Lighthouse locally in Chrome: lab data only, one run at a time | PageSpeed Insights from Google's servers plus CrUX real-user data. Google allows no keyless PSI calls (its keyless quota is 0, checked 2026-11-12) |
| Node.js and Google Chrome | without a Google key, `speed` has no data (`tool_missing`) | local Lighthouse through `npx lighthouse@13.6.0` (override with `SEOX_LIGHTHOUSE`, `CHROME_PATH`) |
| SearXNG (Arshafa's install, or `searxng_url`) | `rank` and report positions are skipped | positions per engine; seox starts and stops its own copy |
| Open PageRank key (free) | authority score is "no data"; Wayback still works | a 0-10 authority estimate |
| Google sign-in or Bing key | no real rankings | `mine` and the report's real rankings for sites you verified |

## License

MIT, see [LICENSE](LICENSE).
