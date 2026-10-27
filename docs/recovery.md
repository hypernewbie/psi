# Recovery

Interrupted sessions: psi detects unresolved tool calls before submission and
stops instead of rerunning uncertain side effects. Use `/new` for a fresh
session, or inspect a copy in Pi and use its branch tools. Psi does not provide
a branch-navigation UI and cannot determine whether an interrupted side effect
already happened.

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
