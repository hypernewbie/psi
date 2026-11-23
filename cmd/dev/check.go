package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type checkOptions struct {
	noJSOnly bool
	skipRace bool
}

// Each case-insensitive pathspec has root and nested forms to preserve the
// checker's existing coverage for source extensions and package manifests.
var forbiddenPathspecs = []string{
	":(icase)*.js", ":(icase)**/*.js",
	":(icase)*.mjs", ":(icase)**/*.mjs",
	":(icase)*.cjs", ":(icase)**/*.cjs",
	":(icase)*.jsx", ":(icase)**/*.jsx",
	":(icase)*.ts", ":(icase)**/*.ts",
	":(icase)*.mts", ":(icase)**/*.mts",
	":(icase)*.cts", ":(icase)**/*.cts",
	":(icase)*.tsx", ":(icase)**/*.tsx",
	":(icase)package.json", ":(icase)**/package.json",
}

func runCheck(root string, options checkOptions, stdout, stderr io.Writer, runner commandRunner) int {
	if code := checkForbiddenFiles(runner, stderr); code != 0 || options.noJSOnly {
		return code
	}

	fmt.Fprintln(stdout, "Checking gofmt...")
	rootFiles, err := rootGoFiles(root)
	if err != nil {
		fmt.Fprintf(stderr, "dev: enumerate root Go files failed: %v\n", err)
		return 1
	}
	formatArguments := append([]string{"-l"}, rootFiles...)
	formatArguments = append(formatArguments, "cmd", "internal", "docs")
	var formatOutput bytes.Buffer
	formatErr := runner("gofmt", formatArguments, &formatOutput)
	if formatOutput.Len() > 0 {
		fmt.Fprintf(stderr, "dev: files need gofmt:\n%s", formatOutput.String())
		if formatOutput.Bytes()[formatOutput.Len()-1] != '\n' {
			fmt.Fprintln(stderr)
		}
	}
	if formatErr != nil {
		fmt.Fprintf(stderr, "dev: gofmt -l failed: %v\n", formatErr)
		return commandExitCode(formatErr)
	}
	if formatOutput.Len() > 0 {
		return 1
	}

	if code := runGoStep(runner, stdout, stderr, "go vet", "go vet ./...", "vet", "./..."); code != 0 {
		return code
	}
	if code := runGoStep(runner, stdout, stderr, "go test", "go test ./...", "test", "./..."); code != 0 {
		return code
	}
	if options.skipRace {
		fmt.Fprintln(stdout, "Skipping race tests: --skip-race was provided.")
	} else if code := runGoStep(runner, stdout, stderr, "go test -race", "go test -race ./...", "test", "-race", "./..."); code != 0 {
		return code
	}
	return runGoStep(runner, stdout, stderr, "go build", "go build ./...", "build", "./...")
}

func runGoStep(runner commandRunner, stdout, stderr io.Writer, label, operation string, arguments ...string) int {
	fmt.Fprintf(stdout, "Checking %s...\n", label)
	return runChild(runner, "go", arguments, stdout, stderr, operation)
}

func rootGoFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		name := entry.Name()
		// The shell glob ./*.go omitted dotfiles; preserve that visible-file scope.
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".go") || entry.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", name, err)
		}
		if info.Mode().IsRegular() {
			files = append(files, "./"+name)
		}
	}
	sort.Strings(files)
	return files, nil
}

func checkForbiddenFiles(runner commandRunner, stderr io.Writer) int {
	arguments := []string{
		"ls-files", "--cached", "--others", "--exclude-standard",
		"-z", // NUL records preserve filenames that contain whitespace or newlines.
		"--",
	}
	arguments = append(arguments, forbiddenPathspecs...)

	var output bytes.Buffer
	if err := runner("git", arguments, &output); err != nil {
		fmt.Fprintf(stderr, "dev: JavaScript and TypeScript file gate failed: git ls-files: %v\n", err)
		return 1
	}

	if output.Len() == 0 {
		return 0
	}
	if output.Bytes()[output.Len()-1] != 0 {
		fmt.Fprintf(stderr, "dev: Git file gate returned malformed NUL-delimited output: %s\n", strconv.Quote(string(output.Bytes())))
		return 1
	}

	paths := bytes.Split(output.Bytes(), []byte{0})
	fmt.Fprintln(stderr, "dev: JavaScript and TypeScript sources are not allowed:")
	for _, path := range paths[:len(paths)-1] {
		fmt.Fprintf(stderr, "  %s\n", strconv.Quote(string(path)))
	}
	return 1
}
