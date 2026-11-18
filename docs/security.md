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
them. Codex and OpenAI OAuth tokens refresh in place with a Pi-compatible
directory lock. OpenAI refresh can update the shared `auth.json` through that
lock and atomic replacement. ChatGPT sign-in stores a stable installation UUID
as `deviceId` in the user `settings.json`; it identifies the installation to
OpenAI and is not a token. The authorization link, codes, and tokens never
enter session files or chat; only the harmless `/login openai` command itself
can remain in editor recall.

Interactive API-key login masks the key and excludes it from conversation history
and prompt recall. It does not check the key with the provider. Keys remain
unencrypted in `auth.json`. New auth files use mode `0600`, and new parent
directories use mode `0700` on Unix. API-key login and `/logout` preserve an existing
`auth.json` symlink. They resolve the link under the shared Pi lock, atomically
replace its regular-file target, and preserve the link text, target mode,
unrelated entries, and unknown fields. Broken, cyclic, and nonregular targets
fail closed. psi rechecks the logical target and file identity before staging
and immediately before replacement. It refuses a change observed by those
checks; a retarget after the final check remains a filesystem race and is not
made atomic. Codex OAuth refresh uses the same logical-lock and resolved-target
rule. `/logout` removes only the selected stored API key, not OAuth credentials,
environment variables, or `models.json` credentials. OpenAI ChatGPT sign-in
and refresh require a regular `auth.json` and refuse symlink writes.

Windows identity checks use an open handle to capture the file ID before a
replacement can affect a later comparison. The file must allow read access.
For a new file, its parent directory must allow read access instead.
If inspection fails, psi aborts the auth operation.

Interactive key entry and selected auth writes keep secret data in mutable
bytes where practical. Secret cursor navigation follows the normal editor's
Unicode line-break and cursor-ownership rules without rendering plaintext.
Only mask characters enter the secret display path. psi wipes retired
application-owned buffers with an explicit clear followed by
`runtime.KeepAlive`, including vacated ranges and fully retired allocations.
These wipes resist removal of the explicit clear in
the optimized path. They do not provide complete process-memory erasure. Go
runtime, standard-library JSON, HTTP, provider credential strings, terminal
mask rendering, and other required boundaries can retain copies outside psi's
control. The system does not lock memory or protect against a hostile local
process inspecting the process.

Session files record prompts, tool calls, tool output, and file contents shown
to the model. Treat a session file as sensitive as the work it describes.
On Unix, new session directories and missing parents use mode `0700`. This
includes the agent directory when startup creates it. Existing directory
modes stay unchanged.

## What leaves the process

Model requests contain the system prompt, the active branch, and the tool
definitions. Codex OAuth refresh, OpenAI code exchange, and OpenAI OAuth
refresh contact their providers. The web tools contact the configured SearXNG
or Exa endpoint and fetch requested pages directly. See
[Web access](web.md#network-guard) for address checks. Commands run by the
`bash` tool can make their own network connections. The
native Phi bridge is a local Unix socket that publishes lifecycle and state.
Transcript-search requests can return excerpts of visible conversation text to
Phi. In RPC mode, prompts, responses, and tool results are also written to stdout.
Treat the controlling process as trusted.

There is no telemetry, no automatic upload, and no hosted service.
