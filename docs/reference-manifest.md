# Reference manifest (PLAN3 P3-006)

## Pinned revisions

- Pi: `d5b3de22d0e24a3e9c38a9410d3329b099717283` (`temp/pi-mono`)
- psi baseline: `d45a88b8563b7c1cde13e0f33f89b9abacb6cf24`

## Toolchain

- Go: `go1.26.4 linux/amd64` (module declares `go 1.24.2`)
- Node: `v24.11.1` (survey probes only; never a runtime dependency)
- proper-lockfile: `4.1.2` (installed public package, resolved locally for Pi auth probes)

## Session format

Pi JSONL v3. Unknown fields preserved byte-for-byte.

## Fixture policy

All fixtures synthetic. No real credentials, paths, transcripts, or prompts.
Regression subprocesses use isolated HOME/workspace/agent/session dirs with an
allowlisted environment. Live provider tests require `-tags liveprovider` plus
explicit operator approval and never run in normal checks.
