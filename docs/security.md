# Security

psi runs on your machine with your permissions. Read this before pointing it at
files, repositories, or automation you do not control.

## Project trust

Before psi reads project-local configuration or resources it asks whether you
trust the working folder. The prompt lists what it would load.

Trust gates:

- `.pi/settings.json`
- `.pi/skills` and `.pi/SYSTEM.md` / `.pi/APPEND_SYSTEM.md`
- `.pi/themes` and project theme paths
- `.pi/packages`, if configured

`--approve` (`-a`) trusts the folder for one process. `--no-approve` (`-na`) or
`--deny-project` denies it. `Escape` and `Ctrl+C` at the prompt also deny. The
answer applies to that run only; the next start asks again.

Context files such as `AGENTS.md`, `AGENTS.override.md`, and `CLAUDE.md` load
without the trust prompt, matching Pi. Treat a repository's instruction file as
untrusted input: it becomes part of the system prompt.

## No sandbox

Trust controls which resources load. It does not limit what a tool can do. The
`bash`, `write`, and `edit` tools run with the permissions of the psi process:

- `bash` can read, modify, or delete anything your user can.
- `write` and `edit` can change any path the process can reach.
- Network access depends on the process environment, not on psi.

There is no per-tool approval, no permission-policy language, and no automatic
Git checkpoint. Use a container, a dedicated user, or a throwaway checkout for
untrusted or unattended work.

## Shell and environment

The `bash` tool inherits the psi process environment, including provider API
keys. A command that prints its environment exposes them to the transcript and,
through it, to the provider.

The `read` tool can read supported image files. It cannot read beyond the
process permissions.

## Credentials

Credentials live in `<agentDir>/auth.json` and `models.json`. psi never prints
them in `/session`, and refuses `!command` credential values without running
them. Codex OAuth tokens are refreshed in place with a Pi-compatible directory
lock.

Session files record prompts, tool calls, tool output, and file contents shown
to the model. Treat a session file as sensitive as the work it describes.

## What leaves the process

Model requests contain the system prompt, the active branch, and the tool
definitions. Codex OAuth token refresh is the other built-in outbound request.
Commands run by the `bash` tool can make their own network connections. The
native Phi bridge is a local Unix socket that publishes lifecycle and state.
Transcript-search requests can return excerpts of visible conversation text to
Phi. In RPC mode, prompts, responses, and tool results are also written to stdout.
Treat the controlling process as trusted.

There is no telemetry, no automatic upload, and no hosted service.
