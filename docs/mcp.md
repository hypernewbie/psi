# MCP configuration and data handling

This layer adds MCP configuration parsing and value resolution, tool-catalog
mapping, and result conversion. The application is not yet wired to load these
files or connect to servers. This document describes data contracts; it does not
claim that MCP tools are available.

## Configuration files

The loader accepts two JSON files:

- A user file at `<agentDir>/mcp.json`. The agent directory is normally
  `~/.pi/agent`.
- A project file at `<sessionCwd>/.pi/mcp.json`. The loader reads this file
  only when its caller marks the project as trusted.

The root must be an object. A missing `mcpServers` key means no servers. If
present, `mcpServers` must be a non-null object. Missing files mean no entries.
Each file is limited to 1 MiB and may start with a UTF-8 BOM. Duplicate root
keys or server names reject the file. A duplicate key inside an entry rejects
that entry; other valid entries can still load.

## Server entries

Each key under `mcpServers` is a server name. Names must match
`^[A-Za-z0-9_-]+$`. Names that differ only by `-` and `_` share a namespace;
the later conflicting entry is rejected.

An entry must define either `command` or `url`, but not both:

- `command` selects stdio. `args`, `cwd`, and `env` configure the command.
- `url` selects streamable HTTP. Use an absolute `http` or `https` URL with a
  hostname. If you specify a port number, use 1 through 65535. Do not include
  user information or a fragment. `headers` configure HTTP requests.
- `type` is optional. It accepts `stdio`, `http`, or `streamable-http` and
  must agree with the selected connection fields. Legacy `sse` is rejected.
- `enabled` defaults to `true`.
- `timeout` is a positive number of seconds. The default is 60.
- `exposure` defaults to `codemode` and accepts `direct`, `codemode`,
  `deferred`, or `hidden`.
- `toolExposure` maps raw tool names to exposure values.
- `oauth` and `auth` are not supported. `autoEnableCodemode` is accepted for
  compatibility and ignored.

This layer validates and records these values. It does not start a command,
make an HTTP request, or declare a tool.

## Project overrides

A full project entry replaces the user entry with the same name. It does not
merge connection fields. A partial project entry without `command`, `url`, or
`type` may set only `enabled`, `exposure`, and `toolExposure`. A partial entry
without a user entry to override is ignored with a warning.

## Value resolution

The resolver processes values in `env` and `headers`:

- `${NAME}` expands from the psi process environment. An unset variable makes
  resolution fail.
- A whole value that starts with `!` runs a shell command. The resolver uses
  `PSI_SHELL`, or `bash` when it is unset or blank, and passes the command to `-c`.
  Each command has a 10-second deadline, a 64 KiB stdout limit, and a bounded
  64 KiB retained stderr tail. Trailing CR and LF characters are removed.
  If cancellation occurs before the command starts, the resolver does not run it.
  After shell exit or cancellation, the resolver waits up to one second to
  drain the output pipes. If the pipes stay open, resolution fails. On Unix,
  cancellation stops the shell's process group. On Windows, it stops only
  the shell. Descendants outside the group can outlive cancellation.

The loader rejects invalid HTTP header names and values. The resolver also
rejects header values that are invalid after interpolation.

Other fields are not interpolated. A project-sourced `!command` is refused
unless the user settings file sets `mcp.allowProjectCommands` to `true`. The
default is `false`, and a project settings file cannot enable this option.
Trust alone does not permit project command interpolation.

## Tool names and schemas

The catalog maps a raw tool name to `mcp__<server>__<tool>`. Characters outside
`[A-Za-z0-9_]` become `_`. Names longer than 64 characters, or names that
collide after sanitizing, receive `_` plus eight lowercase hexadecimal digits
derived from SHA-256. The catalog assigns names across the full discovered list
before it filters by exposure, so reordering the same list does not change a
tool name. A remaining within-server name collision rejects the catalog.

The input schema must be a JSON object. psi deep-copies it, adds
`type: "object"` only when `type` is absent or null, and adds
`properties: {}` only when `properties` is absent. It does not repair other
schema keywords. The description uses the tool description, then the tool title,
then the annotation title, then a generated fallback.

An exact `toolExposure` name takes precedence over wildcard patterns. Otherwise,
the first matching wildcard in JSON order wins. Only `*` is special. A tool
without a matching rule uses the server's `exposure`. The catalog marks
`direct` and `codemode` tools as declared; it filters `deferred` and `hidden`
tools out.

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

Combined text over 20 KiB keeps its beginning and end. psi writes the full text
to a mode-0600 `psi-mcp-*` file. When the read tool is available, the result
notice directs the model to read the file with an offset and limit. If the write
fails, the notice reports that the complete output was not saved and does not
show a false path. A regression test reads the saved output through the actual
`read` tool.

The configuration API and catalog/result conversion do not connect to MCP
servers in this layer. Application integration, server lifecycle, and tool
availability are added in later layers.
