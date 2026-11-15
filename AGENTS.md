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

- Update the affected documentation in `docs/` and the CLI help in both supported languages.
- Update the affected system-prompt guidance in `internal/config/prompt.go` and other prompt sources in both supported languages.
- Describe the actual availability, permissions, limits, and failure behavior.
- If documentation topics change, update the topic list in `docs/docs.go`.
- Update the affected prompt fixtures and regression tests for both supported languages.

Before you report completion, check that the documentation and prompt guidance match the implementation.
If documentation or prompt changes are unnecessary, explain why in the change summary.

## Localization

Supported languages are English (`en`) and Simplified Chinese (`zh`). Do not add another language, aliases such as `zh-CN`, or automatic locale detection without explicit approval.

- New documentation topics, built-in skills, prompt sections, CLI help entries, settings labels and descriptions, notices, errors, and UI text must include both English and Simplified Chinese versions in the same change.
- Route user-facing UI text through a shared i18n layer with stable keys and `en`/`zh` catalogs. Do not add new hard-coded user-facing strings to individual screens, commands, or prompt builders.
- Keep identifiers stable. Tool names, command names, paths, XML tag names, topic IDs, JSON keys, and code identifiers are not translated.
- Do not translate user-owned content such as repository `AGENTS.md` files, `SYSTEM.md`, custom skills, or imported documentation unless the user explicitly asks.
- Update language-specific fixtures and regression tests for both languages whenever behavior or text differs.
- If a surface is not localized yet, document the gap and do not claim full i18n support. Current prompt-only localization covers built-in prompt text and the skills wrapper; terminal labels, notices, errors, embedded documentation bodies, and other UI strings still need migration.
- When adding a language later, update the supported-language list, the `language` setting validation, CLI help, documentation, prompt fixtures, and tests together.
