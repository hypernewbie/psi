package main

import (
	"fmt"
	"os"
)

const usageText = `usage: catalog-bake <new-output-directory>

Fetch the public models.dev and OpenRouter catalogs and write catalog.json and
catalog-source.json into a new directory.

The parent directory must already exist. The target directory must not exist.
A successful run publishes both files together. It applies reviewed token-limit
overrides and warns when an override target is missing; missing targets do not
block an otherwise valid candidate. This is a developer tool. It never runs
during build, install, or startup, and it does not modify the checked-in
catalog.
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
	if err := bake(ctx, os.Args[1], bakeDeps{warnings: os.Stderr}); err != nil {
		fmt.Fprintf(os.Stderr, "catalog-bake: %v\n", err)
		os.Exit(1)
	}
}
