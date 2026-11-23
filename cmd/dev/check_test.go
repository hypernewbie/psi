package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGitFileGateRejectsEveryForbiddenExtension(t *testing.T) {
	root := newGitFixture(t)
	// A caller's Git pathspec mode must not change the checkout policy.
	t.Setenv("GIT_LITERAL_PATHSPECS", "1")

	for _, name := range []string{
		"blocked.JS",
		"nested/blocked.MjS",
		"blocked.CJS",
		"nested/blocked.JSX",
		"blocked.TS",
		"nested/blocked.MTS",
		"blocked.CTS",
		"nested/space path/blocked.TsX",
		"PACKAGE.JSON",
		"nested/package.json",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("forbidden\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		var stderr bytes.Buffer
		if code := checkForbiddenFiles(runProcess, &stderr); code != 1 {
			t.Fatalf("file gate accepted %q with exit %d", name, code)
		}
		if !strings.Contains(stderr.String(), strconv.Quote(filepath.ToSlash(name))) {
			t.Fatalf("file gate diagnostic %q does not quote %q", stderr.String(), name)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(root, "tracked.js"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, "add", "--", "tracked.js")
	var stderr bytes.Buffer
	if code := checkForbiddenFiles(runProcess, &stderr); code != 1 || !strings.Contains(stderr.String(), `"tracked.js"`) {
		t.Fatalf("file gate did not reject tracked file: exit %d, stderr %q", code, stderr.String())
	}
	runGitFixture(t, "rm", "--cached", "--", "tracked.js")
	if err := os.Remove(filepath.Join(root, "tracked.js")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored-dir/\nignored.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ignored-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored-dir", "ignored.ts"), []byte("ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.ts"), []byte("ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"package.json.go", "source.js.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stderr.Reset()
	if code := checkForbiddenFiles(runProcess, &stderr); code != 0 {
		t.Fatalf("file gate rejected allowed files: exit %d, stderr %q", code, stderr.String())
	}

	if err := os.WriteFile(filepath.Join(root, "ignored-dir", "forced.TsX"), []byte("forced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, "add", "-f", "--", "ignored-dir/forced.TsX")
	stderr.Reset()
	if code := checkForbiddenFiles(runProcess, &stderr); code != 1 || !strings.Contains(stderr.String(), `"ignored-dir/forced.TsX"`) {
		t.Fatalf("file gate did not reject force-tracked ignored file: exit %d, stderr %q", code, stderr.String())
	}
}

func TestGitFileGateQuotesNewlineInName(t *testing.T) {
	var stderr bytes.Buffer
	runner := func(_ string, _ []string, stdout io.Writer) error {
		_, err := io.WriteString(stdout, "line\nbreak.TS\x00allowed.go\x00")
		return err
	}
	if code := checkForbiddenFiles(runner, &stderr); code != 1 {
		t.Fatalf("file gate exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `"line\nbreak.TS"`) || strings.Contains(stderr.String(), "allowed.go") {
		t.Fatalf("expected only the quoted forbidden name, got %q", stderr.String())
	}
}

func TestGitFileGateFailsClosedOnStartupAndCommandErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		output string
	}{
		{name: "startup", err: &exec.Error{Name: "git", Err: exec.ErrNotFound}},
		{name: "command with empty output", err: errors.New("git exited unsuccessfully")},
		{name: "command with partial output", err: errors.New("git exited unsuccessfully"), output: "partial.js\x00"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			runner := func(_ string, _ []string, stdout io.Writer) error {
				_, writeErr := io.WriteString(stdout, test.output)
				if writeErr != nil {
					return writeErr
				}
				return test.err
			}
			if code := checkForbiddenFiles(runner, &stderr); code != 1 {
				t.Fatalf("file gate exit = %d, want 1; stderr %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "git ls-files") {
				t.Fatalf("failure diagnostic omits operation: %q", stderr.String())
			}
		})
	}
}

func newGitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	globalConfig := filepath.Join(t.TempDir(), "empty.gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			// Setenv registers cleanup before we remove the inherited setting.
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Chdir(root)
	runGitFixture(t, "init", "-q")
	return root
}

func runGitFixture(t *testing.T, arguments ...string) {
	t.Helper()
	output, err := exec.Command("git", arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %q failed: %v\n%s", arguments, err, output)
	}
}

func TestRunCheckSequence(t *testing.T) {
	root := makeCheckRoot(t, "main.go")
	for _, test := range []struct {
		name     string
		skipRace bool
		want     []string
	}{
		{"default", false, []string{"git", "gofmt", "go vet ./...", "go test ./...", "go test -race ./...", "go build ./..."}},
		{"skip race", true, []string{"git", "gofmt", "go vet ./...", "go test ./...", "go build ./..."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			var stderr bytes.Buffer
			runner := func(executable string, arguments []string, _ io.Writer) error {
				if executable == "go" {
					executable += " " + strings.Join(arguments, " ")
				}
				calls = append(calls, executable)
				return nil
			}
			if code := runCheck(root, checkOptions{skipRace: test.skipRace}, io.Discard, &stderr, runner); code != 0 {
				t.Fatalf("runCheck exit = %d, stderr %q", code, stderr.String())
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("commands = %q, want %q", calls, test.want)
			}
		})
	}
}

func TestRunCheckStopsAtFirstFailureAtEveryStage(t *testing.T) {
	root := makeCheckRoot(t, "main.go")
	stages := []struct {
		name      string
		operation string
	}{
		{name: "Git file gate", operation: "git ls-files"},
		{name: "gofmt", operation: "gofmt -l"},
		{name: "vet", operation: "go vet ./..."},
		{name: "test", operation: "go test ./..."},
		{name: "race", operation: "go test -race ./..."},
		{name: "build", operation: "go build ./..."},
	}
	for failAt, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			var calls []recordedCommand
			var stdout, stderr bytes.Buffer
			runner := func(executable string, arguments []string, _ io.Writer) error {
				calls = append(calls, recordedCommand{executable: executable, arguments: append([]string(nil), arguments...)})
				if len(calls)-1 == failAt {
					return errors.New("injected failure")
				}
				return nil
			}
			if code := runCheck(root, checkOptions{}, &stdout, &stderr, runner); code != 1 {
				t.Fatalf("runCheck exit = %d, want 1; stderr %q", code, stderr.String())
			}
			if len(calls) != failAt+1 {
				t.Fatalf("commands after %s failure: %#v", stage.name, calls)
			}
			if !strings.Contains(stderr.String(), stage.operation) {
				t.Fatalf("failure diagnostic %q omits %q", stderr.String(), stage.operation)
			}
		})
	}
}

func TestRunCheckNoJSOnlyRunsOnlyGit(t *testing.T) {
	for _, test := range []struct {
		name       string
		gitOutput  string
		wantStatus int
	}{
		{name: "empty gate passes", wantStatus: 0},
		{name: "rejected file stops", gitOutput: "blocked.js\x00", wantStatus: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := makeCheckRoot(t, "main.go")
			var calls []recordedCommand
			var stdout, stderr bytes.Buffer
			runner := func(executable string, arguments []string, output io.Writer) error {
				calls = append(calls, recordedCommand{executable: executable, arguments: append([]string(nil), arguments...)})
				_, err := io.WriteString(output, test.gitOutput)
				return err
			}
			options := checkOptions{noJSOnly: true, skipRace: true}
			if code := runCheck(root, options, &stdout, &stderr, runner); code != test.wantStatus {
				t.Fatalf("runCheck exit = %d, want %d; stderr %q", code, test.wantStatus, stderr.String())
			}
			if len(calls) != 1 || calls[0].executable != "git" {
				t.Fatalf("partial check ran extra commands: %#v", calls)
			}
		})
	}
}

func TestRunCheckRejectsUnformattedFileWithoutWritingIt(t *testing.T) {
	root := makeCheckRoot(t)
	t.Chdir(root)
	// Both the leading dash and the space are part of a single filename.
	const name = "-source file.go"
	before := []byte("package fixture\nvar x=1\n")
	if err := os.WriteFile(name, before, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	runner := func(executable string, arguments []string, output io.Writer) error {
		switch executable {
		case "git":
			return nil
		case "gofmt":
			return runProcess(executable, arguments, output)
		default:
			t.Fatalf("ran %q after formatting failure", executable)
			return nil
		}
	}
	if code := runCheck(root, checkOptions{}, io.Discard, &stderr, runner); code != 1 || !strings.Contains(stderr.String(), name) {
		t.Fatalf("expected an unformatted-file failure for %q, got exit %d, stderr %q", name, code, stderr.String())
	}
	after, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("checker modified source: before %q, after %q", before, after)
	}
}

func TestRunCheckGofmtErrorFailsWithEmptyOutput(t *testing.T) {
	root := makeCheckRoot(t, "main.go")
	var calls int
	var stdout, stderr bytes.Buffer
	runner := func(executable string, _ []string, _ io.Writer) error {
		calls++
		if executable == "gofmt" {
			return errors.New("gofmt could not start")
		}
		return nil
	}
	if code := runCheck(root, checkOptions{}, &stdout, &stderr, runner); code != 1 {
		t.Fatalf("runCheck exit = %d, want 1", code)
	}
	if calls != 2 || !strings.Contains(stderr.String(), "gofmt -l failed") {
		t.Fatalf("empty-output gofmt failure was not reported: calls %d, stderr %q", calls, stderr.String())
	}
}

func TestRunCheckFailsWhenRootFileEnumerationFails(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	var calls int
	var stdout, stderr bytes.Buffer
	runner := func(string, []string, io.Writer) error {
		calls++
		return nil
	}
	if code := runCheck(missingRoot, checkOptions{}, &stdout, &stderr, runner); code != 1 {
		t.Fatalf("runCheck exit = %d, want 1", code)
	}
	if calls != 1 || !strings.Contains(stderr.String(), "enumerate root Go files failed") {
		t.Fatalf("enumeration failure was not reported before Go checks: calls %d, stderr %q", calls, stderr.String())
	}
}

type recordedCommand struct {
	executable string
	arguments  []string
}

func makeCheckRoot(t *testing.T, rootGoFiles ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"cmd", "internal", "docs"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range rootGoFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
