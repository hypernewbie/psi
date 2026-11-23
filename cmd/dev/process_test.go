package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestProcessRunnerHelper(t *testing.T) {
	if os.Getenv("PSI_DEV_PROCESS_HELPER") != "1" {
		return
	}

	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator == -1 {
		fmt.Fprintln(os.Stderr, "missing helper argument separator")
		os.Exit(9)
	}

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read helper stdin: %v\n", err)
		os.Exit(9)
	}
	fmt.Fprintf(os.Stdout, "stdin=%s\n", input)
	for _, argument := range os.Args[separator+1:] {
		fmt.Fprintf(os.Stdout, "arg=%q\n", argument)
	}
	fmt.Fprintln(os.Stderr, "separate child stderr")
	os.Exit(7)
}

func TestRunProcessPreservesArgumentsStreamsStdinAndExitStatus(t *testing.T) {
	t.Setenv("PSI_DEV_PROCESS_HELPER", "1")

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdinWriter, "input payload\n"); err != nil {
		t.Fatal(err)
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}

	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdin, originalStderr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = stdinReader, stderrWriter
	defer func() {
		os.Stdin, os.Stderr = originalStdin, originalStderr
	}()

	var stdout bytes.Buffer
	err = runProcess(os.Args[0], []string{
		"-test.run=^TestProcessRunnerHelper$", "--", "alpha with spaces", "--child-option", "omega",
	}, &stdout)
	os.Stdin, os.Stderr = originalStdin, originalStderr
	stdinReader.Close()
	stderrWriter.Close()
	stderrOutput, readErr := io.ReadAll(stderrReader)
	stderrReader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}

	if commandExitCode(err) != 7 {
		t.Fatalf("process exit = %d, error %v; want 7", commandExitCode(err), err)
	}
	wantStdout := "stdin=input payload\n\narg=\"alpha with spaces\"\narg=\"--child-option\"\narg=\"omega\"\n"
	if stdout.String() != wantStdout {
		t.Fatalf("stdout = %q, want %q", stdout.String(), wantStdout)
	}
	if strings.TrimSpace(string(stderrOutput)) != "separate child stderr" {
		t.Fatalf("stderr = %q", stderrOutput)
	}
}
