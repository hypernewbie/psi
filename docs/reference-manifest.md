# Reference manifest (PLAN3 P3-006)

> Historical record. The Node reference adapter, its build scripts, and the
> opt-in comparison tests were retired. The commands that built and ran them no
> longer exist. The results and hashes below describe past runs and are not
> current instructions.

## Pinned revisions

- Pi: `c3cc91632d0a1950fb122605b190233f2aa0006e` (`temp/pi-mono`).
- psi baseline: `d45a88b8563b7c1cde13e0f33f89b9abacb6cf24`.
- PLAN3 implementation review starts at `2bb1c55`.

## Toolchain

- Go: `go1.26.4 linux/amd64`. The module declares `go 1.24.2`.
- Node: `v24.11.1` was used for the retired reference probes. It is not a psi
  runtime or build dependency, and no reference probe remains in the tree.
- Public `proper-lockfile`: `4.1.2` was an explicit input to the retired
  mixed-auth comparison.

## Reference adapter

The retired `scripts/build-reference.mjs` required the pinned revision and a checkout without tracked changes.
It imported unmodified Pi source through a TypeScript loader, including the session reader and compaction preparation/generation.
It does not load agent configuration, credentials, extensions, or provider factories. The optional compact-flow comparison injects a synthetic stream; the `completeSimple` stub throws if a provider call is attempted.
Public dependencies come from the installed coding-agent package, never an agent data directory.

The adapter records these source hashes:

| Source | SHA-256 |
|---|---|
| `packages/coding-agent/src/core/session-manager.ts` | `4c95253a50b3a7f3dd3d4320c5e535c9556e88140d81a426089c455c2ca2ed93` |
| `packages/coding-agent/src/core/compaction/compaction.ts` | `32714966eb644da31cd84e8eb172965256160626b3d26ae9752c3894300f2b0b` |
| `packages/tui/src/word-navigation.ts` | `b73e915a524926ac8881731e89026b0bc7b5b0bd465de67257af81a420465c7f` |
| `packages/agent/src/agent-loop.ts` | `a2c7dc92644bae8655a263e1f5b6ecca6a0d3057002f978e709ed27ec8dd5a92` |
| `packages/coding-agent/src/core/tools/file-mutation-queue.ts` | `33cb06ac9bcdf32c8b84d9d12e33be44c503a7670f668e003a3262cd34294d11` |

The generated manifest uses adapter version `native-node-types-v3-compaction-agent-loop`.
Session reference tests use the public `cross-spawn` dependency.
Word-navigation reference tests also use public `get-east-asian-width`.
Agent-loop reference tests also use public `typebox`.

### Retired reference checks

These opt-in comparisons are no longer available:

- the Pi session continuation and projection matrix;
- compaction preparation and split-summary comparisons;
- parallel and sequential batch traces;
- word-navigation parity;
- mixed-auth contention against public `proper-lockfile`;
- optional PLAN6 payload comparisons and the Responses input comparison.

Go-only coverage remains for session interchange, lease contention and
abandoned-holder recovery, provider request payloads, word navigation, and
Markdown parity. No live Pi reader or Node adapter is available, so exact
executable parity with the pinned source remains unclaimed.

## Session format

Pi JSONL v3. Continuation preserves the existing JSONL prefix, including its opaque fields.
The semantic fuzzer compares eager and indexed state, including usage, tool calls, provenance, and headers.
The former pinned Pi interchange matrix was retired; its recorded results are historical.

## Fixture policy

All fixtures are synthetic.
No real credentials, private configuration, transcripts, or prompts enter fixtures.
Regression subprocesses use isolated directories and an allowlisted environment.
Live provider tests require the `liveprovider` build tag and explicit operator approval.
Normal checks never run live provider tests.

Fixture-hash and retained-artifact records remain incomplete in the full PLAN3 gate ledger.
