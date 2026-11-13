# Free SEO CLI data sources (checked 2026-11-12)

Method: official pages fetched with WebFetch/WebSearch (docli not used; one-off reads). "UNVERIFIED" = only third-party or no source found.

## 1. Google Search Console API  -> USE
Docs: https://developers.google.com/webmaster-tools/v1/searchanalytics/query , https://developers.google.com/webmaster-tools/limits , https://developers.google.com/webmaster-tools/v1/api_reference_index
- Group by: country, device, page, query, searchAppearance, date, hour. Filter by country/device/page/query/searchAppearance (equals, contains, notContains, notEquals, includingRegex, excludingRegex). type: web, discover, googleNews, news, image, video. (verified)
- rowLimit 1-25,000 (default 1,000), startRow paging. API returns top rows only, not all rows (verified). dataState: final (default), all (fresh, may be incomplete), hourly_all. Dates in Pacific Time.
- 16 months of history, ~2-3 day lag: only secondary sources (searchengineland.com/google-search-console-analytics-api-now-has-16-months-of-data-300430) -> not re-confirmed on official page.
- Quotas (verified): Search Analytics 1,200 QPM per site and per user; 30M QPD / 40,000 QPM per project; plus load limits (10-min and 1-day windows; page+query grouping and long ranges are costly). URL Inspection: 2,000 QPD and 600 QPM per site; 10M QPD / 15,000 QPM per project. Other: 20 QPS, 200 QPM per user.
- Methods in the API reference (verified): sites (add/delete/get/list), sitemaps (list/get/submit/delete), searchanalytics.query, urlInspection.index.inspect. NO links/backlinks endpoint (confirmed: absent from the reference index). Links report is UI-only.
- searchAppearance: FAQ appearance deprecated in the API Aug 2026 (per query doc).
- Auth: OAuth 2.0 to owner's account (scope webmasters.readonly) or service account added as a user. REST/JSON, easy in Go.
- Terms: official API for owner's own data; automated use is the point.

## 2. Bing Webmaster Tools API  -> USE
Docs: https://learn.microsoft.com/en-us/bingwebmaster/getting-access , https://learn.microsoft.com/en-us/dotnet/api/microsoft.bing.webmaster.api.interfaces.iwebmasterapi?view=bing-webmaster-dotnet , https://www.seroundtable.com/bing-webmaster-tools-soap-pox-apis-retire-41805.html
- Auth: API key (one per user, works for all verified sites) or OAuth 2.0 (recommended). Site must be verified (can import from GSC). (verified)
- Methods listed on official interface (verified): GetKeyword (impressions for a keyword over a period), GetKeywordStats (historical stats), GetRelatedKeywords (impressions for a period), GetLinkCounts (own pages that have inbound links), GetUrlLinks (inbound links for a URL), GetQueryStats, GetPageStats, GetPageQueryStats, GetQueryPageStats, GetRankAndTrafficStats, GetCrawlIssues, GetCrawlStats, GetUrlInfo, SubmitUrl/SubmitUrlBatch, GetUrlSubmissionQuota.
- So Bing DOES expose keyword research data plus inbound links to the owner's own site (GetLinkCounts/GetUrlLinks). Link data is limited to what Bing reports for the verified site; depth/size of the list UNVERIFIED.
- IMPORTANT: SOAP and POX endpoints were retired 2026-08-31; JSON/HTTP is the supported API, same key, same quotas. Use https://ssl.bing.com/webmaster/api.svc/json/<Method>?apikey=... (endpoint pattern seen on third-party page, UNVERIFIED on official docs).
- Quotas: Microsoft says quotas unchanged; numeric values for query methods NOT found (UNVERIFIED). URL submission has a daily quota via GetUrlSubmissionQuota.
- Whether GetKeyword/GetRelatedKeywords return non-empty data in 2026 for any site: UNVERIFIED (not tested, no signup).

