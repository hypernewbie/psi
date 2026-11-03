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
| `defaultProvider`, `defaultModel` | `anthropic`, `openai-codex`, `minimax`, `opencode-go`, `deepseek`, `zai`, `google`, `xiaomi`, `moonshotai`, `qwen-token-plan`, `openrouter`, `xai`, `groq`, and `mistral` | use the model's declared registered API; reject unsupported provider/API pairs |
| `defaultThinkingLevel` | `off,minimal,low,medium,high,xhigh,max` | Anthropic budget/adaptive profiles and Codex effort maps |
| `sessionDir` | `PI_CODING_AGENT_SESSION_DIR` env + `--session-dir` + `sessionDir` key | Pi-compatible resolution (see below) |
| `defaultTools` | `read,bash,edit,write` with replacement and `+`/`-` | four tools only |
| `skills` | `skills`, `enableSkillCommands` selector + path subset | instructions only, no install/run at discovery |
| `compaction` | `compaction.enabled,reserveTokens,keepRecentTokens,modelOverrides[provider/model].reserveTokens` | compatible entries, bounded summary; model-specific reserve |
| `shell` | `shellPath`, `shellCommandPrefix` | explicit consent for project-supplied executables |
| `network` | idle timeout + bounded retries | one HTTP transport |
| instructions | `AGENTS.override.md`, `AGENTS.md`, fallbacks, `SYSTEM.md`, `APPEND_SYSTEM.md` | documented discovery + consent |
| `models.json` | endpoints, model metadata and overrides for supported providers | public snapshot and Pi cache. No network refresh |
| auth | Provider API keys for Anthropic, MiniMax, OpenCode Go, DeepSeek, Z.AI, and Google; existing Codex OAuth login and refresh | no login UI or credential-command execution |
| startup | `quietStartup` | no dynamic renderer |
| themes | `theme`, `themes`, `terminal.trueColor`, `--use-theme`, repeatable `--theme`, `--no-themes` | Pi theme data and local resource paths; project resources require consent |
| display | `editorPaddingX`, `outputPad`, `autocompleteMaxVisible`, `markdown.codeBlockIndent` | normalized bounded inline settings |
| terminal | always inline | ignore fullscreen/rich preferences |

Unsupported package/extension declarations never trigger install or code loading.
Unknown harmless UI prefs need no per-key warning.

`/settings` changes the supported subset above in place. Persisted values merge
into the user `settings.json`; unknown keys, `$schema`, nested siblings, and
top-level key order stay. Malformed JSON is refused without writing. Project
`.pi/settings.json` is never written and can still override a user value.
Auto-compact is session-only. `quietStartup: "header"` loads as on and is
written as a boolean only when that row changes.

## Native loop and adaptive compaction controls

Psi natively ports the installed `pi-loop-minimal` and `pi-ac` behaviors; it does not load TypeScript extensions. `/loop` and `/loop-stop` control goal/pass/forever runs. `/ac` and `/autocompact` provide adaptive thresholds, predicted-growth compaction, completed-run compaction, and a session-local summary-model choice. See `internal/ac/README.md` for `/ac` defaults and commands. `/ac` settings reset when the session changes and are not written to `settings.json`.

## Provider



- **Anthropic Messages** uses `ANTHROPIC_API_KEY`; **MiniMax** uses `MINIMAX_API_KEY` and its `/anthropic` base path. Both use the Anthropic Messages stream format. MiniMax thinking controls and OpenCode `allowEmptySignature` follow model metadata.
- **OpenCode Go** uses `OPENCODE_API_KEY`. Each model selects one registered API: Anthropic Messages, OpenAI Chat Completions, Google Generative AI, or generic OpenAI Responses. Its catalog endpoint path is model-specific. Requests add `x-opencode-session` unless an explicit case-insensitive header overrides it.
- **DeepSeek** uses `DEEPSEEK_API_KEY` with OpenAI Chat Completions. **Z.AI** uses `ZAI_API_KEY` with its coding Completions endpoint. Their reasoning and tool-stream behavior follows supported model metadata.
- **Google Generative AI** uses `GEMINI_API_KEY`, `x-goog-api-key`, and the `:streamGenerateContent?alt=sse` route. Google Vertex and OAuth are not implemented.
- **OpenAI Responses** for OpenCode Go uses `OPENCODE_API_KEY`, bearer authentication, `store:false`, and the generic `/responses` route. It is distinct from **ChatGPT Codex Responses**, which uses the `openai-codex` OAuth entry and `/backend-api/codex/responses`. Codex never falls back to `OPENAI_API_KEY`.
- API-key precedence is provider `auth.json`, configured `models.json` key, then that provider's exact environment variable. Unsupported stored auth and `!command` credentials fail closed. No login UI is included.
- The model selects the protocol; psi does not infer API from provider name or forward a model through an unrelated adapter. Unknown compatibility fields and unsupported API pairs remain unavailable. The public catalog is pinned to the provenance in `internal/config/data/catalog-source.json`; Pi's cache is an offline overlay, and psi does not refresh it from the network.
- Adapters translate normalized Pi context and retain supported provider/model identity, usage, reasoning signatures, text IDs/phases, and linked tool IDs in JSONL v3. Bounded retries honor `retry-after` within the configured cap. A stream that has started is not retried, and incomplete calls are never dispatched.
- Accounting separates reported usage, estimated context, and requested versus returned models. Prices remain unknown because psi does not calculate costs. No live provider request has run for PLAN6.

