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

Relative paths in a skill body resolve against the skill directory. When psi
loads the body it names the skill file and tells the model that references are
relative to that directory.

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
- `enableSkillCommands` is parsed for Pi compatibility but is not wired in psi.
  `/skill:<name>` completion and dispatch are currently always registered,
  whatever this value says.

`--no-skills` disables discovery for one run. Project paths load only after you
trust the workspace.

## Use a skill

```text
/skill:pdf-report summarize notes/2026-01.md
```

psi expands the invocation to Pi's exact block:

```text
<skill name="pdf-report" location="/home/me/.pi/agent/skills/pdf-report/SKILL.md">
References are relative to /home/me/.pi/agent/skills/pdf-report.

# PDF report

1. Read the notes with the read tool.
2. Generate the report with `scripts/build.py` in this directory.
</skill>

summarize notes/2026-01.md
```

Frontmatter is removed. The body and the argument tail are trimmed, and the
argument tail is appended only when it is nonempty. psi records the typed
`/skill:name args` as `psi.input` provenance, so editor recall and resubmission
keep the invocation even though the model context carries the full block.

The transcript keeps the skill collapsed:

```text
[skill] pdf-report (ctrl+o to expand)
› summarize notes/2026-01.md
```

`Ctrl+O` expands or collapses every skill block in the running process. The
arguments stay a separate user row, outside the skill group. The flag is kept
in memory: a restart starts collapsed, and the toggle is not saved. The same
formatter serves live input, delivered steering and follow-up queues, and
replay, including sessions written by Pi. A complete stored block wins over
`psi.input`. Text that is not a valid block still prefers its `psi.input`
invocation when one exists; otherwise it renders verbatim. A legacy psi
invocation (the old `Skill directory:` preamble) still renders its `/skill:`
provenance; existing sessions are never migrated.

An unknown name, an unreadable file, or a file over 256 KiB is refused in the
TUI, RPC mode, and the Phi bridge. The submitted draft is restored; nothing is
queued, stored, or sent to the provider.

During a run, `Enter` queues the expanded block as steering and `Alt+Enter`
queues it as a follow-up. Cancellation or a failed run restores the typed
`/skill:name args` form, not the expanded body, so resubmitting rereads the
file. Structured image recovery is unchanged. Editor recovery is still bounded
to 256 KiB for the combined restored draft, so a very large set of queued
messages can exceed that existing limit.

Project consent and local-file rules are unchanged: project skills load only
after you trust the workspace, and bodies are never fetched from a network.
`Ctrl+O` toggles skill blocks only; psi has no expandable tool-output panels.

The model can also read a skill file itself with the `read` tool when its
description matches the task.

## Not supported

There is no skill installer, no package distribution, and no remote fetching.
Skills are local files. See [Limitations](limitations.md).
