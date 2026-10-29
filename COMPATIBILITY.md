# Compatibility contract (Phase 0 gate)

Pinned upstream reference:

- Clone URL: `https://github.com/badlogic/pi-mono.git`
- Revision: `d5b3de22d0e24a3e9c38a9410d3329b099717283`
- Package: `packages/coding-agent/` version `1.4.0`
- Session version: `3` (`CURRENT_SESSION_VERSION`)
- Local checkout (ignored): `temp/pi-mono/`

Session compatibility is about entry shapes and projection semantics, not CLI,
configuration, or display parity. The revision above is the final reference for
field shapes and behavior; prose docs lag source.

## Session (v3) — supported

| Record | Read | Write | Notes |
|---|---|---|---|
| `session` header v3 | required | required | `type,id,timestamp,cwd,parentSession?`; refuse v1/v2 writes, refuse future versions |
| `message` (system/user/assistant/toolResult) | required | required | preserve provider replay metadata, unknown content metadata, signatures |
| `thinking_level_change` | required | required | session-local |
| `model_change` | required | required | session-local `{provider, modelId}` |
| `usage` | preserve/read totals | only if required | never invent measured values |
| `compaction` | required | required | `summary, firstKeptEntryId, tokensBefore, details?, usage?, systemMessage?`; keep boundary semantics |
| `branch_summary` | required | not written | contributes context as `branchSummary` |
| `custom` | preserve | `psi.input` provenance only | state only, no context |
| `custom_message` | required | not written | contributes context as `custom`, no extension execution |
| `context_edit` | required | limited | `replacement: {content} \| null`; latest wins; string normalized for assistant/toolResult |
| `label` | preserve | not written (first release writes labels only via explicit command if added) | metadata, not instructions |
| `session_info` | preserve/read | not written (name only if explicit) | metadata |
| historical `bashExecution` | required for supported forms | not written | respect `excludeFromContext` |
| historical extension tool call/result | preserve/read | not executable | completed history only |
| historical images | preserve/read | selected read subset | no terminal image display |
| branch graph | required (read) | append current branch only | no navigation/branch UI |
| v1/v2 sessions | diagnose | no auto-migration | use Pi migration on a copy |
| future version / unsafe unknown context | diagnose | refuse | no lossy continuation |
| deferred/background runtime | preserve | refuse continuation | no provider-job subsystem |

Additional storage rules:

- JSONL: one complete JSON record per line, `\n` terminated.
- Preserve the original file prefix byte-for-byte; never rewrite a whole file to append.
- Detect truncated-tail before append; stop, do not silently concatenate or discard.
- Refuse unsupported versions without replacing the file with an empty session.
- Two psi processes must not mutate one session concurrently (advisory lock + generation check).
- Pi does not honor the psi lock; concurrent Pi+psi writes are out of scope (documented).

## Configuration subset

| Area | Supported | Boundary |
|---|---|---|
| `defaultProvider`, `defaultModel` | `anthropic` and `openai-codex` | reject unsupported families without changing defaults |
| `defaultThinkingLevel` | `off,minimal,low,medium,high,xhigh,max` | Anthropic budget/adaptive profiles and Codex effort maps |
| `sessionDir` | `PI_CODING_AGENT_SESSION_DIR` env + `--session-dir` + `sessionDir` key | Pi-compatible resolution (see below) |
| `defaultTools` | `read,bash,edit,write` with replacement and `+`/`-` | four tools only |
| `skills` | `skills`, `enableSkillCommands` selector + path subset | instructions only, no install/run at discovery |
| `compaction` | `compaction.enabled,reserveTokens,keepRecentTokens` | compatible entries, bounded summary |
| `shell` | `shellPath`, `shellCommandPrefix` | explicit consent for project-supplied executables |
| `network` | idle timeout + bounded retries | one HTTP transport |
| instructions | `AGENTS.override.md`, `AGENTS.md`, fallbacks, `SYSTEM.md`, `APPEND_SYSTEM.md` | documented discovery + consent |
| `models.json` | endpoints, model metadata and overrides for supported providers | public versioned snapshot. No catalog refresh |
| auth | Anthropic API keys; existing Codex OAuth login and refresh | no login UI or credential-command execution |
| startup | `quietStartup` | no dynamic renderer |
| terminal | always inline | ignore fullscreen/rich preferences |

Unsupported package/extension declarations never trigger install or code loading.
Unknown harmless UI prefs need no per-key warning.

## Provider

- **Anthropic Messages API** (`https://api.anthropic.com/v1/messages`, SSE).
  Auth: existing `auth.json` API-key entry, then a configured model credential,
  then `ANTHROPIC_API_KEY`. Anthropic subscription OAuth is not implemented;
  an existing OAuth entry refuses API-key fallback.
- **ChatGPT Codex Responses** (`https://chatgpt.com/backend-api/codex/responses`, SSE).
  Uses the existing `auth.json` `openai-codex` OAuth entry. Refresh preserves other
  credentials and unknown fields under a Pi-compatible directory lock. No fallback
  to `OPENAI_API_KEY`. Login remains a Pi operation.
- Translates normalized Pi context to each protocol. Preserves Anthropic thinking
  signatures and Codex encrypted reasoning, text IDs/phases, and tool item/call
  IDs. Session records retain the selected provider/API, not hardcoded Anthropic.
- Bounded retries, server `retry-after` honored within cap; no retry of completed tool side effects; partial streams are not invisible retries.
- Accounting separates stored usage, estimated context, and requested versus returned models. Prices remain unknown without a cost calculation.

