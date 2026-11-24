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
  when the project is trusted. If startup skipped the trust prompt because
  there were no protected resources, `/reload` does not activate new project
  MCP entries. Approve the project and restart psi to use them.

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
- `type` is optional. Use `stdio`, `http`, or `streamable-http` and match the
  selected connection fields. `sse` is not supported.
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

- `${NAME}` expands from the psi process environment. An unset variable makes
  resolution fail.
- A whole value that starts with `!` runs a shell command. The resolver uses
  `PSI_SHELL` when set, otherwise Pi's platform shell resolution (Git Bash in
  `ProgramFiles` before `bash.exe` on `PATH` on Windows; `/bin/bash`, `bash` on
  `PATH`, then `sh` on Unix). Commands pass via `-c`, except when the resolved
  Windows shell is the legacy WSL `System32`/`Sysnative` `bash.exe` launcher,
  which receives the command on stdin via `-s`.
  Each command has a 10-second deadline, a 64 KiB stdout limit, and a bounded
  64 KiB retained stderr tail. Trailing CR and LF characters are removed.
  If cancellation occurs before the command starts, the resolver does not run it.
  After shell exit or cancellation, the resolver waits up to one second to
  drain the output pipes. If the pipes stay open, resolution fails. On Unix,
  cancellation stops the shell's process group. On Windows, it stops only
  the shell. Descendants outside the group can outlive cancellation.

The loader rejects invalid HTTP header names and values. The resolver also
rejects header values that are invalid after interpolation.

`command`, `args`, `url`, and other fields are not interpolated. Safe discovery
retries reuse resolved values. Only `/reload` re-runs `!command` and
re-resolves `${NAME}` values.

A project-sourced `!command` is refused unless the user settings file sets:

```json
{"mcp": {"allowProjectCommands": true}}
```

This key is user-only. A project settings file cannot enable it. The default is
`false`. Trust alone does not permit project command interpolation.

## Stdio child environment

A stdio server receives the resolved values configured in its own `env` setting.
psi does not merge its process environment. Configure any values the server needs;
ambient `PATH`, `HOME`, proxy settings, and credentials are not passed through.

On Windows, Go's process launcher also adds psi's `SYSTEMROOT` when the configured
environment omits it. This variable identifies the Windows installation directory.
It is a Windows-specific exception, not a general environment merge. The
`!command` interpolation shell remains separate and keeps its existing parent
environment.

## Tool names and schemas

Tool names are `mcp__<server>__<tool>`. Characters outside `[A-Za-z0-9_]`
become `_`. A name longer than 64 characters, or a sanitized-name collision,
gets `_` plus eight SHA-256 hex characters. Collisions are resolved over the
whole discovered list, so a list reorder does not rename a tool. If tools from
different servers still have the same model name, psi withholds all ambiguous
routes and reports `tool-name-collision` in `/mcp`. Other tools remain usable.
Captured and queued calls cannot use an ambiguous route. Rename a server or
hide a conflicting tool, then reload to restore an unambiguous route.

psi copies the server input schema. It adds `type: "object"` only when the root
type is absent or null, and `properties: {}` only when that key is absent. It
does not repair other schema keywords. Descriptions use the tool description,
then the tool title, then the annotation title, then a generated fallback.

## Results and limits

Result conversion maps MCP text, images, embedded resources, resource links,
errors, and structured output to psi tool results. Supported images are PNG,
JPEG, GIF, and WebP, up to 3 MiB of decoded data. Unsupported or oversized
images become text notices. An error with no text includes a fallback message.
Audio and unknown content become text placeholders. Resource links are text
only; psi does not fetch their URIs. Embedded text, JSON, and XML blobs are
returned as text if they are valid UTF-8. Media-type parameters such as
`charset` do not change how psi classifies these blobs. Embedded binary
resources are written to mode-0600 `psi-mcp-resource-*` files, which are not
removed automatically. The mode applies on Linux and macOS; on Windows the
operating system applies its own file permissions.

A combined text result over 20 KiB keeps its start and end. psi writes the full
text to a `psi-mcp-*` file with the same permissions as binary resource files.
When `read` is available, psi tells the model to use it with offset and limit.
The file stays on disk for later reads and session history. If the write fails,
the result says so and does not show a false path.

Limits:

- Incoming stdio frame: 16 MiB.
- HTTP response body or individual SSE event: 16 MiB. Error responses use the
  body limit regardless of media type. A body at the limit is valid; one byte
  over fails explicitly.
- Complete tool catalog: 16 MiB and at most 128 pages.
- Setup concurrency: four servers.
- Active MCP calls: eight per session, four per server.
- `!command`: 10 seconds, 64 KiB stdout, 64 KiB retained stderr.
- Model-facing text: 20 KiB plus a bounded notice.