## Terminal

- Inline Pi regular-mode composition: native scrollback, horizontal editor borders, and a compact two-line folder/usage/model/thinking footer. Resize reflows retained displayed source with a Pi-style main-screen rebuild (`CSI 2J`, home, `CSI 3J`); this can erase earlier shell scrollback and change native selection or scroll position. No alternate screen, mouse, overlays, image protocol, or transcript viewport.
- Themes use Pi's pinned JSON schema and all 56 semantic tokens, including the dark/light built-ins, variable references, missing-only optional fallbacks, RGB/indexed/default/OKLCH/OKHSL forms, gamut mapping, and 256-color/truecolor output. `theme` selects `system` by default, a built-in, or a discovered resource; `themes` and repeatable `--theme` add local paths, while `--use-theme` selects by name. Theme data and package theme files are never executed, and schema URLs are never fetched. Project theme bodies are read only after project consent.
- The adaptive `system` theme uses bounded terminal-color/appearance queries, with background-only and no-reply fallback tiers. Late replies do not enter editor input. `/reload` applies a valid snapshot transactionally; invalid selected-theme reloads retain the last-good theme. `/session` reports selection, source, color mode, and diagnostics. `NO_COLOR` disables generated SGR without removing textual hierarchy.
- User submissions, live assistant streams, and history replay share Pi-style padding and a retained CommonMark plus GFM renderer (headings, emphasis, strict strikethrough, code, lists, task lists, quotes, rules, GFM tables, bare URL autolinks, backslash escapes, and visible link destinations). Table detection follows Pi's marked rules, including the delimiter row without outer pipes and the rejection of a header row whose cell count differs from the delimiter row. Table layout, column sizing, cell wrapping, and the narrow-width source fallback follow Pi's Markdown component, and `internal/terminal/testdata/pi-markdown-parity.json` pins the rendered output of 77 samples against the pinned Pi revision. psi retains the source of each displayed transcript occurrence and reflows it at the current width; active Markdown parsing remains bounded to 64 KiB, with oversized blocks using a sanitized source fallback shared with replay. `outputPad`, `editorPaddingX`, `autocompleteMaxVisible`, and `markdown.codeBlockIndent` configure the bounded inline layout. No syntax highlighter, OSC 8 links, LaTeX, or Mermaid. Compact tool status (`read: done`, `bash: failed`) has no args, result bodies, diffs, or expand-details. The active tool row sits above the editor while the call runs and shares the message margin; the settled line replaces it without a duplicate running row, and the replayed tool row uses the same margin.
- No reasoning text by default.
- Keyboard (fixed): `Enter` submit, `Ctrl+J` newline, `Ctrl+C` cancels a picker/run (idle clears the draft first), `Ctrl+D`/`/quit` exit, `Ctrl+L` opens model selection, and `Shift+Tab` cycles supported thinking levels.
- Slash-command and skill completion is editor-owned and limited to five visible rows. `Up/Down` navigate active completion/pickers before normal draft/history movement; `Tab` applies completion; `Enter` applies and submits.
- `/model`, `/resume`, and `/thinking` use case-insensitive substring search, stable selection, and at most ten visible inline picker entries. Page keys move by the visible list size. Model, thinking, and completion rows use Pi's `→` pointer and `✓` current marker; session rows keep Pi's `›` pointer. Model rows carry provider badges and only the selected model shows API/context/credential details. Session rows prioritize a name or prompt preview. Each picker uses Pi's component-specific no-match text and shows Pi's `(position/total)` indicator when the bounded window cannot show every filtered item. `--resume` uses the same session-picker presentation. Picker input is not a conversation message or recall entry.
- A picker replaces the editor region, as Pi's `showSelector` does. The picker owns its top border, its `> ` search query, the bounded list, and its closing border; the editor block is not rendered underneath it. Picker activity does not alter transcript content. A terminal resize rebuilds and reflows the retained main-screen transcript, with the native scrollback effects stated above. Project-resource consent and workspace confirmation use the same region without a search query. The picker key hint stays on one row and shortens on narrow terminals.
- Project trust follows Pi: a folder is asked about only when it holds trust-requiring project resources (`.pi/settings.json`, `.pi/mcp.json`, `.pi/extensions`, `.pi/skills`, `.pi/prompts`, `.pi/themes`, `.pi/SYSTEM.md`, `.pi/APPEND_SYSTEM.md`, or `.agents/skills` in the folder or an ancestor). The user `~/.agents/skills` directory never needs trust. The inline choice offers Pi's options (`Trust`, `Trust parent folder (…)`, `Trust (this session only)`, `Do not trust`, `Do not trust (this session only)`) with `Trust` selected by default. Saved choices are stored in `<agentDir>/trust.json` and the nearest ancestor entry decides, so a folder is asked about once. The global `defaultProjectTrust` setting (`ask`, `always`, `never`) chooses the default; `--allow-project`/`--approve` and `--deny-project`/`--no-approve` remain explicit overrides.
- During an active run, `Enter` queues steering for the next turn and `Alt+Enter` queues follow-up messages until the run ends. `steeringMode` and `followUpMode` accept Pi's `one-at-a-time` (default) and `all` values; psi has no `/steering-mode` command or settings picker, so change them in `settings.json`. `Esc`/`Ctrl+C` cancel and restore queued text. `/ac`, `/autocompact`, and `/loop-stop` have busy-run handling; other slash commands return to the draft.
- Offline, interrupted, unsaved, and storage-blocked outcomes use semantic notices. Run settlement carries durable/interrupted/unsaved state; a visible response that fails persistence cannot return the app to a successful idle state.
- Idle `Ctrl+C` clears draft first, second exits (documented); repeated interrupts never create overlapping runs.
- Sanitize untrusted output (ANSI/OSC stripping); respect `NO_COLOR`.
- No Kitty keyboard, no special terminal setup; PTY-tested on xterm-compatible.

