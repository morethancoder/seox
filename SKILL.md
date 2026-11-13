---
name: seox
description: Free SEO for agents. Audit any site, check page speed, get keyword ideas, ranks and authority with no account; add real Search Console and Bing data for sites you verified. Use when asked to audit, compare or report on a site's SEO.
---

# seox

Every command prints one JSON envelope `{ok, data, error{code,message,detail}, warnings, meta}`
when stdout is not a terminal (or with `--json`). Exit codes: `seox schema` lists them by status.
Nothing prompts without a terminal. `seox schema` prints every command with its arguments.

Every command works with no account. Read `warnings` and `meta.source`: they say when data is
approximate (SearXNG is "not Google", local Lighthouse has no real-user data, ideas have no volumes).

## Any site, no account

- `seox audit <site> [--max N] [--depth N]` crawl, score 0-100, issues ranked by impact
- `seox page <url>` one page in depth, with the keywords it targets
- `seox keywords <site> [--max N] [--limit N]` terms the site targets
- `seox speed <url> [--strategy mobile|desktop|both]` with a google key: PageSpeed Insights plus CrUX; without one: local Lighthouse (needs Node and Chrome; slow, about a minute per strategy)
- `seox ideas <keyword> [--lang en] [--country us] [--limit N]` autocomplete ideas (no volumes)
- `seox authority <domain>...` Open PageRank and Wayback first-seen
- `seox rank <site> <keyword>... [--lang] [--depth 3] [--gap 3]` positions per engine through SearXNG (started and stopped by seox); queries are paced 3 s + 0-2 s apart and slow down on rate limits
- `seox compare <site> <competitor>... [--kw K]... [--max N] [--depth N]` side by side
- `seox report <site> [--competitor C]... [--kw K]... [--max N] [--out DIR]` one markdown note (never overwrites; a second run gets `-2`). With competitors and no google key it takes a few minutes, since Lighthouse runs one site at a time

## Your own verified site

- `seox login --client client_secret.json` / `seox logout` Google Search Console sign-in
- `seox mine queries|pages|drops|almost <site> [--days 28] [--limit 20]`
- `seox setup verify <site>` how to verify the site in Search Console and Bing

## Setup

- `seox doctor` state of every source (keys, sign-in, SearXNG, Lighthouse and Chrome, report_dir), with a fix for each
- `seox key set|remove <google|bing|openpagerank>` secret from piped stdin, stored in the Keychain
- `seox config [set <key> <value>]` keys: searxng_url, report_dir, crawl_max, arshafa_searxng_dir
- `seox cache clear`

Global flags: `--json --human --fields a,b.c --no-cache --timeout 2m`.