HTTP supports only absolute `http` and `https` URLs with a host. If specified,
the port must be between 1 and 65535. User information and fragments are
rejected. Redirects fail, and TLS certificate verification cannot be disabled.
Invalid or duplicate header names and values containing CR, LF, or NUL are
rejected. These headers are reserved: `Host`, `Content-Length`,
`Transfer-Encoding`, `Content-Type`, `Accept`, `Mcp-Session-Id`,
`MCP-Protocol-Version`, `Last-Event-ID`, `Idempotency-Key`, and `X-Idempotency-Key`.

## Manager and routing

The manager connects configured servers and publishes immutable tool snapshots.
A setup failure is isolated to its server; ready tools from other servers stay
available. A call validates its captured route against the current publication
before dispatch and after admission. A route made stale by reload, disablement,
or retirement is rejected rather than sent to an obsolete connection.

Tool names must also be unique across servers. If two servers produce the same
model-facing name, the manager withholds that ambiguous name and marks both
servers with `tool-name-collision`. Their nonconflicting tools remain available.
When a conflicting route is hidden or removed, the name can be published again
on the existing connection. Previously returned snapshots remain unchanged.

Tool-list change notifications trigger coalesced, rate-limited catalog refreshes.
A failed refresh keeps the previous catalog available and records a warning; a
later successful refresh publishes the updated catalog and clears the warning.
Changes to connection identity replace a session. Exposure and call timeout
changes can take effect without replacing the connection. If policy blocks
publication of a new connection, a reload that restores permission retries setup.

The call timeout is an idle limit. A finite progress update for that call
starts a new idle interval only if its value advances. Progress does not
extend the hard deadline, which is ten times the call timeout.

## Startup and status

MCP work never blocks the terminal loop. psi starts servers in background
workers. The first model request can proceed without MCP tools; new tools
appear at a later request boundary. `/mcp` reads a snapshot and shows server
name, transport, state, discovered and exposed counts, and a safe error class.
It reports when a server rejects the standalone listener and cannot push
tool-list updates, plus bounded config warnings. It never shows URLs, headers,
environment values, arguments, or raw stderr.

`--no-mcp` skips MCP config reads, interpolation, process start, network
requests, and tool declarations. `--no-tools` disables the tools listed under
`--tools`. It does not disable MCP.

`/reload` re-reads MCP configuration in the background. A malformed file keeps
the previous accepted config. An invalid entry is withdrawn, not silently run
with its old definition. A failed re-list keeps the last good catalog and
reports a refresh warning. Tool-list notifications received during discovery
remain queued. Refresh attempts are coalesced and rate-limited to one per
second, including failed attempts. A lost connection withdraws its tools and may use at
most two reconnect cycles. A successful `/reload` accepts the config and starts
a fresh reconnect allowance. An exposure-only reload updates declarations and
live routes without reconnecting. Changes to connection fields replace the
connection; `env` and `headers` are distinct even if their keys and values match.
Calls recheck exposure after admission, and
setup that finishes after the reload uses the latest accepted exposure policy.

A queued call that has not reached the server reports caller cancellation,
connection withdrawal, or admission timeout separately. It does not claim an
unknown server outcome. Cancellation releases the admission capacity for that call.

If a server exits before its tool reply is complete, the call produces an error
and warns that the outcome can be unknown. Once the SDK has settled its pending
replies, session shutdown does not discard them. Connection cleanup
does not turn a crash into caller cancellation. Stop still cancels unfinished
calls. A stalled call waits for its timeout or caller cancellation.
The editor remains usable while that turn waits.

## Security

MCP servers are not sandboxed. A stdio server receives only resolved values in
its configured `env` setting; it does not inherit psi's environment. Go may add
`SYSTEMROOT` on Windows when the configured environment omits it. A trusted
project can still start a server that reads any files available to the psi
process. MCP output can contain secrets and instructions; psi treats it as
untrusted data, not instructions.

The separate `!command` interpolation shell keeps its normal parent environment.
Only its resolved output is used as the configured value. This policy
intentionally differs from Pi's inherited stdio environment behavior.

psi does not write an MCP log. It keeps a bounded in-memory stderr tail.
MCP servers do not inherit provider credentials from psi's process environment.
A configured `${NAME}` reference or `!command` can still pass a secret
explicitly. Config redaction is not a data-loss-prevention guarantee.

The stdio launcher can download packages when the configured command does so,
for example `npx -y`. Use a pinned version when reproducibility matters.

## Stdio transport

