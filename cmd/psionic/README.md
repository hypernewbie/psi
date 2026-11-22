# Psionic

Psionic is a Discord front end for Psi. One process runs one bot and one selected
agent. It reads an existing settings file and workspace in place, and uses
Psi's agent loop, sessions, model catalogue, credentials, compaction, and
skills.

## Build and run

```sh
go install ./cmd/psionic
psionic --config ./settings.json5 --check
psionic --config ./settings.json5
```

`--check` reads the settings and model metadata, resolves the bot token, and
prints a privacy-safe summary without configured IDs or values. It does not
connect, register commands, write state, or create a directory. An `exec` token
provider is run during resolution;
use only a trusted command with known side effects.

Flags:

| Flag | Meaning |
| --- | --- |
| `--config PATH` | Required settings file. |
| `--agent ID` | Select one configured agent entry. Required when more than one entry exists. |
| `--workspace DIR` | Override the selected agent's workspace for this process. |
| `--state-dir DIR` | Psionic's private state directory. Defaults to `psionic` under the OS user configuration directory. |
| `--agent-dir DIR` | Psi agent directory for `settings.json`, `models.json`, and `auth.json`. Defaults to Psi's normal agent directory. |
| `--secret-store-db PATH` | Existing SQLite database for `store` token references. Opened read-only; overrides automatic lookup beside the settings file. |
| `--check` | Validate configuration and resolve credentials without connecting to Discord. It can run a configured `exec` provider. |
| `--version` | Print the version. |

## Supported settings

Psionic reads the JSON5 settings file directly. Unknown optional features are
ignored. A known policy it cannot honor blocks startup instead of widening
access.

| Area | Supported |
| --- | --- |
| Agent | `agents.defaults` and one `agents.entries` profile: workspace, identity, and `tools` filters. |
| Model | Psi's `settings.json` selects the model; Psi's catalogue and credentials configure its provider. Model settings in the input file are ignored. |
| Discord | `channels.discord`: token, DM policy, allow lists, group policy, guild and channel entries, sender and role lists, mention overrides, history limit, text chunk limit, line limit, reply mode, embed suppression, inbound run timeout. |
| Replies | `messages.visibleReplies` and `messages.groupChat.visibleReplies`, including the older boolean form. |
| Sessions | `session.dmScope`, `session.reset`, `session.resetByType`, and `session.resetByChannel` for Discord. |
| Commands | `commands.ownerAllowFrom` for owner-only controls. |

`channels.discord.token` accepts a literal or an environment, file, exec, or
store reference. `DISCORD_BOT_TOKEN` is used only when that field is absent; an
invalid explicit reference does not fall back. Environment references enforce
configured provider aliases and allowlists. Whole-value `$NAME` and `${NAME}` shorthand uses the same
checks; embedded `${NAME}` templates use configuration substitution, while
embedded `$NAME` is literal. Provider aliases start with a lowercase letter
and contain up to 64 lowercase letters, digits, `_` or `-`. Environment and
store IDs start with an uppercase letter and contain up to 128 uppercase
letters, digits or `_`.

File references use a `source: "file"` provider, JSON-pointer selection or
`singleValue` mode, and a private unlinked regular file. Reads use the configured
byte bound and timeout; unsupported ACL verification fails closed. Exec
references use a manually configured absolute command, isolated environment,
JSON request/response protocol, bounded output and timeouts. The executable
must pass platform trust checks; timeout or invalid output blocks startup.
Plugin-managed exec providers are not supported. Store references read only a
live team-scoped value from `--secret-store-db` or `PSIONIC_SECRET_STORE_DB`.
Without either, the CLI looks only in a `state/` directory beside the supplied
settings file and uses a database only when exactly one private SQLite file
contains the secret-store table. Reads are read-only. Missing, ambiguous, or
unavailable databases and missing values block startup.

`channels.discord.dmPolicy: "pairing"` keeps static `allowFrom` entries
working, but Psionic does not implement the pairing workflow. Unknown senders
stay denied. An `open` policy requires `allowFrom: ["*"]`, matching the
existing schema.
Multiple Discord accounts and `$include` are not supported. A
`session.dmScope` of `"main"` is accepted only when the single allowed
direct-message peer is also listed in `commands.ownerAllowFrom`; otherwise
Psionic requires peer isolation.
The settings file is read once at startup; restart after changing it. Workspace
prompt files are read on every turn.

## Discord behavior

The bot needs View Channels, Send Messages, Read Message History, and the
Message Content intent. Enable the intent in the Discord developer portal. The
invite must include the `applications.commands` scope for native commands.

- One bot. Direct messages, guild text channels, and existing threads. Group
  DMs, edits, and attachments are not processed; an attachment-only message
  gets a short notice.
- Guild messages require a mention by default. Guild and channel entries can
  change that.
