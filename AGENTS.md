# Repository Instructions

## Tests

Each behavioral fix must include a focused regression test for the broken behavior.

Tests in the default CI suite must:

- Assert important, observable behavior or protect a fixed regression.
- Add unique coverage. Do not duplicate an existing test or test trivial implementation details.
- Be deterministic across supported platforms and under host load. Use explicit events or fakes, not sleeps, retries, or wall-clock timing budgets.
- Have a small, bounded runtime and not materially slow CI.

Run performance, stress, and environment-sensitive checks as benchmarks or explicitly tagged acceptance suites at a slower cadence. Do not make their timing measurements routine CI pass/fail conditions.