The stdio transport exchanges newline-delimited JSON-RPC messages over the
child's stdin and stdout. Each incoming frame is limited to 16 MiB. Malformed,
incomplete, and oversized frames fail the connection. A trailing CR or LF is
removed from a frame, and a blank line is skipped. The transport drains
stderr continuously and retains only its last 64 KiB, so stderr output cannot
fill the pipe or grow retained diagnostics without bound.

## Process cleanup

When an HTTP connection is retired, psi cancels ordinary requests and withdraws
its tools without waiting for the server. For a stateful session, psi attempts
`DELETE` in the background with the SDK's five-second request deadline.

On Unix, psi starts a stdio server in its own process group and signals the
owned group. When the launcher exits, psi kills remaining group members,
including when the launcher exits during graceful shutdown. Children that
leave that group are not cleaned up.

A launcher exit alone does not discard fully written stdout frames. A trailing
incomplete frame is rejected. Close and cancellation release blocked transport
reads and writes without waiting for the pipe peer. Close offers stdin EOF if
no write is in progress, then uses platform-specific bounded shutdown.
Cancellation can skip the graceful wait and force-stop the process or owned
group.

On Unix, graceful close waits 500 ms, then sends SIGTERM. If the launcher has
not exited after another 1.5 seconds, psi sends SIGKILL. On Windows, psi waits
500 ms and then stops only the launched process; descendant processes can
outlive cancellation. Prefer a native executable such as `node.exe` with
explicit arguments. On either platform, a surviving descendant that inherited
a pipe can delay the release of that pipe, but it does not prevent psi from
reaping the launcher or closing the connection.

## Known deviations from Pi

- `codemode` tools are declared directly instead of becoming a script tool.
- `deferred` tools and `tool_search` are not available.
- `mcp_servers` prompt sections, resource tools, OAuth, `mcp.log`, and
  `psi mcp` subcommands are not available.
- Startup never waits for servers. Pi waits up to 10 seconds for direct tools.
- Duplicate JSON keys are strict errors instead of last-value-wins.
- psi never replays an interrupted tool call. After an ambiguous side effect,
  inspect the external state.

## Failure regression checks

Unix tests use controlled launcher processes and loopback providers. They do
not run `npx`, download packages, or use real provider credentials.

The default suite covers a launcher that exits before initialization and one
that stalls initialization. Chat and `/mcp` remain usable. Quit stops the
stalled process before the fixture releases its gate. A deterministic SDK
regression also checks that cancellation returns while a handler ignores it,
and that another call on the same connection succeeds. Native Unix subprocess
regressions check that a same-group child cannot survive when its launcher
exits before cleanup or exits after stdin closes. A subprocess regression also
checks that final replies can be read after process exit, followed by EOF or
an error for an incomplete trailing frame.

Fake-time SDK checks cover progress-renewed idle deadlines, the hard deadline,
and admission fairness between servers. Queued-call checks cover cancellation,
caller deadlines, idle deadlines, and connection withdrawal at both admission
stages. Exposure-reload checks require withdrawn tools to send no request,
re-enabled tools to work on the same connection, and pending setup to publish
the latest policy. Cross-server name-collision checks require ambiguous routes
to disappear, captured and queued calls to send no request, healthy tools to
remain usable, and resolved routes to recover without reconnecting. Reload
checks distinguish environment values from HTTP headers. Real HTTP/SSE checks
preserve notifications that arrive before discovery completes. Fake-time SDK
checks cover coalesced notifications after failed refreshes, retained catalogs,
and warning clearance. Result checks preserve embedded-resource omission
notices beside other text and read the complete saved output with `read`.
A gated SDK check retires a connection before callers consume
their completed replies. A separate check requires Stop to release an unfinished
call while its handler ignores cancellation. HTTP checks verify that routes are
withdrawn while a stateful session's `DELETE` is in flight. Invalid SSE streams
fail after a finite allowance. SSE ping replies verify that the size limit
applies per event, not per connection.

The tagged acceptance suite covers a mid-call crash, a handler that ignores
cancellation, and a server that exits immediately after its complete reply.
It checks cancellation, continued chat, durable error or success results,
no replay during a gated reconnect, and process cleanup. This check polls
`/mcp` for initial catalog readiness. It runs in `stress.yml`, not routine CI.

Run both checks against a built executable:

```sh
mkdir -p .mcp-work
go build -o .mcp-work/psi .
go test ./internal/app \
  -run '^TestActualAppMCPLauncherFailureDoesNotBlockChat$' -count=1 \
  -args -psi-test-binary="$PWD/.mcp-work/psi"
go test -tags acceptance ./internal/app \
  -run '^TestActualAppMCPCallFailureKeepsSessionUsable$' -count=1 \
  -args -psi-test-binary="$PWD/.mcp-work/psi"
```
