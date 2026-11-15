# Repository instructions

## Git history

- Never merge the default branch into a feature branch. Rebase the feature branch onto `main` instead. History stays linear, and no merge commits reach `main`.

## Supported platforms

- Linux, Windows, and macOS are first-tier supported platforms.
- Treat reproducible failures on any supported platform as merge blockers.
- Verify platform-specific runtime behavior natively. Cross-compilation alone is not runtime verification.

## Code readability

- Write code for people who must read and change it. Compilation and passing tests are not enough.
- Prefer clear names and direct control flow over cleverness or fewer lines.
- Explain special values, including Unicode code points and bit masks, where the code uses them.
- For character exceptions, state the character names and why the rule includes or excludes them.
- Use meaningful names for constants and helper functions that clarify a rule. Do not add abstractions that only hide unexplained values.
- Use comments to explain intent, constraints, and exceptions, not to repeat the syntax.
- Read the changed code as a maintainer before you finish. Rewrite code that requires readers to decode literals or reconstruct unexplained rules.

## Secret handling

- Prefer mutable byte buffers for secrets until a required string boundary. Keep secret lifetimes short and document each forced string or interface copy.
- State whether a function borrows secret bytes or takes ownership. A transfer moves cleanup responsibility to the receiver; a borrowed slice is never wiped by the callee.
- Use `internal/secretmem.Wipe` for retired application-owned storage: vacated ranges, discarded events, and fully retired allocations. Wipe exact dead ranges; use the full capacity only when the whole allocation is retired.
- Keep `Wipe` as `clear` followed by `runtime.KeepAlive`. The keep-alive prevents the optimized path from dropping the explicit clear.
- Never use an unsafe byte/string alias as an erasure shortcut. Do not log secrets. Do not claim complete process-memory erasure; document unavoidable runtime, standard-library, provider, HTTP, and terminal-rendering copies instead.
- Preserve symlink-preserving auth writes: the logical `auth.json` path owns the Pi lock, while the resolved regular-file target receives the atomic replacement. `/logout` also holds Pi's resolved-target lease when it differs from the logical lease, for the whole read, revocation request, and write. Recheck the link and target identity before staging and before replacement, and refuse a change observed by those checks; do not claim an atomic guard against a race after the final check.
- Adding a built-in provider or changing its credential kind requires a working `/logout` in the same change: listing, local removal, OAuth revocation where the provider supports it, tests, CLI help, docs, and prompt guidance. An API-key provider cannot revoke the key; `/logout` removes the stored copy and says so.

## Tests

Each behavioral fix must include a focused regression test for the broken behavior.

Never put a performance or timing test in the regular test suite. Gate
performance tests so `go test ./...` does not execute them: a build tag, a
benchmark entry point, or a dedicated package. A wall-clock budget must never
be a routine CI pass/fail condition.

Tests in the default CI suite must:

- Assert important, observable behavior or protect a fixed regression.
- Add unique coverage. Do not duplicate an existing test or test trivial implementation details.
- Be deterministic across supported platforms and under host load. Use explicit events or fakes, not sleeps, retries, or wall-clock timing budgets.
- Have a small, bounded runtime and not materially slow CI.

Run performance, stress, and environment-sensitive checks behind their gate at
a slower cadence. Do not make their timing measurements routine CI pass/fail
conditions. In this repository those profiles run from
`.github/workflows/stress.yml` (weekly schedule and manual dispatch), never
from `.github/workflows/ci.yml`.

## Keep documentation and prompts current

For each tool or capability that you add, change, rename, or remove, complete these steps in the same change:

- Update the affected documentation in `docs/` and the CLI help in both supported languages.
- Update the affected system-prompt guidance in `internal/config/prompt.go` and other prompt sources in both supported languages.
- Describe the actual availability, permissions, limits, and failure behavior.
- If documentation topics change, update the topic list in `docs/docs.go`.
- Update the affected prompt fixtures and regression tests for both supported languages.

Before you report completion, check that the documentation and prompt guidance match the implementation.
If documentation or prompt changes are unnecessary, explain why in the change summary.

## Development procedures

Contributor procedures that are not `psi --docs` topics are indexed in [docs/development/README.md](docs/development/README.md).

## Psionic architecture

- Keep all Psionic-specific Go code, including tests, under `cmd/psionic/`. Do not place Psionic-specific Go files in Psi directories or Psi Go files in the Psionic tree.
- Psi packages must not import Psionic packages. Treat Psionic as a separate codebase: keep adapters in `cmd/psionic/` and do not reorganize or extract Psi code for reuse. A small additive export is allowed only when an essential feature cannot use the existing API.
- Keep Psionic in the same Go module as Psi. Code under `cmd/psionic/` may import Psi's `internal` packages, such as `github.com/hypernewbie/psi/internal/agent`. Place Psionic-private packages under `cmd/psionic/internal/`, where Go restricts imports to the `cmd/psionic/` tree.

## Repository privacy

- Never include or refer to private installation details, configuration values, personal data, or identifiers from a real environment anywhere in the repository. This applies to code, comments, documentation, prompts, tests, fixtures, examples, generated files, and commit messages.
- Never include real local hostnames, IP addresses, network identifiers, private usernames or paths, or other identifying infrastructure details. Use fictional data and placeholders instead.
- Do not name a predecessor or attribute Psionic's design, behavior, or formats to one. Describe Psionic's capabilities on their own terms.

## Localization

Supported languages are English (`en`) and Simplified Chinese (`zh`). Do not add another language, aliases such as `zh-CN`, or automatic locale detection without explicit approval.

- New documentation topics, built-in skills, prompt sections, CLI help entries, settings labels and descriptions, notices, errors, and UI text must include both English and Simplified Chinese versions in the same change.
- Route user-facing UI text through a shared i18n layer with stable keys and `en`/`zh` catalogs. Do not add new hard-coded user-facing strings to individual screens, commands, or prompt builders.
- Keep identifiers stable. Tool names, command names, paths, XML tag names, topic IDs, JSON keys, and code identifiers are not translated.
- Do not translate user-owned content such as repository `AGENTS.md` files, `SYSTEM.md`, custom skills, or imported documentation unless the user explicitly asks.
- Update language-specific fixtures and regression tests for both languages whenever behavior or text differs.
- If a surface is not localized yet, document the gap and do not claim full i18n support. Current prompt-only localization covers built-in prompt text and the skills wrapper; terminal labels, notices, errors, embedded documentation bodies, and other UI strings still need migration.
- When adding a language later, update the supported-language list, the `language` setting validation, CLI help, documentation, prompt fixtures, and tests together.
