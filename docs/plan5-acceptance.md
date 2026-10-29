# PLAN5 acceptance ledger

## Status

PLAN5 implementation is in progress. This ledger records the first compaction checkpoint only; it does not mark any PLAN5 gate complete. Theme compatibility, message/tool hierarchy, tool summaries, replay, and the remaining platform and human checks are still open.

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

## Remaining PLAN5 work

- Complete compaction preparation acceptance: cancellation between summary calls, append-failure/restart cases, and pinned Pi session-reader acceptance. Continue differential tests for cut-point edge cases.
- Implement Pi theme data, strict schema, color conversions, discovery/trust, terminal replies, reload, and theme-aware styling.
- Implement role-aware message surfaces and bounded live/lazy-replay tool summaries with separate hidden-body and credential canaries.
- Complete retry/status, metadata, navigation, lifecycle, resource, platform, and human acceptance cases.
- Update compatibility and user documentation, then build and checksum the executable from the final clean commit.

G5-01 through G5-08 remain open. Missing native/platform and human evidence must remain blocked, not passed.
