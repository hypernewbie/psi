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
| `PHI_PI_ATTACHMENT_ROOT` | absolute handoff directory for staged attachments |

psi reads them once, clears all five from the process environment, and starts
the client after the first runtime generation. Absent or partial environment
means a plain pane: no client, no goroutine, and no output.

The endpoint must start with `unix:/`. Anything else is refused, so a stray
value cannot send the token off-host.

A handoff root is used only when it is an absolute, UTF-8 directory that is not
a symlink and still names the directory psi opened. An unusable root is not a
provisioning error: the bridge runs with one capability fewer.

## Capabilities

Twelve capabilities are advertised when the handoff root opens safely;
eleven otherwise.

- `pi.lifecycle.v1`: run start and settle edges plus a bounded 64-record
  journal that reconnects replay.
- `pi.state.v2`: state snapshots (session, leaf, model, thinking, optional
  context telemetry, optional compact readiness) and replies to
  `StateRequest`.
- `pi.models.v1`: list the live session's selectable models (providers with a
  usable credential only, like `/model`) and switch the live model.
- `pi.thinking.v1`: list the live model's advertised thinking levels and
  switch. A switch accepts any of `off`, `minimal`, `low`, `medium`, `high`,
  `xhigh`, and `max`; the provider clamps a level the model does not advertise
  and the session records the requested level. Only a level the model cannot
  apply is refused.
- `pi.history.v1`: read editor recall, newest first.
- `pi.context.v1`: token/window/percent telemetry, present only when known.
- `pi.skills.v1`: list session skills and dispatch one through the native
  `/skill:` path, including during an active run (queued with provenance).
- `pi.transcript-search.v2`: bounded visible-text search of the active
  branch, paginated by anchor and index.
- `pi.compact.v1`: readiness reporting and one admitted native compaction
  per request id.
- `pi.reload.v1`: reload instructions, skills, and theme/display resources
  through the native reload path, with session and context checks.
- `pi.syncboard.v1`: coordinator requests through the negotiated bridge
  capability; the tool is available only while that capability is connected.
- `pi.attachments.v3`: verified image and file references, advertised only
  with a usable handoff root.

Phi keeps its own fallbacks for the capabilities a pane does not advertise.
`pi.reload.v1` and `pi.syncboard.v1` are existing capabilities that predate
attachments; they are listed here for completeness, not as attachment
features.

## Attachments

An attachment prompt carries 1-8 references. Each reference names one basename
(`[A-Za-z0-9._-]+`, at most 128 bytes), a declared size, a MIME type, a kind,
and a lowercase SHA-256. Limits: 25 MiB per file, 50 MiB per prompt, 64 KiB of
prompt text, and 50 MiB of decoded image bytes across preparation, the running
loop's queue, an admitted prompt that has not started yet, and recovered input.

Images must be `.png` or `.jpg` with a matching `image/png` or
`image/jpeg` and a matching signature, and the live model must accept image
input. Ordinary files must declare `application/octet-stream`; psi verifies
their bytes and then keeps only a JSON-quoted absolute path text block for the
read tool. Bytes that match a PNG, JPEG, GIF, WebP, BMP, HEIC/HEIF, AVIF, or
SVG signature are refused as files.

Readiness (`attachmentReadiness`) is published only with a usable root. It
reports idle, pending messages, active compaction, bridge work, and optional
image availability. Image availability is present only when the live model is
known.

**Acknowledgments.** `REFUSED` is sent only before dispatch, with one of Phi's
fixed reasons (`unavailable`, `stale`, `busy`, `queued`, `compaction-active`,
`pending`, `command`, `unsupported-image`, `unsupported-model`, `missing-item`,
`invalid-item`, `limit`, `storage`, `error`). `DISPATCHED` reports admission,
not model receipt: an idle prompt started a run, and a prompt sent during a
running turn entered the steering queue. A lost receipt is not success; psi
never resends a prompt and never answers one request twice. If psi itself
cannot enqueue an acknowledgment (a dead stream, a full outbox), the prompt
already admitted stays admitted; Phi learns that as `UNKNOWN`, not as a
refusal that would replay it.

**Delivery and recovery.** Prepared content enters the session as text and
image blocks, so a steered image reaches the next provider request. If a run
fails, is cancelled, or exits before the prompt became durable, psi keeps the
verified content in a recovery batch, sends no further ack, and never replays
it automatically. The batch blocks new attachment prompts with `queued` and is
discarded when the session is replaced. Session replacement, shutdown, or
process exit can lose undelivered content; Phi's saved copy is the durable
fallback.

**Privacy.** Image bytes never appear on the bridge stream. The root path is
never published in hello, snapshots, acknowledgments, telemetry, or errors.
Only the ordinary-file content block carries the checked absolute path.

**Guarantees.** Identity, session, leaf, model, root, and readiness are checked
at admission and again before dispatch. A change that happens after dispatch
is outside that preparation-time guarantee. Attachments are unavailable on
Windows.

## Failure behavior

The bridge is silent: errors never reach the TUI.

- Transient failures (unavailable, resource exhausted, internal, unknown)
  retry from 250 ms to 5 s with up to 20% upward jitter. `Close` interrupts a
  wait immediately.
- Terminal failures (unauthenticated, permission denied, failed precondition,
  cancelled, deadline exceeded) stop the client for the rest of the process.
  This includes rejections that arrive while psi sends its initial Hello.
- A missing `Accepted` within 4 s counts as transient. Phi's own hello
  deadline is 5 s, and its exceeded status is terminal, so the local timer
  fires first and keeps the retry.
- A stream write blocked past 5 s is a local transport fault, not Phi's
  verdict: the stream is dropped and reconnected.
- Capability requests are forwarded to the host loop; a full request queue
  answers unavailable instead of blocking the stream. Replies queue in a
  bounded outbox so a Phi that stops reading cannot stall the host.
- Attachment preparation has a 10 s budget. Phi waits up to 20 s for the ack.
  A preparation that misses the budget refuses `unavailable` once and keeps the
  slot until its worker returns; the late result is discarded without another
  answer.
- Reconnects reuse the runtime id and resend the retained journal, so Phi
  merges a gap once and ignores duplicate sequences. Requests and replies
  from a lost stream are never replayed.
- A new runtime generation (new session, replay, or clone) rotates the runtime
  id, resets the journal, and rebinds the stream.

## Disabling

Unset the variables above before launching psi, or set Phi's
`pi_bridge_no_inject` option so Phi does not provision the pane. A pane is
managed only when both sides support it; otherwise it stays plain.

`pi_bridge_no_inject` controls Phi's own injection. It does not disable psi's
native bridge: a pane that inherits the five variables still connects. Clear
the variables to opt out of the native bridge.

## Limits

- Windows panes stay plain: Phi uses a named pipe and psi has no pipe dialer.
- Only the capabilities above exist. Attachments remain a Phi fallback.
- psi output is not streamed to Phi. The pane renders its own PTY.

## Contract

The wire contract, bounds, and status mappings live in Phi at
`bonus/pi-phi/PROTOCOL.md`, pinned at revision `a207d6b4`. The bindings under
`internal/pibridge/pb` are generated from `internal/pibridge/proto/bridge.proto`
and must be regenerated from that same Phi revision.
