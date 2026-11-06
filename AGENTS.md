# Repository instructions

## Git history

- Never merge the default branch into a feature branch. Rebase the feature branch onto `main` instead. History stays linear, and no merge commits reach `main`.

## Tests

Each behavioral fix must include a focused regression test for the broken behavior.

Tests in the default CI suite must:

- Assert important, observable behavior or protect a fixed regression.
- Add unique coverage. Do not duplicate an existing test or test trivial implementation details.
- Be deterministic across supported platforms and under host load. Use explicit events or fakes, not sleeps, retries, or wall-clock timing budgets.
- Have a small, bounded runtime and not materially slow CI.

Run performance, stress, and environment-sensitive checks as benchmarks or explicitly tagged acceptance suites at a slower cadence. Do not make their timing measurements routine CI pass/fail conditions. In this repository those profiles live in `.github/workflows/stress.yml` (weekly schedule and manual dispatch), never in `.github/workflows/ci.yml`.

## Keep documentation and prompts current

For each tool or capability that you add, change, rename, or remove, complete these steps in the same change:

- Update the affected documentation in `docs/` and the CLI help.
- Update the affected system-prompt guidance in `internal/config/prompt.go` and other prompt sources.
- Describe the actual availability, permissions, limits, and failure behavior.
- If documentation topics change, update the topic list in `docs/docs.go`.
- Update the affected prompt fixtures and regression tests.

Before you report completion, check that the documentation and prompt guidance match the implementation.
If documentation or prompt changes are unnecessary, explain why in the change summary.