## Terminal

- Inline Pi regular-mode composition: native scrollback, horizontal editor borders, and a compact two-line folder/usage/model/thinking footer. No alternate screen, mouse, overlays, image protocol, or transcript viewport.
- Retained components use Pi-derived semantic tokens with indexed terminal-palette fallbacks. This is an internal style table, not a theme engine; `NO_COLOR` disables all generated SGR.
- User submissions, live assistant streams, and history replay share Pi-style padding and a retained CommonMark renderer (headings, emphasis, code, lists, quotes, rules, and visible link destinations). Live output commits stable blocks and retains at most 64 KiB of unresolved source; oversized blocks use a sanitized source fallback shared with replay. No syntax highlighter or OSC 8 links. Compact tool status (`read: done`, `bash: failed`) has no args, result bodies, diffs, or expand-details.
- No reasoning text by default.
- Keyboard (fixed): `Enter` submit, `Ctrl+J` newline, `Ctrl+C` cancels a picker/run (idle clears the draft first), `Ctrl+D`/`/quit` exit, `Ctrl+L` opens model selection, and `Shift+Tab` cycles supported thinking levels.
- Slash-command and skill completion is editor-owned and limited to five visible rows. `Up/Down` navigate active completion/pickers before normal draft/history movement; `Tab` applies completion; `Enter` applies and submits.
- `/model`, `/resume`, and `/thinking` use case-insensitive substring search, stable selection, and at most ten visible inline picker entries. Page keys move by the visible list size. Model, thinking, and completion rows use Pi's `→` pointer and `✓` current marker; session rows keep Pi's `›` pointer. Model rows carry provider badges and only the selected model shows API/context/credential details. Session rows prioritize a name or prompt preview. Each picker uses Pi's component-specific no-match text and shows Pi's `(position/total)` indicator when the bounded window cannot show every filtered item. `--resume` uses the same session-picker presentation. Picker input is not a conversation message or recall entry.
- A picker replaces the editor region, as Pi's `showSelector` does. The picker owns its top border, its `> ` search query, the bounded list, and its closing border; the editor block is not rendered underneath it, and the committed transcript above is untouched. Project-resource consent and workspace confirmation use the same region without a search query.
- Project-resource consent uses an inline choice with deny selected by default. The trust choice is per-run only; `--allow-project` and `--deny-project` remain explicit overrides.
- Input during active run is cancellation only (no steering/follow-up queues).
- Offline, interrupted, unsaved, and storage-blocked outcomes use semantic notices. Run settlement carries durable/interrupted/unsaved state; a visible response that fails persistence cannot return the app to a successful idle state.
- Idle `Ctrl+C` clears draft first, second exits (documented); repeated interrupts never create overlapping runs.
- Sanitize untrusted output (ANSI/OSC stripping); respect `NO_COLOR`.
- No Kitty keyboard, no special terminal setup; PTY-tested on xterm-compatible.

## Platform

- Supported: Linux and WSL2 (WSLg not required; TUI is text-only).
- Shell for `bash` tool: `bash` on PATH, run in workspace cwd, process-group cancellation.
- macOS builds and runs the same suite as Linux.
- Windows builds and runs the portable suite. Unix-only coverage stays out:
  process-group cleanup, PTY harness tests, suspend/resume, and mode-bit tests.
  Windows stops the shell leader only. Descendants outlive cancellation.

## Explicit exclusions

TypeScript extensions, JS runtime, npm packages, MCP, codemode, tool search,
subagents, public SDK, RPC server, JSON event mode, print mode, fullscreen mode,
custom widgets, Pi TUI API, session tree/branch/fork/clone UI, HTML export,
sharing, bug uploads, hosted services, update checks, self-update, catalog
refresh, telemetry, browser automation, cloud credential discovery, extra
providers, cache warming, background jobs, virtual models, routing, classifier
models, generated media, clipboard, image paste, external editor, prompt
templates, sandbox, per-tool approval, permission-policy language, automatic Git
ops, visual Pi clone.

Reading excluded history (e.g. old extension result) is ordinary history; psi
never loads or reruns the extension.

## Fixture and source rules

- Only synthetic or operator-approved sanitized sessions in tests; fixtures can be inline or under `internal/**/testdata/`.
- No real credentials, private paths, private file contents, or private prompts.
- Regression subprocesses use temporary homes and an allowlisted environment;
  they do not inherit operator secrets, Pi paths, or live-test opt-in flags.
- Preserve upstream license notices for copied/adapted source or fixtures.
- Never mutate `temp/pi-mono/`; never require ignored files at build/runtime.

## Test evidence and remaining boundaries

The offline suite includes a real app subprocess in a PTY against a local HTTP
server. It covers prompt/configuration wiring, stored credentials, skill
submission, session replacement, and cancellation. A synthetic Codex OAuth
workflow checks a complete model/tool/model round trip and encrypted replay.
OAuth refresh, rotation, cancellation, and concurrent access use dummy tokens
and local HTTP servers. The optional
`PSI_PI_SESSION_MODULE` suite uses the actual pinned Pi reader and writer.
It covers both continuation directions and both provider metadata forms.
A JSON-shape test alone is not evidence of complete Pi interchange.

See `docs/limitations.md` for explicit size limits, reasoning-protocol limits,
and compaction boundaries. See `docs/ui-acceptance.md` for component, emulator,
actual-app, platform, and human evidence for this UI pass. The pinned source
remains the target, not a claim of compatibility with every newer Pi feature.
- Never test against the only copy of a real session.
