# Recovery

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
lock + file identity, size, and modification-time checks). Pi does not honor the psi lock; never let Pi and psi write
one file simultaneously.
