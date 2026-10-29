# Reference manifest (PLAN3 P3-006)

## Pinned revisions

- Pi: `d5b3de22d0e24a3e9c38a9410d3329b099717283` (`temp/pi-mono`).
- psi baseline: `d45a88b8563b7c1cde13e0f33f89b9abacb6cf24`.
- PLAN3 implementation review starts at `2bb1c55`.

## Toolchain

- Go: `go1.26.4 linux/amd64`. The module declares `go 1.24.2`.
- Node: `v24.11.1`, for reference probes only. Node is not a psi runtime dependency.
- Public `proper-lockfile`: `4.1.2`. The mixed-auth test checks the package name and version.

## Reference adapter

`scripts/build-reference.mjs` requires the pinned revision and a checkout without tracked changes.
It imports unmodified Pi source through a TypeScript loader, including the session reader and compaction preparation/generation.
It does not load agent configuration, credentials, extensions, or provider factories. The optional compact-flow comparison injects a synthetic stream; the `completeSimple` stub throws if a provider call is attempted.
Public dependencies come from the installed coding-agent package, never an agent data directory.

The adapter records these source hashes:

| Source | SHA-256 |
|---|---|
| `packages/coding-agent/src/core/session-manager.ts` | `6dd4b725c91f7b3102ec30b4e7d26f0ae486e603e9744b2eca781d57c2a7466c` |
| `packages/coding-agent/src/core/compaction/compaction.ts` | `32714966eb644da31cd84e8eb172965256160626b3d26ae9752c3894300f2b0b` |
| `packages/tui/src/word-navigation.ts` | `b73e915a524926ac8881731e89026b0bc7b5b0bd465de67257af81a420465c7f` |

The generated manifest uses adapter version `native-node-types-v3-compaction`.
Session reference tests use the public `cross-spawn` dependency.
Word-navigation reference tests also use public `get-east-asian-width`.

### Optional reference checks

Build the adapter:

```sh
node scripts/build-reference.mjs temp/pi-mono temp/pi-reference.mjs
```

Run the session, compaction, and word-navigation reference checks:

```sh
PSI_PI_SESSION_MODULE="file://$PWD/temp/pi-reference.mjs" \
  go test ./internal/session -run '^TestReferencePi' -count=2

PSI_PI_SESSION_MODULE="file://$PWD/temp/pi-reference.mjs" \
  go test ./internal/agent -run '^TestPiCompactionCutPointDifferential$' -count=1

go test ./internal/terminal -run '^TestF09WordParityWithPi$' -count=3 \
  -args -psi-test-pi-word-module="file://$PWD/temp/pi-reference.mjs"
```

Resolve the public dependency path:

```sh
PROPER_LOCKFILE="$(node -p 'require("node:path").join(require("node:path").dirname(process.execPath), "..", "lib/node_modules/@earendil-works/pi-coding-agent/node_modules/proper-lockfile")')"
```

Run the mixed-auth checks:

```sh
go test ./internal/provider -run '^TestMixedPi' -count=3 \
  -args -psi-test-proper-lockfile="$PROPER_LOCKFILE"
```

The word-navigation and mixed-auth checks skip without their explicit flags.
A requested reference check fails if its dependency is absent or invalid.
Reference subprocesses use isolated environments without inherited Node loader flags.

## Session format

Pi JSONL v3. Continuation preserves the existing JSONL prefix, including its opaque fields.
The semantic fuzzer compares eager and indexed state, including usage, tool calls, provenance, and headers.
This comparison is not a substitute for the pinned Pi interchange matrix.

## Fixture policy

All fixtures are synthetic.
No real credentials, private configuration, transcripts, or prompts enter fixtures.
Regression subprocesses use isolated directories and an allowlisted environment.
Live provider tests require the `liveprovider` build tag and explicit operator approval.
Normal checks never run live provider tests.

Fixture-hash and retained-artifact records remain incomplete in the full PLAN3 gate ledger.