## Platform

- Linux and WSL2 have local runtime and PTY evidence (WSLg is not required; the TUI is text-only).
- Shell for `bash` tool: `bash` on PATH, run in workspace cwd, process-group cancellation.
- macOS is a build target. PLAN5 cross-build and app test-binary compilation passed for amd64 and arm64; native runtime acceptance remains open.
- Windows portable and ConPTY evidence is recorded in `docs/ui-acceptance.md`. PLAN5 cross-build and app test-binary compilation passed. Unix-only coverage stays out: process-group cleanup, PTY harness tests, suspend/resume, and mode-bit tests. Windows stops the shell leader only. Descendants outlive cancellation.

## Explicit exclusions

TypeScript extensions, JS runtime, npm package installation/execution, MCP, codemode, tool search,
subagents, public SDK, RPC server, JSON event mode, print mode, fullscreen mode,
custom widgets, Pi TUI API, session tree/branch/fork/clone UI, HTML export,
sharing, bug uploads, hosted services, update checks, self-update, catalog
refresh, telemetry, browser automation, cloud credential discovery, unregistered
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
and local HTTP servers. The optional `PSI_PI_SESSION_MODULE` suite exercises a configured Pi reader and
writer across both continuation directions and the supported provider/API pairs.
The recorded run used the installed Pi 1.4.0 package adapter, not a verified
build from the pinned source revision. Exact executable Pi parity remains
unclaimed. A JSON-shape test alone is not evidence of complete Pi interchange.

See `docs/limitations.md` for explicit size limits, reasoning-protocol limits,
and compaction boundaries. See `docs/ui-acceptance.md` for component, emulator,
actual-app, platform, and human evidence for this UI pass. The pinned source
remains the target, not a claim of compatibility with every newer Pi feature.
- Never test against the only copy of a real session.
