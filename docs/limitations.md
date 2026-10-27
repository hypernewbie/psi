# Limitations

No extensions, JS runtime, npm packages, MCP, codemode, tool search, subagents,
public SDK, RPC, JSON event mode, print mode, fullscreen, widgets, Pi TUI API,
session tree/branch/fork/clone UI, HTML export, sharing, bug uploads, hosted
services, update checks, self-update, catalog refresh, telemetry, browser
automation, cloud credential discovery, extra providers, cache warming,
background jobs, virtual models, routing, classifier/image/audio/video models,
clipboard, image paste, external editor, prompt templates, sandbox, per-tool
approval, permission-policy language, automatic Git ops, or visual Pi clone.

Tools run with the current OS user permissions. Project consent and workspace
paths do not provide isolation.

Additional boundaries:

- Authentication supports Anthropic API keys, not subscription OAuth refresh.
- Reasoning uses budget-based Anthropic thinking. Adaptive-only model protocols
  are not implemented. Unknown model IDs require explicit local metadata.
- Compaction uses an actual model-generated summary and safe user-message
  boundaries. A single oversized user span can fail without a safe boundary.
  Context estimates are approximate. There is no automatic overflow retry.
- New image reads support bounded JPEG, PNG, GIF, and WebP payloads, up to 3 MiB
  before base64 encoding. There is no automatic image resize or BMP conversion.
- Session files must be regular files of at most 256 MiB. Each JSONL record has
  a 32 MiB limit. Complete final records without a newline remain appendable.
- Shell output uses bounded memory and a private full-output file. The command
  stops if its captured output exceeds 256 MiB.
- Terminal input is limited to 256 KiB per prompt. Only cancellation is accepted
  during a run. The terminal supports bracketed paste and a fixed keyboard map.
  Unicode text is preserved. Cursor width for complex joined emoji remains approximate.
- Unsupported model, credential, role, or content requirements produce an error.
  Psi does not silently use a fake provider or discard unknown active context.
