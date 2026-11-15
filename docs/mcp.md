# MCP servers

psi can use tools from Model Context Protocol (MCP) servers over stdio or
streamable HTTP. This is a tool-call client, not a full MCP host. It does not
run MCP Apps, JavaScript, codemode, prompts, resources, sampling, elicitation,
roots, tasks, or OAuth.

## Configure servers

psi reads two files:

- `<agentDir>/mcp.json` for user servers. `agentDir` is normally
  `~/.pi/agent`.
- `<sessionCwd>/.pi/mcp.json` for project servers. psi reads this file only
  when the project is trusted.

Both files use the same `mcpServers` shape:

```json
{
  "mcpServers": {
    "chrome-devtools": {
      "command": "npx",
      "args": ["-y", "chrome-devtools-mcp@1.13.0"],
      "env": {"CI": "1"},
      "timeout": 120,
      "exposure": "hidden",
      "toolExposure": {
        "navigate_page": "direct",
        "take_snapshot": "direct",
        "take_screenshot": "direct"
      }
    }
  }
}
```

Root rules:

- The root must be a JSON object.
- An absent `mcpServers` means no servers. A present `mcpServers` must be a
  non-null object.
- Duplicate root keys or duplicate server names reject the whole file.
- A duplicate key inside one entry rejects only that entry.
- Missing files mean no servers. psi reads at most 1 MiB per file and accepts
  a UTF-8 BOM.

Entry rules:

- `command` selects stdio. `args`, `cwd`, and `env` apply to stdio.
- `url` selects streamable HTTP. `headers` apply to HTTP.
- `type` is optional. Use `stdio`, `http`, or `streamable-http`. `sse` is not
  supported.
- `enabled` defaults to `true`. A disabled entry stays visible in `/mcp` but
  psi never interpolates or starts it.
- `timeout` is a positive number of seconds per request. The default is 60.
  Numeric progress from the server resets the idle timeout. A hard limit of
  ten times `timeout` always applies.
- `exposure` defaults to `codemode` and accepts `direct`, `codemode`,
  `deferred`, or `hidden`. In this MVP, `codemode` tools are declared directly.
  `deferred` and `hidden` are never declared or executable.
- `toolExposure` maps raw tool names to exposure values. An exact name wins.
  Otherwise the first matching wildcard in JSON order wins. Only `*` is
  special.
- Server names must match `^[A-Za-z0-9_-]+$`. Names that differ only in `-` and
  `_` share a tool namespace; the later conflicting name is rejected.
- `oauth`, `auth`, and legacy SSE are unsupported. `autoEnableCodemode` is
  accepted for compatibility but ignored.

Project override rules:

- A full project entry replaces the user entry with the same name. It does not
  merge credentials.
- A partial project entry without `command`, `url`, or `type` may set only
  `enabled`, `exposure`, and `toolExposure`. Other fields reject that override.

## Interpolate secrets

`env` and `headers` values support two forms:

- `${NAME}` expands from the psi process environment. A missing variable makes
  that server unavailable. psi never sends an empty value or a literal
  placeholder.
- A whole value that starts with `!` runs a shell command once per setup.
  psi uses `PSI_SHELL` or `bash` with `-c`, a 10-second deadline, and bounded
  output. Trailing CR/LF is removed.

`command`, `args`, `url`, and other fields are not interpolated. Safe discovery
retries reuse resolved values. Only `/reload` runs `!command` again.

A project-sourced `!command` is refused unless the user settings file sets:

```json
{"mcp": {"allowProjectCommands": true}}
```

This key is user-only. A project settings file cannot enable it. The default is
`false`. Trust alone does not permit project command interpolation.

## Tool names and schemas

Tool names are `mcp__<server>__<tool>`. Characters outside `[A-Za-z0-9_]`
become `_`. A name longer than 64 characters, or a sanitized-name collision,
gets `_` plus eight SHA-256 hex characters. Collisions are resolved over the
whole discovered list, so a list reorder does not rename a tool.

