# Robustness ledger (PLAN3)

Reference: `temp/pi-mono` rev `d5b3de22d0e24a3e9c38a9410d3329b099717283`, session v3.
psi baseline: `d45a88b8563b7c1cde13e0f33f89b9abacb6cf24`.
Environment: Linux amd64, WSL2, Go 1.26.4 (module declares 1.24.2), Node v24.11.1.

## Evidence classes

| Label | Meaning |
|---|---|
| Reproduced | Synthetic psi probe observed the stated behavior. |
| Reference executed | Probe ran pinned Pi source or stated Node primitive. |
| Reference reviewed | Comparison from pinned Pi source. |
| Review risk | Code path needs deterministic test; no failure claim. |
| Shared limitation | Pi shares it; no new psi requirement. |
| Existing protection | Current implementation already guards/tests. |

Survey probes that observe a target defect report `PASS` as instruments,
not as acceptance. Permanent regressions assert corrected behavior.

## Finding ledger

| ID | Pri | Finding | Evidence | Status |
|---|---|---|---|---|
| F01 | P1 | Auth reads ignore writer lease | Reproduced, reference reviewed (R01) | Open |
| F02 | P1 | Abandoned auth leases never recover | Reproduced, reference executed (R02) | Open |
| F03 | P2 | OAuth refresh changes existing permissions | Reproduced, reference executed | Open |
| F04 | P1 | Lazy reads mix file generations | Reproduced, reference reviewed (R03) | Open |
| F05 | P1 | Decoders accept case aliases | Reproduced, Node primitive executed | Open |
| F06 | P1 | Mutation continues after cancellation | Reproduced with barriers, reference reviewed (R05/R06) | Open |
| F07 | P1 | Fixed drain deadline loses descendant output | Reproduced, reference executed (R08) | Open |
| F08 | P1 | Anthropic accumulation quadratic | Reproduced, source reviewed, V8 microcheck | Open |
| F09 | P1 | Word deletion scans prefixes quadratically | Reproduced, reference executed (R13) | Open |
| F10 | P1 | Multiline status breaks row accounting | Reproduced, reference reviewed (R14) | Open |
| F11 | P2 | Recall bypasses editor limit | Reproduced, psi invariant defect | Open |
| F12 | P2 | Stream failure discards known usage | Reproduced, reference reviewed (R10/R11/R12) | Open |
| F13 | P2 | Tool modes differ from Node defaults | Reproduced, Node primitive executed | Open |
| F14 | P2 | Compaction threshold addition can overflow | Reproduced, arithmetic reviewed (R15) | Open |
| F15 | P2 | Discovery retains full skill bodies | Reproduced, reference reviewed (R16) | Open |

Reference anchors R01–R18: see `temp/PLAN3.md` §4.
Installed `proper-lockfile@4.1.2` provenance recorded in `docs/reference-manifest.md`.

## Scope rule

Pi defines the boundary. Shared limitations (§5 of plan) do not become psi
requirements. Go-specific regressions stay in scope despite similar sources.
Preserved psi safety choices: bounded input, strict session reads, refusal
after uncertain storage writes.

## Product limits

Four tools (`read/write/edit/bash`), two provider families (Anthropic
Messages, `openai-codex`), inline main-screen terminal, native scrollback,
no tool args/results/diffs in terminal, Pi JSONL v3 compatible, no
extensions/MCP/RPC/sandbox/session-tree/login/telemetry/updates/background
service, no live provider calls without explicit approval.
