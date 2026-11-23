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
	root, globalConfig := newGitFixture(t)
	restoreDirectory := changeDirectory(t, root)
	defer restoreDirectory()

	for index, name := range []string{
		"blocked.JS",
		"nested/blocked.MjS",
		"blocked.CJS",
		"nested/blocked.JSX",
		"blocked.TS",
		"nested/blocked.MTS",
		"blocked.CTS",
		"nested/space path/blocked.TsX",
		"PACKAGE.JSON",
	} {
		if index%2 == 1 {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(root, filepath.FromSlash(name))
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
	runGitFixture(t, root, globalConfig, "add", "--", "tracked.js")
	var stderr bytes.Buffer
	if code := checkForbiddenFiles(runProcess, &stderr); code != 1 || !strings.Contains(stderr.String(), `"tracked.js"`) {
		t.Fatalf("file gate did not reject tracked file: exit %d, stderr %q", code, stderr.String())
	}
	runGitFixture(t, root, globalConfig, "rm", "--cached", "--", "tracked.js")
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
	stderr.Reset()
	if code := checkForbiddenFiles(runProcess, &stderr); code != 0 {
		t.Fatalf("file gate rejected ignored files: exit %d, stderr %q", code, stderr.String())
	}

	if err := os.WriteFile(filepath.Join(root, "ignored-dir", "forced.TsX"), []byte("forced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, globalConfig, "add", "-f", "--", "ignored-dir/forced.TsX")
	stderr.Reset()
	if code := checkForbiddenFiles(runProcess, &stderr); code != 1 || !strings.Contains(stderr.String(), `"ignored-dir/forced.TsX"`) {
		t.Fatalf("file gate did not reject force-tracked ignored file: exit %d, stderr %q", code, stderr.String())
	}
}

func TestGitFileGateUsesNULRecordsAndExactPathspecs(t *testing.T) {
	var stderr bytes.Buffer
	var calls int
	runner := func(executable string, arguments []string, stdout io.Writer) error {
		calls++
		if executable != "git" {
			t.Fatalf("executable = %q, want git", executable)
		}
		want := append([]string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"}, forbiddenPathspecs...)
		if !reflect.DeepEqual(arguments, want) {
			t.Fatalf("arguments = %#v, want %#v", arguments, want)
		}
		_, err := io.WriteString(stdout, "line\nbreak.TS\x00")
		return err
	}
	if code := checkForbiddenFiles(runner, &stderr); code != 1 {
		t.Fatalf("file gate exit = %d, want 1", code)
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
	if !strings.Contains(stderr.String(), strconv.Quote("line\nbreak.TS")) {
		t.Fatalf("newline-containing filename is not quoted: %q", stderr.String())
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

func newGitFixture(t *testing.T) (root, globalConfig string) {
	t.Helper()
	root = t.TempDir()
	globalConfig = filepath.Join(t.TempDir(), "empty.gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setFixtureGitEnvironment(t, globalConfig)
	runGitFixture(t, root, globalConfig, "init", "-q")
	return root, globalConfig
}

func setFixtureGitEnvironment(t *testing.T, globalConfig string) {
	t.Helper()
	var previous []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			previous = append(previous, entry)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
				os.Unsetenv(key)
			}
		}
		for _, entry := range previous {
			key, value, _ := strings.Cut(entry, "=")
			os.Setenv(key, value)
		}
	})
	if err := os.Setenv("GIT_CONFIG_GLOBAL", globalConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GIT_CONFIG_NOSYSTEM", "1"); err != nil {
		t.Fatal(err)
	}
}

func runGitFixture(t *testing.T, root, globalConfig string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	command.Env = fixtureGitEnvironment(globalConfig)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q failed: %v\n%s", arguments, err, output)
	}
}

func fixtureGitEnvironment(globalConfig string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			environment = append(environment, entry)
		}
	}
	return append(environment,
		"GIT_CONFIG_GLOBAL="+globalConfig,
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

func TestRunCheckUsesDefaultSequenceAndRootGoArguments(t *testing.T) {
	root := makeCheckRoot(t, "source file.go", "main.go", "-leading.go")
	var calls []recordedCommand
	var stdout, stderr bytes.Buffer
	runner := func(executable string, arguments []string, _ io.Writer) error {
		calls = append(calls, recordedCommand{executable: executable, arguments: append([]string(nil), arguments...)})
		return nil
	}

	if code := runCheck(root, checkOptions{}, &stdout, &stderr, runner); code != 0 {
		t.Fatalf("runCheck exit = %d, stderr %q", code, stderr.String())
	}
	want := []recordedCommand{
		{executable: "git", arguments: append([]string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"}, forbiddenPathspecs...)},
		{executable: "gofmt", arguments: []string{"-l", "./-leading.go", "./main.go", "./source file.go", "cmd", "internal", "docs"}},
		{executable: "go", arguments: []string{"vet", "./..."}},
		{executable: "go", arguments: []string{"test", "./..."}},
		{executable: "go", arguments: []string{"test", "-race", "./..."}},
		{executable: "go", arguments: []string{"build", "./..."}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %#v, want %#v", calls, want)
	}
	for _, label := range []string{"Checking gofmt...", "Checking go vet...", "Checking go test...", "Checking go test -race...", "Checking go build..."} {
		if !strings.Contains(stdout.String(), label) {
			t.Errorf("stdout %q does not contain step label %q", stdout.String(), label)
		}
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

func TestRunCheckSkipRaceOmitsOnlyRaceCommand(t *testing.T) {
	root := makeCheckRoot(t, "main.go")
	var calls []recordedCommand
	var stdout, stderr bytes.Buffer
	runner := func(executable string, arguments []string, _ io.Writer) error {
		calls = append(calls, recordedCommand{executable: executable, arguments: append([]string(nil), arguments...)})
		return nil
	}
	if code := runCheck(root, checkOptions{skipRace: true}, &stdout, &stderr, runner); code != 0 {
		t.Fatalf("runCheck exit = %d, stderr %q", code, stderr.String())
	}
	want := []recordedCommand{
		{executable: "git", arguments: append([]string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"}, forbiddenPathspecs...)},
		{executable: "gofmt", arguments: []string{"-l", "./main.go", "cmd", "internal", "docs"}},
		{executable: "go", arguments: []string{"vet", "./..."}},
		{executable: "go", arguments: []string{"test", "./..."}},
		{executable: "go", arguments: []string{"build", "./..."}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("skip-race commands = %#v, want %#v", calls, want)
	}
	if !strings.Contains(stdout.String(), "Skipping race tests: --skip-race was provided.") {
		t.Fatalf("stdout does not explain skipped race check: %q", stdout.String())
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

func TestRunCheckRejectsGofmtOutputWithoutWritingFiles(t *testing.T) {
	root := makeCheckRoot(t, "main.go")
	mainFile := filepath.Join(root, "main.go")
	before, err := os.ReadFile(mainFile)
	if err != nil {
		t.Fatal(err)
	}
	var calls []recordedCommand
	var stdout, stderr bytes.Buffer
	runner := func(executable string, arguments []string, output io.Writer) error {
		calls = append(calls, recordedCommand{executable: executable, arguments: append([]string(nil), arguments...)})
		if executable == "gofmt" {
			_, err := io.WriteString(output, "./main.go\n")
			return err
		}
		return nil
	}
	if code := runCheck(root, checkOptions{}, &stdout, &stderr, runner); code != 1 {
		t.Fatalf("runCheck exit = %d, want 1", code)
	}
	if len(calls) != 2 || !strings.Contains(stderr.String(), "./main.go") {
		t.Fatalf("gofmt output was not rejected immediately: commands %#v, stderr %q", calls, stderr.String())
	}
	after, err := os.ReadFile(mainFile)
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

func changeDirectory(t *testing.T, directory string) func() {
	t.Helper()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}
}
