# Quickstart

Build with Go alone (no Node, npm, or Pi packages):

```sh
go build -o psi ./cmd/psi
./psi --help
./psi
```

Daily use (Anthropic Messages):

```sh
export ANTHROPIC_API_KEY="..."
./psi --provider anthropic --model claude-sonnet-4-5
```

Resume:

```sh
./psi --continue
./psi --resume
./psi --session ~/.pi/agent/sessions/<dir>/<file>.jsonl
```

Cancel: `Ctrl+C` cancels a run. Idle `Ctrl+C` clears the draft, second exits.
`Ctrl+D` or `/quit` exits. `Ctrl+J` inserts a newline.

See `COMPATIBILITY.md` for the pinned baseline and boundaries.
