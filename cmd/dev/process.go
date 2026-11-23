package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type commandRunner func(executable string, arguments []string, stdout io.Writer) error

func runProcess(executable string, arguments []string, stdout io.Writer) error {
	command := exec.Command(executable, arguments...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() > 0 {
		return exitError.ExitCode()
	}
	return 1
}

func runChild(runner commandRunner, executable string, arguments []string, stdout, stderr io.Writer, operation string) int {
	if err := runner(executable, arguments, stdout); err != nil {
		fmt.Fprintf(stderr, "dev: %s failed: %v\n", operation, err)
		return commandExitCode(err)
	}
	return 0
}
