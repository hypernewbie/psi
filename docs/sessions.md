# Session compatibility

Pinned: `temp/pi-mono` at `d5b3de2`, `packages/coding-agent@1.4.0`, session v3.
Full matrix in `COMPATIBILITY.md`.

- v3 required for writes; v1/v2 diagnose (no auto-migration); future versions refuse.
- Branches project to the active path; siblings stay out of context.
- Compactions rebuild through the same projector (latest wins, retain-none supported).
- Context edits: `replacement: {content} | null`, latest wins, string normalized for assistant/toolResult.
- Branch summaries and custom messages contribute context; `bashExecution` respects `excludeFromContext`.
- Usage/labels/session_info/custom state are preserved, never instructions.
- Historical extension tool results are ordinary history (no execution).
- Deferred/background state refuses continuation (no provider-job subsystem).
- Tool results remain in the session even though the terminal shows only `name: state`.
