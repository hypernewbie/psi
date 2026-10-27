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
| `custom` | preserve | not written | state only, no context |
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
| `defaultThinkingLevel` | `off,minimal,low,medium,high,xhigh,max` | Anthropic token budgets; Codex effort and `thinkingLevelMap` |
| `sessionDir` | `PI_CODING_AGENT_SESSION_DIR` env + `--session-dir` + `sessionDir` key | Pi-compatible resolution (see below) |
| `defaultTools` | `read,bash,edit,write` with replacement and `+`/`-` | four tools only |
| `skills` | `skills`, `enableSkillCommands` selector + path subset | instructions only, no install/run at discovery |
| `compaction` | `compaction.enabled,reserveTokens,keepRecentTokens` | compatible entries, bounded summary |
| `shell` | `shellPath`, `shellCommandPrefix` | explicit consent for project-supplied executables |
| `network` | idle timeout + bounded retries | one HTTP transport |
| instructions | `AGENTS.override.md`, `AGENTS.md`, fallbacks, `SYSTEM.md`, `APPEND_SYSTEM.md` | documented discovery + consent |
| `models.json` | endpoints, model metadata and overrides for supported providers | small bundled catalog; no catalog refresh |
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
- Usage: report provider usage + catalog estimate separately; never present unknown price as `$0.00`.

## Terminal

- Main screen only, native scrollback, no alternate screen, no mouse, no overlays, no image protocol, no theme engine.
- Assistant text as plain Markdown source; compact tool status (`read: done`, `bash: failed`); no args, no result bodies, no diffs, no expand-details.
- No reasoning text by default.
- Keyboard (fixed): `Enter` submit, `Ctrl+J` newline, `Ctrl+C` cancel run, `Ctrl+D`/`/quit` exit, `Up/Down` history, paste inserts literally.
- Input during active run is cancellation only (no steering/follow-up queues).
- Idle `Ctrl+C` clears draft first, second exits (documented); repeated interrupts never create overlapping runs.
- Sanitize untrusted output (ANSI/OSC stripping); respect `NO_COLOR`.
- No Kitty keyboard, no special terminal setup; PTY-tested on xterm-compatible.

## Platform

- Supported: Linux and WSL2 (WSLg not required; TUI is text-only).
- Shell for `bash` tool: `bash` on PATH, run in workspace cwd, process-group cancellation.
- macOS is a later portability check, not a first-release target.
- Native Windows process behavior excluded unless explicitly requested.

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
`PSI_PI_SESSION_MODULE` test compares a Go-continued synthetic session against
an actual Pi context projector. A JSON-shape test alone is not evidence of
complete Pi interchange.

See `docs/limitations.md` for explicit size limits, reasoning-protocol limits,
and compaction boundaries. The pinned source remains the target, not a claim
of compatibility with every newer Pi feature.
- Never test against the only copy of a real session.
