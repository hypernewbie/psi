# CLI and configuration

CLI (exact spelling, no hidden flags):

```text
psi
psi --continue
psi --resume
psi --session PATH
psi --provider PROVIDER --model MODEL
psi --thinking LEVEL
psi --session-dir PATH
psi --tools read,bash,edit,write
psi --no-tools
psi --no-session
psi --help
psi --version
```

Project consent: `--allow-project` / `--deny-project`. Otherwise psi asks once
per run before loading `.pi/settings.json`, project skills, and project
instruction files.

| Area | Keys | Notes |
|---|---|---|
| Provider/model | `defaultProvider`, `defaultModel` | `anthropic` only |
| Reasoning | `defaultThinkingLevel` | off/minimal/low/medium/high/xhigh/max |
| Sessions | `sessionDir` | CLI > env (`PI_CODING_AGENT_SESSION_DIR`) > settings > default |
| Tools | `defaultTools` | four tools; plain replaces, `+`/`-` modify |
| Skills | `skills`, `enableSkillCommands` | paths + selector |
| Compaction | `compaction.enabled,reserveTokens,keepRecentTokens` | bounded summaries |
| Shell | `shellPath`, `shellCommandPrefix` | consent for project executables |
| Network | idle timeout, retries | one HTTP transport, bounded |
| Instructions | user/project files | see below |
| Models | `models.json` endpoint subset | no catalog refresh |
| Auth | `auth.json` entries, env refs | no `!command` execution |
| Startup | `quietStartup` | inline only |
| Terminal | always inline | fullscreen prefs ignored with one notice |

Env credential refs: `$VAR`, `${VAR}`, `$$` escapes. `!command` is rejected,
never executed. Secrets are redacted in errors.
