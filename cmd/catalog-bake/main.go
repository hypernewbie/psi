package main

import (
	"fmt"
	"os"
)

const usageText = `usage: catalog-bake <new-output-directory>

Fetch the public models.dev and OpenRouter catalogs and write catalog.json and
catalog-source.json into a new directory.

The parent directory must already exist. The target directory must not exist.
A successful run publishes both files together. The command is a developer
tool: it never runs during build, install, or startup, and it does not modify
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
	if err := bake(ctx, os.Args[1], bakeDeps{}); err != nil {
		fmt.Fprintf(os.Stderr, "catalog-bake: %v\n", err)
		os.Exit(1)
	}
}
