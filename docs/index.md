# psi

psi is a minimal AI coding agent for the terminal, written in Go. Give it a
goal and a working folder, and it can read files, run shell commands, edit
content, and work through multi-step tasks.

psi speaks Pi's session and settings formats, uses Pi's model catalog, and
renders an inline terminal interface. It is not a Pi extension host: there is
no TypeScript runtime, no MCP, and no print mode.

## Start using psi

New to psi? Follow the [Quickstart](quickstart.md) to build the binary, set a
provider key, and complete a first task.

If psi is already built, choose what you want to do:

- [Use psi interactively](usage.md): write prompts, follow tool calls, steer
  ongoing work, and jump between sessions.
- [Choose a model](providers.md): every supported provider and the
  environment variable each one reads.
- [Continue or branch a session](sessions.md): resume, name, and clone work
  without losing history.
- [Configure psi](config.md): `settings.json`, `models.json`, instructions, and
  precedence between user and project files.
- [Understand how psi works](how-psi-works.md): the agent loop, context
  assembly, session files, and project trust.

## Look things up

- [Command line](cli.md): every flag, and what psi deliberately rejects.
- [Keyboard and commands](keyboard.md): keys, slash commands, pickers.
- [Compaction](compaction.md): when history is summarized and what settings
  control it.
- [Skills](skills.md): on-demand instruction files.
- [Environment variables](env.md): provider keys and directory overrides.
- [Limitations](limitations.md): what psi does not do.

## Work safely

Tool calls run with the permissions of the psi process. Project consent decides
which project-local settings, skills, and themes psi reads; it is not a
sandbox. Review [Security](security.md) before pointing psi at untrusted files
or unattended work.

## Read the docs without a checkout

Documentation is embedded in the binary:

```sh
psi --docs           # list topics
psi --docs config    # print one topic
```

The same topic list appears in psi's `<docs>` system-prompt section, so the
agent can read these pages when you ask about psi itself.
