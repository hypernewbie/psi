# Live transcript spacing audit

## Scope

Baseline: `99ac018`. This audit covers live submissions, assistant streams, tool chains, history commands, restart replay, and configuration feedback.

Pi source reference: `d5b3de22d0e24a3e9c38a9410d3329b099717283` in `temp/pi-mono`.

- `UserMessageComponent` uses vertical padding around user text.
- `AssistantMessageComponent` tests for visible content before adding its spacer.
- `InteractiveMode.showStatus` replaces consecutive status messages instead of appending each one.
- `AgentSession.setThinkingLevel` saves a change only when the effective level changes.
- `InteractiveMode.renderSessionEntries` converts conversation entries to chat messages.
- Model and thinking records remain session state, not chat messages.

Psi retains compact tool summaries and native scrollback. This audit does not copy Pi's expanded tool panels or change the terminal model.

## Corrections

| Area | Previous behavior | Corrected behavior |
|---|---|---|
| Tool-only provider responses | Each response start ended the tool group with a blank row. | Only visible prose or run settlement ends the group. |
| Blank assistant chunks | Whitespace-only output created gaps during stream settlement and replay. | Empty visible blocks do not separate adjacent tools. |
| User submissions | A submission directly followed command output without a gap. | One blank row separates the submission from previous output. Existing message padding is not duplicated. |
| Consecutive turns | Separate spacing paths risked different live and history layouts. | The PTY regression compares live output, both history commands, and restart replay. |
| Initial stream format | The first Markdown stream ignored display geometry until another presentation update. | The first frame uses the same output padding and code indentation as replay. |
| User color policy | Live submission passed a different no-color argument from replay. | Both paths use the active no-color policy. |
| Transcript headings | Session and history headings touched the first message. | A blank row follows the heading and any offline notice. |
| Thinking/model feedback | Each successful change appended another chat log. | The footer shows the selection without appending a success log. Errors remain visible. |
| Configuration replay | Restart and history showed each model/thinking record as a message. | The records remain durable but do not enter the chat transcript. |
| Unchanged thinking | Repeated selection added redundant records. Cycling a model with one level also added records. | An unchanged level adds no record. Single-level cycling does nothing. |

The committed-row tracker retains only boundary state. It does not retain transcript bodies or count editor, picker, tool, or thinking rows as committed output.

The gap correction does not collapse blank lines inside Markdown or code. Explicit commands such as `/help`, `/session`, and `/history` still produce their requested output.

## Regression coverage

- `internal/app/ux_spacing_review_pty_test.go` compares the same transcript across live output, `/history all`, `/history 2`, and restart. It includes separate tool-only responses, a blank chunk, a code block, custom indentation, and a second turn.

- `internal/terminal/spacing_review_test.go` covers idempotent gaps, pending frames, styled blank rows, repeated stream close, blank chunks, color modes, and first-frame display geometry.

- `internal/app/ux_spacing_review_test.go` covers quiet configuration changes, durable state, no-op selection, and configuration-free replay.

- `internal/app/model_picker_test.go` checks direct commands, picker selection, keyboard cycling, footer updates, restart state, and the exact number of thinking changes.

The new regressions reproduced missing gaps, extra gaps, display differences, and configuration log spam before the corrections.

## Local evidence

- `./scripts/check.sh` passed in a serial run. It includes formatting, vet, ordinary tests, race tests, and builds.

- Ten repeated race runs passed for the spacing, thinking, configuration, and picker regressions.

- The full app suite passed with `-psi-test-binary="$PWD/psi"`. The new PTY regression uses synthetic configuration and a loopback endpoint.

- Cross-builds passed for Linux, macOS, and Windows, each on amd64 and arm64.

- `./psi` was rebuilt with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -o ./psi ./cmd/psi`. SHA-256: `f492016265c311cff3c0b511aa512dc6d4c184425f72d69530f810d08c73cad1`.

The first parallel validation run failed the existing theme-watch burst test. Two of 20 concurrent repeats also failed. Ten idle repeats and the serial full check passed. No theme-watch code changed for this audit.

## Evidence limits

Pi UI behavior received source review, not an executable UI comparison. Native macOS/Windows terminal behavior and human readability remain unmeasured.

No live provider request ran. Vci remains unavailable because no client profile exists. No coordinator configuration changed.
