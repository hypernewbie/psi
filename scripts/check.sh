#!/bin/sh
set -eu

no_js_only=false
case "${1:-}" in
"") ;;
--no-js-only) no_js_only=true ;;
*)
  printf 'usage: %s [--no-js-only]\n' "$0" >&2
  exit 2
  ;;
esac
if [ "$#" -gt 1 ]; then
  printf 'usage: %s [--no-js-only]\n' "$0" >&2
  exit 2
fi

# Reject tracked or untracked JavaScript and TypeScript sources. The recursive,
# case-insensitive pathspecs find nested files and mixed-case extensions. Git
# errors fail the gate; an empty result is the only accepted state.
check_no_javascript() {
  if ! command -v git >/dev/null 2>&1; then
    printf 'JavaScript gate: git is not on PATH\n' >&2
    exit 1
  fi
  files=$(git ls-files --cached --others --exclude-standard -- \
    ':(icase)*.js' ':(icase)**/*.js' \
    ':(icase)*.mjs' ':(icase)**/*.mjs' \
    ':(icase)*.cjs' ':(icase)**/*.cjs' \
    ':(icase)*.jsx' ':(icase)**/*.jsx' \
    ':(icase)*.ts' ':(icase)**/*.ts' \
    ':(icase)*.mts' ':(icase)**/*.mts' \
    ':(icase)*.cts' ':(icase)**/*.cts' \
    ':(icase)*.tsx' ':(icase)**/*.tsx' \
    ':(icase)package.json' ':(icase)**/package.json') || {
    printf 'JavaScript gate: git ls-files failed\n' >&2
    exit 1
  }
  if [ -n "$files" ]; then
    printf 'JavaScript and TypeScript sources are not allowed:\n%s\n' "$files" >&2
    exit 1
  fi
}

check_no_javascript
if [ "$no_js_only" = true ]; then
  exit 0
fi

unformatted=$(gofmt -l cmd internal docs)
if [ -n "$unformatted" ]; then
  printf 'Files need gofmt:\n%s\n' "$unformatted" >&2
  exit 1
fi
go vet ./...
go test ./...
go test -race ./...
go build ./...