## 3. PageSpeed Insights v5 + CrUX + CrUX History  -> USE
Docs: https://developers.google.com/speed/docs/insights/v5/about , https://developers.google.com/speed/docs/insights/v5/get-started , https://developer.chrome.com/docs/crux/api , https://developer.chrome.com/docs/crux/history-api
- PSI: GET https://www.googleapis.com/pagespeedonline/v5/runPagespeed?url=...&key=... Returns Lighthouse lab data (Performance, Accessibility, Best Practices, SEO scores, FCP/LCP/Speed Index/CLS/TBT, audits) plus CrUX field data (28-day, LCP/INP/CLS/FCP/TTFB, CWV pass/fail, origin fallback). (verified)
- Key: **required in practice.** Checked 2026-11-12: a keyless call returns 429 with `quota_limit: defaultPerDayPerProject`, `quota_limit_value: 0`. Without a key, seox runs Lighthouse locally instead. Quota 25,000/day and ~240/min per project: SECONDARY only (unlighthouse.dev/learn-lighthouse/pagespeed-insights-api/rate-limits); per-100-s figures conflict. Check Cloud Console quota page.
- CrUX API: POST https://chromeuxreport.googleapis.com/v1/records:queryRecord, Cloud API key, 150 QPM per project, free, quota cannot be bought. Origin or URL level (URL only if enough traffic), form factor PHONE/TABLET/DESKTOP, 28-day window, ~2 days behind, daily update ~04:00 UTC. (verified)
- CrUX History API: POST .../v1/records:queryHistoryRecord, 1-40 weekly collection periods (default 25), each 28-day rolling, same 150 QPM shared. (verified)
- Auth: plain Google Cloud API key (free). REST/JSON.

## 4. Autocomplete  -> OPTIONAL
- Google: https://suggestqueries.google.com/complete/search?client=firefox&q=... returns JSON, no key. Undocumented; no official terms or limits found. Google ToS prohibit automated queries (https://seroundtable.com/google-warns-automated-queries-23135.html, 2016, covers search scraping generally). Throttling anecdotal (~100 queries cited in an old StackOverflow post). Verdict: works, fragile, gray area. Low volume, cache, back off on errors.
- Bing autosuggest without a key: NOT confirmed (no source found) -> UNVERIFIED. The old Bing Autosuggest API was under the retired Bing Search APIs (retired 2025-08-11).

