# Recovery

Automatic retry: when a model response fails with a transient error, psi
retries the response before the turn ends, as Pi does. Transient errors are
rate limits, provider overload, server and gateway failures, connection
failures, and a stream that drops before it completes. psi prints the cause,
then `Retrying (n/max) in Ns... (escape to cancel)` while it waits. The default
budget is three retries. The wait starts at 2 s, doubles for each retry, and is
capped at 60 s (`retry` in [Configuration](config.md)).

The failed attempt keeps its visible partial text in the transcript and is
omitted from the model context, so the retried request sends the same context
as the failed one. After a reload, the transcript marks each omitted attempt
with `[session] Context edited (model context updated, transcript preserved).`

`Esc` cancels the wait and the turn ends with `Retry failed after n attempts:
Retry cancelled`. When the budget is spent, or a retried response fails with an
error that is not transient, the turn ends with `Retry failed after n
attempts:` and the cause. A response that fails with a non-transient error on
its first attempt is not retried and reports the cause alone. Each retry can
bill the provider again.

Automatic retry never retries a cancelled run, a context-limit error
([context-limit recovery](compaction.md) compacts instead), quota, budget, and
billing exhaustion, or an authentication or request-validation failure.
Rate-limit and server failures that arrive before a response starts streaming
are retried at the provider layer first, under the same budget and with the
same indicator; a `Retry-After` header sets that wait.

Retry a failed turn: when a request fails or is interrupted before the turn
finishes, one empty `Enter`, `/b`, or `/bump` retries it without adding a prompt.
Explicit commands can also request another response after a completed or
output-limited answer. Bump is available only while idle, with no run, picker,
or compaction active. Empty `Enter` works only for an unfinished turn;
explicit commands bypass that gate but keep auth, storage, and continuation
checks. It uses the current model, thinking level, tools,
system prompt, and compaction policy, and keeps saved answers. All admission
refusals stay silent, and refused busy commands are discarded without a queue
entry or restored draft. Once a run starts, failures, cancellation,
partial output, and unsaved output use the normal message behavior. A retry can
bill the provider again.

When a failed turn produced no visible output, its assistant stays in the
session as a hidden record and is omitted from ordinary continuation requests.
Known provider usage is saved separately from empty failure records, including
when a failed bump keeps an existing record instead of adding another one.
Visible partial text remains in the transcript. Existing compaction can
summarize that visible progress. A crash can hide whether a tool already ran;
the retry sends synthetic missing-tool results in request context, does not
write them to the session, and never re-executes the old batch. The model can
request a new call.

Interrupted sessions: psi follows Pi's replay behavior. It adds a synthetic
error result for each tool call with no saved result, then continues without
rerunning that tool. The synthetic result is sent only as model context and is
not written to the session. Psi cannot determine whether an interrupted side
effect already happened.

A shell entry that was never appended is not replayed or rerun. A cancelled or
infrastructure-failed shell entry has no exit code, so its outcome is unknown
apart from the saved partial output. Recovery never reruns an interactive
`!`/`!!` command. Append failure leaves the committed leaf unchanged and blocks
later mutations.

Truncated tail: psi refuses to append after an incomplete final record. Recover
from a copy:

```sh
cp session.jsonl session.jsonl.bak
# remove the broken last line or restore from backup, then reopen
```

Malformed interior records: psi reports `path:line` and stops. Fix the copy,
never the only original.

Disk errors / external growth: psi stops further model/tool actions and keeps
the last durable state. Session replacement (`/new`, `/resume`) is rejected
during an unsettled run.

Concurrent writers: two psi processes cannot mutate one session (advisory
lock + file identity, size, and modification-time checks). A same-size
external rewrite before verification is detected by a content hash, even if
the modification time is unchanged. This requires reading the whole session
before each append and cannot prevent a concurrent rewrite after verification.
Pi does not honor the psi lock;
never let Pi and psi write one file simultaneously.
