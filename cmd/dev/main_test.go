package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunHelpDoesNotRequireCheckoutOrStartChild(t *testing.T) {
	for _, arguments := range [][]string{{"help"}, {"-h"}, {"--help"}, {"check", "--help"}} {
		t.Run(strings.Join(arguments, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			runner := func(string, []string, io.Writer) error {
				called = true
				return nil
			}
			code := run(arguments, &stdout, &stderr, runner)
			if code != 0 || !strings.Contains(stdout.String(), "Usage:") || stderr.Len() != 0 {
				t.Fatalf("run(%q) = %d, stdout %q, stderr %q", arguments, code, stdout.String(), stderr.String())
			}
			if called {
				t.Fatal("help started a child process")
			}
		})
	}
}

func TestRunRejectsInvalidArgumentsBeforeStartingChild(t *testing.T) {
	for _, arguments := range [][]string{
		{}, {"unknown"}, {"check", "--unknown"}, {"check", "unexpected"}, {"--help", "extra"},
		{"build", "extra"}, {"install", "extra"}, {"bake"}, {"bake", "one", "two"}, {"bake", ""},
	} {
		t.Run(strings.Join(arguments, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			runner := func(string, []string, io.Writer) error {
				called = true
				return nil
			}
			code := run(arguments, &stdout, &stderr, runner)
			if code != 2 {
				t.Fatalf("run(%q) exit = %d, want 2; stderr %q", arguments, code, stderr.String())
			}
			if called {
				t.Fatal("invalid arguments started a child process")
			}
		})
	}
}

func TestCheckoutRootGuardRejectsOtherDirectoryBeforeStartingChild(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outsideDirectory := t.TempDir()
	if err := os.Chdir(outsideDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	var stdout, stderr bytes.Buffer
	called := false
	runner := func(string, []string, io.Writer) error {
		called = true
		return nil
	}
	code := run([]string{"check", "--no-js-only"}, &stdout, &stderr, runner)
	if code != 1 || !strings.Contains(stderr.String(), "checkout root") {
		t.Fatalf("run exit = %d, stderr %q", code, stderr.String())
	}
	if called {
		t.Fatal("root guard started a child process")
	}
}

func TestRunDispatchesAllDevelopmentCommands(t *testing.T) {
	for _, test := range []struct {
		name       string
		input      []string
		executable string
		arguments  []string
	}{
		{name: "build", input: []string{"build"}, executable: "go", arguments: []string{"build", "."}},
		{name: "run", input: []string{"run", "--help", "--label", "text with spaces"}, executable: "go", arguments: []string{"run", ".", "--help", "--label", "text with spaces"}},
		{name: "install", input: []string{"install"}, executable: "go", arguments: []string{"install", "."}},
		{name: "bake", input: []string{"bake", "catalog output with spaces"}, executable: "go", arguments: []string{"run", "./cmd/catalog-bake", "catalog output with spaces"}},
		{name: "bake help", input: []string{"bake", "--help"}, executable: "go", arguments: []string{"run", "./cmd/catalog-bake", "--help"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			enterFakeCheckout(t)
			var stdout, stderr bytes.Buffer
			var calls int
			runner := func(executable string, arguments []string, _ io.Writer) error {
				calls++
				if executable != test.executable || !reflect.DeepEqual(arguments, test.arguments) {
					t.Fatalf("child = %q %#v, want %q %#v", executable, arguments, test.executable, test.arguments)
				}
				return nil
			}
			if code := run(test.input, &stdout, &stderr, runner); code != 0 {
				t.Fatalf("run exit = %d, stderr %q", code, stderr.String())
			}
			if calls != 1 {
				t.Fatalf("child calls = %d, want 1", calls)
			}
		})
	}
}

func enterFakeCheckout(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"cmd/dev"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range map[string]string{
		"go.mod":          "module fixture\n",
		"main.go":         "package main\n",
		"cmd/dev/main.go": "package main\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func TestParseCheckArgumentsAcceptsBothFlagsInEitherOrder(t *testing.T) {
	for _, arguments := range [][]string{
		{"--no-js-only", "--skip-race"},
		{"--skip-race", "--no-js-only"},
	} {
		options, showHelp, err := parseCheckArguments(arguments)
		if err != nil || showHelp || !options.noJSOnly || !options.skipRace {
			t.Fatalf("parseCheckArguments(%q) = %+v, %t, %v", arguments, options, showHelp, err)
		}
	}
}
