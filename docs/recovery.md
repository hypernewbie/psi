# Recovery

Retry a failed turn: when a request fails or is interrupted before the turn
finishes, one empty `Enter` or `/b` retries it without adding a prompt. Bump is
available only while idle, with no run, picker, or compaction active, and only
for an unfinished turn. It uses the current model, thinking level, tools,
system prompt, and compaction policy. Auth, storage, and continuation checks
still apply. A completed answer or an output-limited response (`length`) is not
retried. Empty `Enter` stays silent when bump is unavailable; `/b` reports the
reason. A retry can bill the provider again.

The failed assistant stays in the session as a hidden record and is omitted
from ordinary continuation requests. Visible partial text remains in the
transcript. Existing compaction can summarize that visible progress. A crash
can hide whether a tool already ran; the retry sends synthetic missing-tool
results in request context, does not write them to the session, and never
re-executes the old batch. The model can request a new call.

Interrupted sessions: psi follows Pi's replay behavior. It adds a synthetic
error result for each tool call with no saved result, then continues without
rerunning that tool. The synthetic result is sent only as model context and is
not written to the session. Psi cannot determine whether an interrupted side
effect already happened.

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
