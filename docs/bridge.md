# Phi bridge

psi speaks Phi's integration protocol natively when Phi provisions the pane.
No extension is loaded and no extra process runs.

## Activation

Phi sets these variables when it spawns psi:

| variable | meaning |
| --- | --- |
| `PHI_PI_BRIDGE_ENDPOINT` | `unix:<absolute socket path>` |
| `PHI_PI_BRIDGE_PANE_ID` | pane identity, sent in metadata |
| `PHI_PI_BRIDGE_PROCESS_ID` | process identity, sent in metadata |
| `PHI_PI_BRIDGE_TOKEN` | per-process token, sent in metadata only |
| `PHI_PI_ATTACHMENT_ROOT` | unused here; read only to clear it |

psi reads them once, clears all five from the process environment, and starts
the client after the first runtime generation. Absent or partial environment
means a plain pane: no client, no goroutine, and no output.

The endpoint must start with `unix:/`. Anything else is refused, so a stray
value cannot send the token off-host.

## Capabilities

Nine capabilities are advertised:

- `pi.lifecycle.v1`: run start and settle edges plus a bounded 64-record
  journal that reconnects replay.
- `pi.state.v2`: state snapshots (session, leaf, model, thinking, optional
  context telemetry, optional compact readiness) and replies to
  `StateRequest`.
- `pi.models.v1`: list the resolved catalog and switch the live model.
- `pi.thinking.v1`: list the live model's thinking levels and switch.
- `pi.history.v1`: read editor recall, newest first.
- `pi.context.v1`: token/window/percent telemetry, present only when known.
- `pi.skills.v1`: list session skills and dispatch one through the native
  `/skill:` path, including during an active run (queued with provenance).
- `pi.transcript-search.v2`: bounded visible-text search of the active
  branch, paginated by anchor and index.
- `pi.compact.v1`: readiness reporting and one admitted native compaction
  per request id.

Phi keeps its own fallbacks for the capabilities a pane does not advertise.
Attachments and reload remain Phi-side work.

## Failure behavior

The bridge is silent: errors never reach the TUI.

- Transient failures (unavailable, resource exhausted, internal, unknown)
  retry from 250 ms to 5 s with up to 20% upward jitter. `Close` interrupts a
  wait immediately.
- Terminal failures (unauthenticated, permission denied, failed precondition,
  cancelled, deadline exceeded) stop the client for the rest of the process.
- A missing `Accepted` within 4 s counts as transient. Phi's own hello
  deadline is 5 s, and its exceeded status is terminal, so the local timer
  fires first and keeps the retry.
- A stream write blocked past 5 s is a local transport fault, not Phi's
  verdict: the stream is dropped and reconnected.
- Capability requests are forwarded to the host loop; a full request queue
  answers unavailable instead of blocking the stream. Replies queue in a
  bounded outbox so a Phi that stops reading cannot stall the host.
- Reconnects reuse the runtime id and resend the retained journal, so Phi
  merges a gap once and ignores duplicate sequences. Requests and replies
  from a lost stream are never replayed.
- A new runtime generation (new session, replay, or clone) rotates the runtime
  id, resets the journal, and rebinds the stream.

## Disabling

Unset the variables above before launching psi, or set Phi's
`pi_bridge_no_inject` option so Phi does not provision the pane. A pane is
managed only when both sides support it; otherwise it stays plain.

## Limits

- Windows panes stay plain: Phi uses a named pipe and psi has no pipe dialer.
- Only the capabilities above exist. Attachments and reload remain Phi
  fallbacks; syncboard is not bridged.
- psi output is not streamed to Phi. The pane renders its own PTY.

## Contract

The wire contract, bounds, and status mappings live in Phi at
`bonus/pi-phi/PROTOCOL.md`, pinned at revision `a207d6b4`. The bindings under
`internal/pibridge/pb` are generated from `internal/pibridge/proto/bridge.proto`
and must be regenerated from that same Phi revision.
