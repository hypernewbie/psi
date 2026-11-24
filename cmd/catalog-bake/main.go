package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usageText = `usage: catalog-bake <new-output-directory>

Fetch the public models.dev and OpenRouter catalogs and write catalog.json and
catalog-source.json into a new directory.

The parent directory must already exist. The target directory must not exist.
A successful run publishes both files together. It applies reviewed token-limit
overrides and reports missing targets as warnings. A missing target or a warning
write failure does not block an otherwise valid candidate. This is a developer
tool. It never runs during build, install, or startup, and it does not modify
the checked-in catalog.
`

func main() {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			fmt.Fprint(os.Stdout, usageText)
			return
		}
	}
	if len(os.Args) != 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	ctx, stop := interruptContext()
	defer stop()
	if err := bakeCLI(ctx, os.Args[1], bakeDeps{}); err != nil {
		fmt.Fprintf(os.Stderr, "catalog-bake: %v\n", err)
		os.Exit(1)
	}
}

func bakeCLI(ctx context.Context, out string, deps bakeDeps) error {
	// On Unix, a broken stderr pipe can terminate the process before a
	// warning write returns an error. Let the bake continue without it.
	signal.Ignore(syscall.SIGPIPE)
	deps.warnings = os.Stderr
	return bake(ctx, out, deps)
}
