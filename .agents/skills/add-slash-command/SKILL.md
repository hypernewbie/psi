---
name: add-slash-command
description: Add or change a static psi slash command and keep parsing, runtime admission, documentation, and tests aligned.
disable-model-invocation: true
---

# Add a slash command

Run these steps from the checkout root. This skill lives in
`.agents/skills/add-slash-command`, so `../../..` from this skill's directory is
the checkout root.

1. Decide the command's scope. Check whether RPC or Phi needs matching
   behavior. Document any intentional differences.
2. Add or change the spec in `internal/terminal/commands.go`. If the command
   accepts an argument, update `CommandTakesArgument` in the same file. Keep one
   completion source: `CommandNames` and `CommandMetadata` read the same table.
3. In `internal/app/app.go`, follow `terminal.ParseCommand` through both the
   idle and busy paths. Update the applicable handler or admission rule. Shared
   dispatch is in `(*runtime).command`; some commands are handled before that
   method is called. The generic busy fallback restores recognized commands
   and a bare slash to the draft. Unknown command names follow the normal
   prompt path.
4. Keep `/skill:` distinct from unknown slash input. `/skill:name` is a dynamic
   family. Psi refuses a missing skill, but treats an unknown command such as
   `/not-a-command` as prompt text.
5. Update the static declarations in `docs/keyboard.md` for both idle and busy
   behavior. Update any affected public documentation, CLI help, and prompt
   guidance under the repository rules. If you add, remove, or rename a served
   `--docs` topic, update the topic list in `docs/docs.go`.
6. Add observable tests for the changed behavior. Cover busy behavior if it
   changes. Do not duplicate the existing parser tests.
7. Run the command-doc parity test from the checkout root:

   ```sh
   go test ./internal/terminal -run '^(TestCommandDocsParity|TestCommandDocsParityRejectsDrift)$'
   ```

   This checks names, not dispatch. Also run the tests that cover the changed
   behavior.
