# MCP configuration

This layer adds MCP configuration parsing and value resolution. The application
is not yet wired to load these files or connect to servers. This document does
not describe available MCP tools.

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

The loader rejects invalid HTTP header names and values. The resolver also
rejects header values that are invalid after interpolation.

Other fields are not interpolated. A project-sourced `!command` is refused
unless the user settings file sets `mcp.allowProjectCommands` to `true`. The
default is `false`, and a project settings file cannot enable this option.
Trust alone does not permit project command interpolation.

The configuration API does not connect to MCP servers in this layer. Application
integration, server lifecycle, and tool availability are added in later layers.
