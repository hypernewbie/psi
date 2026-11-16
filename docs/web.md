# Web access

psi has native web tools. They read the `pi-web-access` plugin configuration
file, `web-search.json`, so the same settings apply to Pi and psi. psi does not
host the TypeScript extension, an MCP server, or a browser renderer.

## Tools

`fetch_content` fetches one URL or a batch of up to 20.
`web_search` searches with one query or a batch of up to three.
`get_search_content` pages retained text by `responseId`.

### Fetch

- `url` or `urls` (exactly one): an absolute HTTP(S) URL, or 1-20 URLs.
- `mode`: `readable` (default) converts HTML to Markdown. `raw` returns decoded
  text without HTML conversion.

Web tools are internal calls: they serialize with `loop_control` and
`syncboard` calls in the same batch. File and shell built-ins can still overlap
them.

Batches use at most three workers and return sections in input order, with a
per-URL error for each failure. All URL targets are validated before any
request; one syntactically invalid entry fails the batch without fetching the
others. A batch that has some successes is not an error and is marked
`incomplete: true`. A batch where every URL fails returns an error and stores
nothing. Content is stored in the same order and one `responseId` covers the
batch. If the result cap omits whole URL sections, the output reports the
omitted count instead of silently ending at `[truncated]`.

### Search

`web_search` returns ranked result metadata plus a `responseId`:

- `query` or `queries` (exactly one): at most 4 KiB per query, 1-3 queries.
- `numResults`: 1-20 per query (default 5).
- `provider`: `auto`, `searxng`, or `exa`. `auto` uses a configured SearXNG
  instance, else Exa. If SearXNG was configured but is invalid, `auto` reports
  a SearXNG error instead of silently using Exa. An explicit unavailable
  provider errors and never falls back. Exa with a key uses `POST /search`;
  without a key it uses the keyless `mcp.exa.ai` JSON-RPC endpoint. A failed
  keyed request does not retry keyless. Keyless prose without a parseable
  result is an invalid response, not an empty search.
- `domainFilter`: up to 20 ASCII hostnames. A leading `-` excludes. Filtering
  applies before the per-query result limit.
- `recencyFilter`: `day`, `month`, or `year`.
- `includeContent`: fetch and store each result page. Requires `fetch_content`
  to be selected and enabled; otherwise the call fails before any search or
  page request. Uses the configured `fetch.defaultMode`, including raw-only
  configurations.

Queries run with at most three workers and sections keep input order. Duplicate
URLs are kept once, at their first position. Search results are metadata only
unless `includeContent` is true; use `fetch_content` or retrieve a stored page
with `get_search_content`. An empty result list is valid. Degraded engines
appear as bounded reasons when the instance returns none.

`includeContent` shares a 90-second deadline across search and page fetches.
Fetched pages are stored in result order. A failed page records a per-result
error without failing the search. Any failed query or page marks the snapshot
`incomplete: true`; retrieval metadata repeats the flag. Cancellation stops new
work, keeps completed results, and also marks the snapshot incomplete.

SearXNG gets `site:` and `-site:` operators plus `time_range`. The Exa direct
API gets `includeDomains`, `excludeDomains`, and a rolling UTC
`startPublishedDate` (1, 30, or 365 days). Keyless Exa appends the same
operators and a recency phrase to its query.

`readable` emits headings, paragraphs, lists, links, code blocks, blockquotes,
and simple table rows. It skips scripts, styles, navigation, footers, and
sidebars. Relative links resolve against the final page URL; non-HTTP,
credential-bearing, malformed, and overlong links are dropped.

The model-facing result is limited to 2000 lines or 50 KiB, including metadata.
The response body is capped at 5 MiB of decompressed text; a larger body is
fetched only up to the cap and marked `source-truncated: true`. A result cut by
the output limit ends with `[truncated]`; a batch or search result that drops
whole numbered sections ends with `[truncated; N section(s) omitted]`.

Fetched content is retained in memory and `fetch_content` returns a
`responseId`. `get_search_content` pages that text with `offset` and `limit`, or
jumps to a match with `findText`. Retrieval metadata repeats `incomplete` and
`source-truncated` flags. The store is bounded (default 64 entries and 64 MiB,
LRU); an oversized entry is not retained. IDs are process-local: a restart or
eviction loses them, and nothing is written to disk. The byte limit counts
retained string bytes, not object overhead, total process memory, or in-flight
batches. The session file keeps only the bounded tool-result preview that was
shown to the model, not the retained text behind a `responseId`.

`get_search_content` returns metadata lines, then a final `content:` line,
then the exact stored text prefix. It adds no whitespace inside the payload.
`offset` and `length` count Unicode scalar values, not UTF-16 units.
When more text remains, use the returned `next-offset`; it accounts for the
actual payload after byte and line limits, not the requested page size.

Web page text is untrusted data. psi labels it in the result, but the label does
not prevent prompt injection. Treat fetched text as evidence, never as
instructions. Tool errors, including validation failures, use the same bounded
output formatter as successful results.

## Configuration

Resolved path, in order:

1. `$PI_CODING_AGENT_DIR/web-search.json` when `PI_CODING_AGENT_DIR` is set.
2. With `XDG_CONFIG_HOME`: an existing `$XDG_CONFIG_HOME/pi/web-search.json`,
   else an existing `~/.pi/web-search.json`, else the XDG path.
3. Without XDG: an existing `~/.pi/agent/web-search.json`, else an existing
   `~/.pi/web-search.json`, else the agent path.

`PSI_CODING_AGENT_DIR` does not affect this plugin file.

Supported keys:

| Key | Effect |
|---|---|
| `provider`, `searchProvider` | `auto`, `searxng`, or `exa`; `auto` selects configured SearXNG, else Exa |
| `searxngBaseUrl`, `searxngHeaders` | SearXNG endpoint and extra request headers |
| `exaApiKey`, `exaBaseUrl` | Exa credentials and endpoint |
| `allowHttpBaseUrls` | Allow plain-HTTP provider base URLs outside loopback |
| `webSearch.enabled` | Disable `web_search` |
| `tools.webSearch.enabled`, `tools.fetchContent.enabled`, `tools.getSearchContent.enabled` | Per-tool overrides |
| `fetch.defaultMode`, `fetch.allowedModes` | Fetch mode defaults and the effective allowed set |
| `maxInlineContentChars` | Inline character limit (default 30000; below 1000 resets to the default; capped at 200000) |
| `ssrf.allowRanges` | CIDR exemptions from the private-address block |
| `cache.maxEntries`, `cache.maxBytes` | In-memory store limits (default 64 and 64 MiB) |

Unknown keys are ignored. A key for a provider psi does not support is ignored
without a diagnostic, and its value is never reported. A configuration file
larger than 1 MiB is rejected before it is read.

Environment overrides:

| Variable | Effect |
|---|---|
| `SEARXNG_BASE_URL` | Overrides `searxngBaseUrl`; an empty value leaves SearXNG unconfigured |
| `EXA_BASE_URL` | Overrides `exaBaseUrl`; an empty value is invalid |
| `EXA_API_KEY` | Overrides a configured literal key |
| `PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS` | `true`, `false`, `1`, or `0`; overrides `allowHttpBaseUrls` |

These overrides also apply when `web-search.json` is absent.

`exaApiKey` accepts a literal value or `$NAME`. A `!command` value is refused;
psi never runs a command to obtain a credential.

A configured `searxngBaseUrl` must be an absolute HTTP(S) URL with a host and
without credentials, query, or fragment. Plain HTTP requires loopback or
`allowHttpBaseUrls`.

## Enablement

psi tool selection wins first: `defaultTools`, `--tools`, `--exclude-tools`, and
`--no-tools` decide whether a web tool is advertised. Without a per-tool
override, `web_search` follows `webSearch.enabled`, while `fetch_content` and
`get_search_content` default to enabled. Excluding `fetch_content` also blocks
inline page fetching by `web_search`; metadata-only search remains available.

Failures are scoped:

- An unreadable or invalid shared configuration disables every web tool. Shared
  values include HTTP permission, address exemptions, cache limits, inline
  limits, and enablement objects.
- Invalid fetch mode settings disable `fetch_content` only.
- Invalid routing disables `web_search` only.
- Invalid provider settings disable that provider only.

Failures produce bounded startup notices that never contain configuration
values.

## Network guard

psi fetches directly. It uses private HTTP transports, ignores proxy
environment variables, and keeps TLS certificate and hostname checks.

Every URL and redirect target is validated. At most five redirects are followed.
HTTPS-to-HTTP downgrades are refused. Provider requests refuse cross-origin
redirects.

DNS resolution happens inside the dialer. Every resolved address is checked and
the checked literal address is dialed, so a second lookup cannot swap the
destination. Private, loopback, link-local, CGNAT, multicast, documentation,
and metadata destinations are denied. SearXNG gets a loopback exception only
for its own configured origin. `ssrf.allowRanges` bypasses the address denial,
not URL or TLS checks.

This guard is defense in depth, not a sandbox. The `bash` tool can make its own
network requests.

## Limits and failure behavior

- Dial budget: 10 seconds for DNS and all connection attempts.
- TLS handshake: 10 seconds. Response headers: 30 seconds.
- Provider request: 30 seconds; the keyless Exa MCP request: 60 seconds.
- Response headers: 64 KiB. Provider bodies: 5 MiB.
- One fetch call: 30 seconds, including redirects and body reads. A batch runs
  at most three fetches at a time.
- A search without content: one provider request per query. With content: 90
  seconds total, at most three page fetches at a time, and at most 60 pages.
- No application retries and no provider fallback.

Errors use fixed classes: validation, blocked target, DNS, TLS/transport,
timeout, cancellation, HTTP status, invalid response, and rate limit. Responses
never include raw transport errors, provider bodies, headers, or configured
values. Configured Exa keys and SearXNG header values are redacted from fetched
text, URLs, and provider metadata before output clipping. Stored text is already
redacted; retrieval does not scan redaction markers again.

## Not supported

`answer` and `extract` fetch modes, PDF parsing, images, video, GitHub cloning,
provider fetch routing (Crawl4AI, Firecrawl), browser cookies, response caching
to disk, and the `source_check` tool. See [Limitations](limitations.md).
