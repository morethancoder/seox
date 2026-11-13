# Bugs from the evidence run (2026-11-12)

Run: `seox report morethancoder.com --max 60 --competitor toptal.com --competitor marclou.com --kw ...`
Output: `.out/morethancoder.com-seo-2026-11-12-2.md`, `.out/final.json`.

## E1 (high, verified): local Lighthouse runs in parallel and skews scores. Area: signals
- `report` runs `speed` for 3 sites at once (`internal/report/gather.go:203`), and `Speed` runs mobile and
  desktop at once (`internal/signals/speed.go:189`), so up to 6 Chromes run Lighthouse together.
- morethancoder.com inside the report: mobile 38 (LCP 6.74 s), desktop 58 (LCP 4.48 s).
  Alone (`seox speed`): mobile 88 (LCP 2.49 s), desktop 90 (LCP 0.97 s).
- Fix: run local Lighthouse one at a time, across processes too (an flock in seox home, e.g. via
  `internal/quota`'s lock helper or a small lock file), while PSI calls stay parallel. Respect ctx while
  waiting for the lock. Add a test with a fake lighthouse binary that records overlapping runs.

## E2 (medium, verified): the report labels local Lighthouse as Google's servers. Area: report
- `internal/report/gather.go:472` always says "PageSpeed Insights lab run (Lighthouse)", and
  `internal/report/note.go:292` says "lab numbers from Google's servers".
- The run used `lighthouse-local` (no google key). The spec (criterion 5) wants the label
  "local Lighthouse, no real-user data".
- Fix: build the label from each `PSIResult.Source` (psi, lighthouse-local, or mixed), and word note.go
  to match. Test both sources.

## E3 (medium, verified): SVG `<title>` counted as extra page titles. Area: site
- toptal.com: "The page has more than one <title>" on 60 of 60 pages; `seox page https://www.toptal.com/`
  gives `title_count 3`, but the raw HTML has one `<title>` in `<head>`; the rest are `<svg><title>`.
- `internal/site/parse.go:192` counts every `title` element.
- Fix: count only HTML-namespace titles (`n.Namespace == ""`), and take the page title from the first
  of those. Test with a page that has `<svg><title>` icons.

## E4 (medium, verified): timeouts reported as broken internal links. Area: site
- marclou.com: "Pages link to internal URLs that return an error" (52). Every example is a timeout
  ("http2: timeout awaiting response headers", "context deadline exceeded"); `curl` gets 200 for them.
- A link that could not be checked is not a broken link.
- Fix: only 4xx/5xx (and DNS or connection refused) count as broken. Timeouts go to a separate low
  issue "links that could not be checked (timeout)" with their URLs, plus a warning. Consider one slower
  retry for timeouts. Same rule for `broken-link-external` and `page-unreachable` wording. Test with an
  httptest server that sleeps past the timeout.