## 5. Google Trends  -> SKIP official / OPTIONAL unofficial
- Official API alpha: announced 2025-07-24 (https://developers.google.com/search/blog/2025/07/trends-api). Page https://developers.google.com/search/apis/trends still says "accepting applications for alpha testers" and "can't open the API for everyone yet". Not open to everyone; SEOs not expected in first group (https://www.seroundtable.com/google-trends-api-seo-access-39957.html). Data: 5 years, consistently scaled (not 0-100 rescaled), day/week/month/year, regions. No API terms published on the page. Cannot be depended on.
- Unofficial: pytrends archived 2025-04-17 (secondary: dev.to / scrapfly), frequent HTTP 429; trendspyg and pytrends-modern exist (RSS trending, explore). All scrape trends.google.com from your IP; Google ToS risk, breakage risk. A Go CLI would have to reimplement the explore token + widgetdata flow (UNVERIFIED effort).

## 6. Rank checks beyond GSC impressions
| Option | Status | Note |
|---|---|---|
| Brave Search API | No free tier. $5/1,000 requests, $5 free credit/month (~1,000 queries), credit card required. https://brave.com/search/api/ (homepage also says "up to $20" free calls). Storage rights need custom plan. | Allowed, paid-ish. Results are Brave's index, not Google's rank. OPTIONAL |
| Google Programmable Search JSON API | Closed to new customers; shutdown 2027-01-01. 100/day free for existing only. https://developers.google.com/custom-search/v1/overview . New engines capped at 50 domains (secondary). | SKIP |
| Bing Web Search API | Retired 2025-08-11 (https://learn.microsoft.com/en-us/lifecycle/announcements/bing-search-api-retirement ; title seen, full text not read). Replacement "Grounding with Bing" gives no raw results. | SKIP |
| DuckDuckGo | No official full-results API; Instant Answer API gives summaries only. HTML scraping terms not found. | SKIP for rank |
| SearXNG self-hosted | Aggregates engines; JSON must be enabled, limiter returns 429 to non-browser clients unless off; Google engine often blocked/CAPTCHA from one IP (2026 test, apiserpent.com, secondary). Upstream engines' ToS still apply. | OPTIONAL, unreliable for Google position |
| Mojeek API | Paid pay-as-you-go in GBP; free trial credit, card required; own ToS. https://www.mojeek.com/services/search/web-search-api/ | Independent index, not Google rank. SKIP |
Conclusion: nothing free and permitted gives true Google position for keywords with no impressions. Best free proxy: GSC for ranking keywords; Bing WMT data; manual/own-browser checks.

## 7. Backlinks
- Common Crawl web graph: https://www.commoncrawl.org/blog/host--and-domain-level-web-graphs-august-september-and-october-2026 (verified). Released 2026-10-22, roughly monthly, each covers 3 crawls with 2-month overlap. Host graph 242.8M nodes / 2.4B edges; domain graph 135.0M nodes / 1.8B edges. Files: host edges paths 10.9 GiB, host transposed graph (inlinks) 4.9 GiB, host ranks 4.5 GiB; domain edges 7.1 GiB, domain vertices 942.6 MiB, domain ranks 2.4 GiB, domain transposed 3.7 GiB. HTTPS: https://data.commoncrawl.org/projects/hyperlinkgraph/cc-main-2026-aug-sep-oct/ . No per-host query API: you must download (domain ranks 2.4 GiB is the smallest useful piece; inlinks need the 3.8-7 GiB files or a WebGraph/Java toolchain). Offline pipeline; could be pre-processed into a small lookup shipped separately. Free, open data. Counts only domain/host edges, not page-level anchors.
- Open PageRank: https://keywordseverywhere.com/openpagerank/docs (verified). POST https://openpagerank.keywordseverywhere.com/v1/domains/bulk, Bearer key, 100 domains/request, free tier example 30,000 domains/month, 60 req/min; free key tied to one IP (enforced from 2026-11-01); old DomCop keys dead. Gives a rank score per domain only, not a link list. OPTIONAL. Signup needed (not done).
- Ahrefs free: see section 8.
- Not checked: Moz/Majestic free link data beyond 8; Majestic, Seobility etc. UNVERIFIED.

## 8. Others
- Ahrefs Webmaster Tools (now "Ahrefs Free"): changelog (https://ahrefs.com/blog/changelog/) says 2026-07-16 API enabled for free accounts; Domain Rating endpoint free since 2026-06-13, attribution required, POST up to 1,000 domains. No limits listed. Docs say other endpoints use units and v3 needs paid Lite+ ; free accounts get a limited set of free test queries (https://docs.ahrefs.com/api/reference/subscription-info/get-limits-and-usage). So: free API = DR endpoint + test queries only; no backlink/keyword data via API. Conflicting secondary sources; exact test-query endpoints UNVERIFIED. OPTIONAL (DR only).
- Moz API free: ~50 rows/month, 1 request per 10 s, no card (secondary: toolsurf.com/moz-api-free-limit-2026). Not useful. SKIP. UNVERIFIED on moz.com.
- Wayback CDX: http://web.archive.org/cdx/search/cdx?url=...&output=json ; no key, no stated rate limit, page history/first-seen, up to 150k results per query (verified from the repo README). OPTIONAL for page history. Be polite.
- IndexNow: key file on site, POST up to 10,000 URLs; engines share submissions; Google not named on the page. Bing/Yandex etc. only. OPTIONAL (action, not insight). https://www.indexnow.org/documentation
- Google Indexing API: only JobPosting or BroadcastEvent-in-VideoObject pages (verified: https://developers.google.com/search/apis/indexing-api/v3/quickstart). SKIP. Default quota 200/day: UNVERIFIED.
- Not checked: Common Crawl CDX index, Google Safe Browsing, Lighthouse CLI locally (free, no quota, an alternative to PSI), robots/sitemap/HTML crawling of the owner's own site (free, own code).

## Biggest gaps
1. Keyword volume / difficulty for new-content ideas (no free official source; autocomplete + Bing related keywords + Trends are proxies).
2. Rank for keywords with no impressions, and competitor rankings (no free permitted SERP source with Google positions).
3. Real backlink index (GSC has no endpoint; Bing gives own-site links only; Common Crawl graph is domain-level and heavy; no free anchor/page-level data from a big index).
