# Skills

A skill is an instruction file psi loads on demand. The description is
advertised in the system prompt; the body is read only when the task matches,
so a set of skills costs a few lines of context until one is used.

## Create a skill

A skill is a directory containing `SKILL.md`:

```text
~/.pi/agent/skills/pdf-report/SKILL.md
```

```markdown
---
name: pdf-report
description: Build a PDF report from markdown notes
---

# PDF report

1. Read the notes with the read tool.
2. Generate the report with `scripts/build.py` in this directory.
```

Frontmatter keys:

| Key | Required | Meaning |
|---|---|---|
| `name` | yes | Command name: `/skill:pdf-report` |
| `description` | yes | One line, at most 1024 characters |
| `disable-model-invocation` | no | `true` hides it from the prompt |

Relative paths in a skill body resolve against the skill directory. psi tells
the model the absolute directory when it loads the body.

## Discovery

psi looks in these places, in order:

1. `<agentDir>/skills`
2. `<cwd>/.pi/skills` — after project consent
3. `<cwd>/.agents/skills` — after project consent
4. `~/.agents/skills`
5. Paths listed in the `skills` setting

A `SKILL.md` at the root of a directory makes that directory one skill;
otherwise psi walks subdirectories and also accepts top-level `*.md` files.
Symlinks are followed with a cycle guard.

Limits: 256 skills, 256 KiB per file, exclusive-use of a name. A duplicate name
keeps the first file and reports a diagnostic; an unreadable or oversized file
is skipped with a diagnostic instead of aborting startup.

## Settings

```json
{
  "skills": ["~/work/skills", "-legacy-*"],
  "enableSkillCommands": true
}
```

- A plain path adds a skill file or directory.
- A path starting with `-` or `!` excludes matching skills by path or glob.
- `enableSkillCommands` (default on) registers `/skill:<name>` completion and
  dispatch.

`--no-skills` disables discovery for one run. Project paths load only after you
trust the workspace.

## Use a skill

```text
/skill:pdf-report summarize notes/2026-01.md
```

The body and the arguments become the user message, and psi records
`psi.input` provenance so the expanded text is reproducible. The transcript
shows the invocation once, not the expanded body.

The model can also read a skill file itself with the `read` tool when its
description matches the task.

## Not supported

There is no skill installer, no package distribution, and no remote fetching.
Skills are local files. See [Limitations](limitations.md).
