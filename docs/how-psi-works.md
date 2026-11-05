# How psi works

psi coordinates model requests, tool execution, context assembly, and session
storage. A session records everything that happened: messages, tool calls and
results, model changes, compactions, and state entries.

Entries in a session file form a tree. Each path through that tree is a branch.
The branch ending at the current entry is the active branch, and it supplies the
history for the next model request.

## Agent loop

A submitted message is appended to the active branch. psi builds a model
request from the system prompt, the active branch, the tool definitions, and
the model settings, then sends it through the selected provider.

The provider streams a response, which can contain text, thinking, and tool
calls. psi records the response, runs each tool call, and records the results.
That completes one turn. If the tool results require another model request, the
run continues with another turn. Otherwise the run ends.

Tool calls in one assistant response run concurrently. `write` and `edit` share
one per-file queue so mutations serialize; `read` and `bash` do not queue.
Results settle in call order behind a barrier the next request cannot cross.

Steering messages enter at the next turn boundary. Follow-up messages enter
after the current run ends. Cancelling stops the run, records unstarted calls as
`not_started`, and returns queued text to the editor.

## Context

The active branch supplies conversation history. psi converts session entries
into provider messages: user, assistant, tool result, and — for providers that
accept them — system updates.

The system prompt is assembled from ordered sections:

| Section | Contents |
|---|---|
| `preamble` | psi's base sentence, `SYSTEM.md`, or `--system-prompt` |
| `tools` | one line per selected tool |
| `rules` | tool-specific guidance and the fixed response rules |
| `docs` | the `psi --docs` topic list |
| `addendum` | `APPEND_SYSTEM.md` and `--append-system-prompt` |
| `project_context` | `AGENTS.md` and ancestor instruction files |
| `skills` | skill names, descriptions, and paths |
| `cwd` | the working folder |
| `loop` | active loop guidance, omitted when no loop is active |

A custom preamble (`SYSTEM.md` or `--system-prompt`) suppresses `tools`,
`rules`, and `docs`. The other sections still apply. Skill bodies are not in the
prompt; psi reads a skill file when the task matches its description.

Each section is stored in the session as a named entry. When configuration
changes, psi appends ordered section values and null values for removals. If
retained sections must move, psi removes them before restoring the new order.
Replaying a session rebuilds the prompt text sent to the provider.

## Sessions

Sessions are JSONL files under the agent directory, grouped per workspace.
Every record has an ID and a parent ID. The current leaf defines the active
branch; sibling branches stay out of context.

Resuming reopens the file and continues the active branch. Cloning copies the
branch into a new file. Compaction inserts a summary entry that replaces older
messages in later requests while the original entries stay in the tree.

psi writes session v3 and reads v1 and v2 for diagnosis only. A future version
is refused rather than rewritten. See [Sessions](sessions.md) and
[Recovery](recovery.md).

## Interfaces

The default interface is the inline terminal. psi renders session and agent
events in the editor region and never switches to fullscreen.

`--mode rpc` adds a headless interface for one session: JSONL commands on stdin,
JSONL events on stdout, no terminal, theme, or picker. It exists for a controlling
process, not for general shell scripting.

The native Phi bridge is the other external control surface. It is a local Unix
socket that publishes lifecycle and state.

## Trust and permissions

Before reading `.pi/settings.json`, project skills, project themes, or a project
`SYSTEM.md`/`APPEND_SYSTEM.md`, psi asks whether you trust the workspace.
`--approve` and `--deny-project` answer ahead of time; deny is the default.

Trust controls which project resources load. It is not a sandbox. Tools run
with the permissions of the psi process, and the `bash` tool inherits its
environment. Context files such as `AGENTS.md` load without the trust prompt,
matching Pi.
