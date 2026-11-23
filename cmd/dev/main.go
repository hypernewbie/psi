package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, runProcess))
}

func run(args []string, stdout, stderr io.Writer, runner commandRunner) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	if isHelpArgument(args[0]) {
		if len(args) != 1 {
			printUsage(stderr)
			return 2
		}
		printUsage(stdout)
		return 0
	}

	command := args[0]
	arguments := args[1:]
	var options checkOptions

	switch command {
	case "check":
		var showHelp bool
		var err error
		options, showHelp, err = parseCheckArguments(arguments)
		if err != nil {
			fmt.Fprintf(stderr, "dev: %s\n", err)
			printCheckUsage(stderr)
			return 2
		}
		if showHelp {
			printCheckUsage(stdout)
			return 0
		}
	case "build", "install":
		if len(arguments) != 0 {
			fmt.Fprintf(stderr, "dev: %s accepts no arguments\n", command)
			printUsage(stderr)
			return 2
		}
	case "bake":
		if len(arguments) != 1 || arguments[0] == "" {
			fmt.Fprintln(stderr, "dev: bake requires exactly one nonempty destination path")
			printUsage(stderr)
			return 2
		}
	case "run":
		// Application flags and arguments belong to psi.
	default:
		fmt.Fprintf(stderr, "dev: unknown command %q\n", command)
		printUsage(stderr)
		return 2
	}

	root, err := checkoutRoot()
	if err != nil {
		fmt.Fprintf(stderr, "dev: %s\n", err)
		return 1
	}

	switch command {
	case "build":
		return runChild(runner, "go", []string{"build", "."}, stdout, stderr, "go build .")
	case "run":
		goArguments := append([]string{"run", "."}, arguments...)
		return runChild(runner, "go", goArguments, stdout, stderr, "go run .")
	case "install":
		return runChild(runner, "go", []string{"install", "."}, stdout, stderr, "go install .")
	case "bake":
		bakeArguments := []string{"run", "./cmd/catalog-bake", arguments[0]}
		return runChild(runner, "go", bakeArguments, stdout, stderr, "go run ./cmd/catalog-bake")
	case "check":
		return runCheck(root, options, stdout, stderr, runner)
	default:
		fmt.Fprintf(stderr, "dev: unsupported command %q\n", command)
		return 2
	}
}

func isHelpArgument(argument string) bool {
	return argument == "help" || argument == "-h" || argument == "--help"
}

func parseCheckArguments(arguments []string) (checkOptions, bool, error) {
	var options checkOptions
	showHelp := false
	for _, argument := range arguments {
		switch argument {
		case "--no-js-only":
			options.noJSOnly = true
		case "--skip-race":
			options.skipRace = true
		case "-h", "--help":
			showHelp = true
		default:
			return checkOptions{}, false, fmt.Errorf("unexpected check argument %q", argument)
		}
	}
	return options, showHelp, nil
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: go tool dev <command> [arguments]")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "Commands:")
	fmt.Fprintln(writer, "  build                 Build psi from this checkout.")
	fmt.Fprintln(writer, "  run [args...]          Run psi with the given arguments.")
	fmt.Fprintln(writer, "  install                Install psi from this checkout; respects GOBIN.")
	fmt.Fprintln(writer, "  bake <new-directory>   Generate catalog files in a new directory (network required).")
	fmt.Fprintln(writer, "  check [flags]          Run the repository checks.")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "Run all commands from the psi checkout root. Go uses its normal download settings.")
	fmt.Fprintln(writer, "Run go tool dev check --help for checker flags.")
}

func printCheckUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: go tool dev check [--no-js-only] [--skip-race]")
	fmt.Fprintln(writer, "Run from the checkout root. Check the Git file gate, gofmt, vet, tests, race tests, and build.")
	fmt.Fprintln(writer, "Requires Go 1.25 or later and Git on PATH.")
	fmt.Fprintln(writer, "On Linux and Windows, the default race tests require cgo and a compatible C compiler.")
	fmt.Fprintln(writer, "  --no-js-only  Run only the Git file gate.")
	fmt.Fprintln(writer, "  --skip-race   Run the other checks without race tests (used by Windows CI).")
}

func checkoutRoot() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot determine the current directory: %w", err)
	}

	for _, expectedFile := range []string{"go.mod", "main.go", "cmd/dev/main.go"} {
		info, err := os.Stat(filepath.Join(root, expectedFile))
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("run this command from the psi checkout root (missing expected file %s)", expectedFile)
		}
	}
	return root, nil
}