- Guild allowlist semantics: a guild must have an entry unless the group policy
  is `open`; when a guild lists channels, an unlisted channel is denied.
- A sender qualifies when their user ID **or** one of their role IDs is listed
  at the guild or channel scope.
- Bots, webhooks, self-authored messages, and edited messages are ignored.
- Replies use the configured text and line limits and are capped at 16 messages.
  Truncated replies include a notice; the notice can exceed a very small
  configured text limit. Code fences are balanced across chunks only when the
  configured limits leave room; otherwise the original fence text is kept
  without adding over-limit markers. Link and role pings are suppressed.
- History is read on demand in 100-message pages, up to the configured limit
  (capped at 250 messages).
- `message(action="read")` reads only the current conversation, with either a
  bounded recent window or one checked message ID.
- `message(action="send")` is available only when `visibleReplies` is
  `message_tool`; the final assistant text stays private in that mode.
- Commands: `/new`, `/reset`, `/stop`, `/status`. They are registered as global
  Discord application commands at startup and also accepted as ordinary
  messages. They require an owner ID from `commands.ownerAllowFrom`. Set
  `commands.native` to `false` to skip registration.

## Prompt and memory

The prompt loads `AGENTS.md`, `SOUL.md`, `IDENTITY.md`, `TOOLS.md`, `USER.md`,
and `MEMORY.md` in a fixed order. `USER.md` and `MEMORY.md` are loaded only for
an owner direct message. Shared rooms get the shareable files. An optional
`SYSTEM.md` replaces the base preamble, and `APPEND_SYSTEM.md` adds a final
section; both are read on every turn, so edits apply to the next turn.

`memory_search` and `memory_get` index `MEMORY.md`, `USER.md`, and Markdown
files under `memory/`. Matching is case-insensitive and requires every query
term. It is not semantic search and does not read transcripts.

Workspace `skills/*/SKILL.md` files are advertised by name and description when
the route permits `skill`. The tool loads one skill's instructions on demand.
Skill instructions do not grant permissions.

## Tools

The model gets only the tools for the current route: `memory_search` and
`memory_get` for owner direct messages, `message` when the tool profile or
allow lists permit it, and `skill` when workspace skills exist. `tools.profile`,
`allow`, `alsoAllow`, and `deny` filter this implemented set; an unavailable
tool is never added. The `coding` profile excludes `message`, and the
`messaging` profile excludes memory.

There is no shell, write, edit, or general file-read tool. A workspace path is
not a sandbox. Run the bot under an account that cannot read credentials or
other private data it should not expose.

## State and transcripts

Psionic keeps JSONL transcripts under `<state>/sessions/` and a small bbolt
database at `<state>/state.db`.
Transcripts start fresh. They are never read from, written to, or discovered by
another Psi or Pi installation. `--state-dir` is rejected if it overlaps the
workspace or a known Psi/Pi session directory. A non-empty state directory must
carry Psionic's marker file; an unrelated directory is refused.

Each direct-message peer, guild channel, or thread has its own conversation,
queue, and active run, subject to `session.dmScope`. A duplicate Discord event
is admitted once inside a 24-hour deduplication window. An interrupted run is
not replayed automatically.

## Model credentials

Psi resolves model metadata and API keys. Put the model in Psi's `models.json`
when the bundled catalogue does not contain it, and provision credentials in
`auth.json` or the provider's environment variable. Psionic reads the same Psi
agent directory. Model and provider settings from the input file are ignored.
OAuth-only provider modes are not supported in this release; configure an API-key endpoint
instead.

## Security

The security boundary is who can talk to the bot, not the model's tool list.
Only allowlisted people reach an agent run. Owner-only controls and private
memory require `commands.ownerAllowFrom`. Do not publish a bot token in a
workspace file or a settings example. Token settings and JSON-backed sources
pass through immutable Go strings; decoders, the runtime, Discord's SDK, and the
HTTP stack can create copies that cannot be reliably wiped. Exec-provider
arguments and environment values also pass into a child process and OS-owned
copies. Psionic wipes owned file, exec, and store byte buffers where practical,
but does not claim complete process-memory erasure.

## Canary and cutover

Use a separate Discord application and token, a synthetic workspace, a
separate state directory, and a disposable or inexpensive provider before
touching the intended bot. Exercise a direct message, a mention-gated room, an
existing thread, history reading, a prompt edit, reset, explicit delivery,
cancellation, and restart. Never run two processes with the same bot token,
and do not change the incumbent service or its state during the canary.

Cut over by stopping the service that uses the intended token, confirming it
has disconnected, and then starting Psionic with a fresh state directory. Keep
the same application identity and workspace. The first conversation in each
route starts a new transcript; no pending work or old transcript is imported.

## Rollback

Stop Psionic, keep its state directory for diagnosis, and start the service it
replaced. Do not delete or rewrite either service's state. If the cutover
changed command registrations, restore only those known commands.
