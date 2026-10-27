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
`SYSTEM.md`/`APPEND_SYSTEM.md`. Context files such as `AGENTS.md` load separately,
as in Pi. User instructions come from the agent directory and ancestor folders.

| Area | Keys | Notes |
|---|---|---|
| Provider/model | `defaultProvider`, `defaultModel` | `anthropic` only |
| Reasoning | `defaultThinkingLevel` | off/minimal/low/medium/high/xhigh/max |
| Sessions | `sessionDir` | CLI > env (`PI_CODING_AGENT_SESSION_DIR`) > settings > default |
| Tools | `defaultTools` | four tools; plain replaces, `+`/`-` modify |
| Skills | `skills`, `enableSkillCommands` | paths + selector |
| Compaction | `compaction.enabled,reserveTokens,keepRecentTokens` | bounded summaries |
| Shell | `shellPath`, `shellCommandPrefix` | consent for project executables |
| Network | `httpIdleTimeoutMs`, `retry.enabled`, `retry.maxRetries` | default idle timeout 300000 ms; 0 disables it; at most 10 retries |
| Instructions | user/project files | see below |
| Models | `providers.anthropic.baseUrl`, `models[]`, `modelOverrides` | local metadata only; no catalog refresh |
| Auth | `auth.json` entries, env refs | no `!command` execution |
| Startup | `quietStartup` | inline only |
| Terminal | always inline | fullscreen and rich-display preferences are ignored |

Credential references support `$VAR`, `${VAR}`, `$$`, and `$!` escapes.
Psi rejects `!command` values without running them. Stored Anthropic credentials
precede `models.json` credentials, then `ANTHROPIC_API_KEY`. An OAuth entry stops
API-key selection instead of changing the billing path silently.

The default model is `claude-sonnet-4-5`. Configure other compatible models in
Pi's normal shape:

```json
{
  "providers": {
    "anthropic": {
      "baseUrl": "https://api.anthropic.com/v1",
      "models": [
        {
          "id": "your-model-id",
          "api": "anthropic-messages",
          "contextWindow": 200000,
          "maxTokens": 16384,
          "reasoning": true,
          "input": ["text", "image"]
        }
      ]
    }
  }
}
```

A configured thinking budget must fit below the model's output limit.
Skill paths resolve from their source configuration directory. User and project
skill lists combine. An empty `defaultTools` list disables tools. Project `+`/`-`
tool entries modify the inherited user selection.
