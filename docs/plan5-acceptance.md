# PLAN5 acceptance ledger

## Status

PLAN5 implementation is in progress. This ledger records the compaction checkpoint and theme-core checkpoint; it does not mark any PLAN5 gate complete. Theme application/discovery, terminal query routing, message/tool hierarchy, tool summaries, replay, and remaining platform and human checks are still open.

Reference revisions:

- psi starting point: `e88b504`.
- Pi: `d5b3de22d0e24a3e9c38a9410d3329b099717283`.
- Codex: `23934d31b15b1127c83535f84854e5009f7cd51d`.

No live provider request was made for this checkpoint. Tests use fake providers and isolated PTYs.

## Compaction checkpoint

Ported Pi's projected cut-point policy for psi's retained session roles from `packages/coding-agent/src/core/compaction/compaction.ts` (`findProjectedCutPoint`, `isCutPointMessage`, and `isTurnStartMessage`). Preparation uses the canonical `session.Projection`, so context edits and the latest compaction boundary are respected. It distinguishes eligible work, empty history, history within the retained budget, and an already-compacted path. The existing explicit zero-retention behavior remains a documented psi exception.

A split turn is summarized in two stages, following Pi's history and turn-prefix model and the pinned prompts. Previous summaries are sent through Pi's update-summary prompt path. Usage from completed summary calls is aggregated in the compaction entry; known usage is separately persisted if a required later summary is incomplete or fails. No checkpoint is appended unless every required summary completes. Preparation also follows Pi's recovery-omission suffix adjustment, projects context replacements before selecting summary input, and never cuts between a tool call and its trailing result.

Manual `/compact` now performs read-only preparation before showing a spinner or requiring credentials. Expected no-work results use a muted informational notice and make no provider request or session change. Threshold no-work continues the pending request; overflow no-work returns the original context-limit failure without retrying.

### Evidence

| Level | Evidence | Result |
|---|---|---|
| Component | `TestPrepareCompactionReportsNoWorkOutcomes`, `TestPrepareCompactionSplitsLongAssistantSpanAtPiCutPoint`, `TestLongSingleUserSpanCompactsAtOrdinaryRetentionBudget` | Pass; the long-span fixture uses the ordinary 20,000-token retention budget. |
| Agent | Two-stage split summary, previous-summary update, aggregate usage, failure/incomplete-summary usage, no-checkpoint, threshold no-op, overflow no-op, retain-none, projected replacement, omitted recovery suffix, and tool-call/result boundary regressions | Pass with fake providers. |
| Actual app | `TestActualEmptyCompactIsOfflineNoOpWithoutIndicator`; `TestActualCompactionShowsPiIndicatorAndSettles` uses a real PTY, loopback Anthropic server, and ordinary 20,000-token retention | Pass. The actual app request includes the selected prefix and excludes the retained continuation; the saved `firstKeptEntryId` is checked. |
| Reference | Pi source reviewed at the pinned revision for cut points, split prompts, and usage combination | Source comparison only; no full Pi runtime session. |
| Human | Visual review, selection/copy, flicker, and macOS/Windows Terminal acceptance | Not performed. |

Validation at this checkpoint: `./scripts/check.sh` passed, including the full race run. The actual-app long-span PTY passed under the race detector after keeping its fixture to two 80,000-character assistant updates; it uses the ordinary 20,000-token retention setting.

## Theme-core checkpoint

Implemented strict Pi theme JSON parsing for the 56 pinned tokens, their foreground/background slots, the five missing-only fallbacks, chained variable references, inert `$schema`, exact names, BOM handling, and strict top-level/color/export fields. Added both unmodified Pi built-ins and verified their SHA-256 hashes and resolved colors. Pi color parsing and conversion cover RGB, indexed/default, OKLCH, OKHSL, sRGB gamut mapping, 256-color quantization, and truecolor output. The port retains the OKHSL/OKLab MIT notice.

Ported Pi's `system` family map, contrast curves, role rules, three fallback tiers, appearance detection, panel limits, dim fallbacks, and default-foreground handling. Seven complete 56-token fixtures under `internal/theme/testdata/pi-system-theme.jsonl` are generated from the pinned `system-theme.ts` and compared token by token: dark, light, mid-gray, complete palette, no replies, light appearance hint, and pending grayscale. All fixtures match exactly. WCAG checks verify generated text against representative message/tool surfaces. These results establish a local implementation/reference comparison, not terminal or actual-app theme acceptance.

Added config loading for `theme`, `themes`, and `terminal.trueColor`, with user/project scalar precedence, source bases, project-consent gating, and focused tests. These settings are not yet wired to startup or a CLI override.

Pi reference source hashes for this checkpoint:

| Source | SHA-256 |
|---|---|
| `system-theme.ts` | `877a3dc24fd194f2dc5ee9efe8f1699acdcdac6defa197009bc7b39920bbb729` |
| `theme-tokens.ts` | `6c38a169da4f85ee04bc31980a13ef6a498b6be68e63faceda4f5a1e5c632ecf` |
| `theme-schema.ts` | `6440ccc6aa0576f0f9447584b707b3bf09f2b77aed64687f4598db261588474b` |
| `tui/src/colors.ts` | `d4fe729c424d2c07bc64cf0c3edfdbf5642865cba395dfb37234c6c88d65f468` |
| `tui/src/oklab.ts` | `45b067e6e3605b385f595adecd7c0216f1c6b6686800d5c73f661286de736be6` |

Validation at this checkpoint: `go test ./internal/theme ./internal/config -count=1 -race` and `git diff --check` passed.

## Remaining PLAN5 work

- Complete compaction preparation acceptance: cancellation between summary calls, append-failure/restart cases, and pinned Pi session-reader acceptance. Continue differential tests for cut-point edge cases.
- Wire the theme core into startup, `--use-theme`, `--theme`, `/reload`, `/session`, project trust discovery, configured/local/package resource discovery, collisions, and the user's early bootstrap UI.
- Add bounded OSC color and appearance reply decoding to the existing input owner, query/late-reply lifecycle, automatic pairs, user-theme watching, and clean suspend/resume/shutdown behavior.
- Replace terminal `tokenSGR` use with immutable theme snapshots; add cell-aware foreground/background styling to user, assistant, metadata, notices, Markdown, tool rows, pickers, editor, and footer.
- Implement role-aware surfaces and bounded live/lazy-replay tool summaries with separate hidden-body and credential canaries.
- Complete retry/status, metadata, navigation, lifecycle, resource, platform, and human acceptance cases.
- Update compatibility and user documentation, then build and checksum the executable from the final clean commit.

G5-01 through G5-08 remain open. Missing native/platform and human evidence must remain blocked, not passed.