psi copies the server input schema. It adds `type: "object"` only when the root
type is absent or null, and `properties: {}` only when that key is absent. It
does not repair other schema keywords. Descriptions use the tool description,
then the tool title, then the annotation title, then a generated fallback.

## Results and limits

psi maps text, images, embedded resources, errors, and structured output to its
normal tool result. It preserves supported images (`image/png`,
`image/jpeg`, `image/gif`, `image/webp`) up to 3 MiB of decoded bytes. Other
images become bounded text notices.

A combined text result over 20 KiB keeps its start and end. psi writes the full
text to a mode-0600 `psi-mcp-*` file and tells the model to use `read` with
offset and limit. The file stays on disk for later reads and session history.
If the write fails, the result says so and does not show a false path.

Resource links are text only. psi never fetches a URI or renders MCP Apps
content. Audio and unsupported blocks become text placeholders.

Limits:

- Incoming stdio frame: 16 MiB.
- HTTP body and SSE event: 16 MiB. A non-SSE body exactly at the cap is valid;
  one byte over fails explicitly.
- Complete tool catalog: 16 MiB and at most 128 pages.
- Setup concurrency: four servers.
- Active MCP calls: eight per session, four per server.
- `!command`: 10 seconds, 64 KiB stdout, 64 KiB retained stderr.
- Model-facing text: 20 KiB plus a bounded notice.

HTTP supports only `http` and `https` URLs with a host. User information and
fragments are rejected. Redirects fail. TLS certificate verification cannot be
disabled. HTTP headers `Host`, `Content-Length`, `Transfer-Encoding`,
`Content-Type`, `Accept`, `Mcp-Session-Id`, `MCP-Protocol-Version`,
`Last-Event-ID`, `Idempotency-Key`, and `X-Idempotency-Key` are reserved.

## Startup and status

MCP work never blocks the terminal loop. psi starts servers in background
workers. The first model request can proceed without MCP tools; new tools
appear at a later request boundary. `/mcp` reads a snapshot and shows server
name, transport, state, discovered and exposed counts, and a safe error class.
It never shows URLs, headers, environment values, arguments, or raw stderr.

`--no-mcp` skips MCP config reads, interpolation, process start, network
requests, and tool declarations. `--no-tools` disables only the built-in
`read`, `bash`, `edit`, and `write` tools. It does not disable MCP.

`/reload` re-reads MCP configuration in the background. A malformed file keeps
the previous accepted config. An invalid entry is withdrawn, not silently run
with its old definition. A failed re-list keeps the last good catalog and
reports a refresh warning. A lost connection withdraws its tools and may use at
most two reconnect cycles. `/reload` can retry after network recovery.

## Security

MCP servers are not sandboxed. A stdio server inherits the psi process
environment, then applies configured `env` values. A trusted project can start
a server that reads anything the user can read. MCP output can contain secrets
and instructions; psi treats it as untrusted data, not instructions.

psi does not write an MCP log. It keeps a bounded in-memory stderr tail. It does
not forward provider credentials to MCP servers. Config redaction is not a
data-loss-prevention guarantee.

The stdio launcher can download packages when the configured command does so,
for example `npx -y`. Use a pinned version when reproducibility matters.

## Process cleanup

On Unix, psi starts a stdio server in its own process group and signals the
owned group. Children that leave that group are not cleaned up.

On Windows, psi stops only the owned leader process. Descendant processes can
outlive cancellation. Prefer a native executable such as `node.exe` with
explicit arguments.

## Known deviations from Pi

- `codemode` tools are declared directly instead of becoming a script tool.
- `deferred` tools and `tool_search` are not available.
- `mcp_servers` prompt sections, resource tools, OAuth, `mcp.log`, and
  `psi mcp` subcommands are not available.
- Startup never waits for servers. Pi waits up to 10 seconds for direct tools.
- Duplicate JSON keys are strict errors instead of last-value-wins.
- psi never replays an interrupted tool call. After an ambiguous side effect,
  inspect the external state.
