# Compaction

Compaction replaces older conversation entries with a summary so a long session
can continue inside the model's context window. Original entries stay in the
session file. A compaction record tells later requests to use the summary plus
the retained tail.

## When it runs

Two mechanisms exist.

**Threshold compaction** runs before a model request. psi estimates the context
(system prompt + active branch + tool definitions) and compacts when the
estimate exceeds `contextWindow - reserveTokens`. It is controlled by
`compaction.enabled`, which defaults to on.

If a provider reports a context-limit error and the run has not streamed any
text yet, psi compacts once and retries. A second limit error ends the run.

**Adaptive compaction** (`/ac`) watches growth. By default it targets the lower
of 50% of the context window or 230,000 tokens, and checks completed runs at
max(200,000 tokens, 40% of the window) after ten minutes. `/ac status` prints the
current policy; `/ac threshold`, `/ac end-turn`, and `/ac end-turn-delay` change
it for the session. `/ac compact` entered during a run waits for the run to
settle, compacts, then resumes the task.

## Manual compaction

```text
/compact              # summarize settled history now
/compact focus on the API changes
```

`/compact` calls the provider for a real summary and reports start and outcome.
It is cancellable. Cancelling leaves the original context untouched.

## Settings

```json
{
  "compaction": {
    "enabled": true,
    "reserveTokens": 16384,
    "keepRecentTokens": 20000,
    "modelOverrides": {
      "anthropic/claude-example": { "reserveTokens": 32768 }
    }
  }
}
```

| Key | Meaning |
|---|---|
| `enabled` | Enable threshold compaction |
| `reserveTokens` | Space kept free for the next response |
| `keepRecentTokens` | Size of the tail left uncompacted |
| `modelOverrides` | Per `provider/model` reserve override |

`/ac compact` can use a separate compactor model. `/ac compactor current`
selects the active model for this session; `/ac compactor clear` removes that
choice. Without a saved choice, psi summarizes with the active model.

## What a summary keeps

The summary request asks for a structured checkpoint: goal, constraints,
progress, decisions, next steps, and critical context. psi keeps exact file
paths, function names, and error messages when the model preserves them.

Split turns are handled in two requests: history up to the turn boundary, then
a short prefix summary for the part of the turn that is being cut.

Tool results that were interrupted are represented by a synthetic error entry
for the model only; it is not written to the session. See
[Recovery](recovery.md).

## After compaction

Later requests replay the summary entry plus every entry after it. The
transcript still shows the original messages, so `/history` and the visible
scrollback do not change.

Compaction never rewrites the session format. Pi can read a compacted psi
session, and psi can read a compacted Pi session, as long as both are session
version 3.

## Limits

psi replays existing branch summaries but has no command to create one. There
is no compaction hook or summary template override. `/ac` policy controls are in
memory and reset when another session is opened. See [Limitations](limitations.md).
