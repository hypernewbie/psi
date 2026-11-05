package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func updateSettings(t *testing.T, path string, pathParts []string, value string) error {
	t.Helper()
	return UpdateUserSettings(path, []SettingsUpdate{{Path: pathParts, Value: json.RawMessage(value)}})
}

func writeSettingsFileForTest(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// requireSettingsMode checks POSIX permission bits. Windows reports 0666 for
// every file and ignores chmod, so the bits carry no meaning there.
func requireSettingsMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode = %o, want %o", info.Mode().Perm(), want)
	}
}

func TestUpdateUserSettingsPreservesUnknownKeysAndOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, "{\n  \"$schema\": \"https://example/schema.json\",\n  \"unknown\": {\"keep\": true},\n  \"outputPad\": 1\n}\n", 0o644)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := updateSettings(t, path, []string{"outputPad"}, "0"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "{\n  \"$schema\"") {
		t.Fatalf("top-level order not preserved:\n%s", text)
	}
	if !strings.Contains(text, "\"unknown\": {") || !strings.Contains(text, "\"keep\": true") {
		t.Fatalf("unknown key dropped:\n%s", text)
	}
	if !strings.Contains(text, "\"outputPad\": 0") {
		t.Fatalf("value not updated:\n%s", text)
	}
	if strings.HasSuffix(text, "\n") {
		t.Fatalf("unexpected trailing newline:\n%q", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("mode changed: got %o, want %o", info.Mode().Perm(), before.Mode().Perm())
	}
}

func TestUpdateUserSettingsNestedMergeKeepsSiblings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, `{"terminal":{"other":true,"trueColor":"auto"},"theme":"dark"}`, 0o600)

	if err := updateSettings(t, path, []string{"terminal", "trueColor"}, `"false"`); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Terminal map[string]json.RawMessage `json:"terminal"`
		Theme    string                     `json:"theme"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.Terminal["trueColor"]) != `"false"` {
		t.Fatalf("trueColor = %s", decoded.Terminal["trueColor"])
	}
	if string(decoded.Terminal["other"]) != "true" {
		t.Fatalf("sibling lost: %s", decoded.Terminal["other"])
	}
	if decoded.Theme != "dark" {
		t.Fatalf("theme = %q", decoded.Theme)
	}
	if strings.Index(string(data), `"other"`) > strings.Index(string(data), `"trueColor"`) {
		t.Fatalf("nested order not preserved:\n%s", data)
	}
}

func TestUpdateUserSettingsNoOpDoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, `{"outputPad":0}`, 0o600)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := updateSettings(t, path, []string{"outputPad"}, "0"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("no-op update rewrote the file")
	}
}

func TestUpdateUserSettingsRefusesMalformedJson(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, `{"outputPad":`, 0o600)
	err := updateSettings(t, path, []string{"outputPad"}, "0")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != `{"outputPad":` {
		t.Fatalf("malformed file changed: %q", data)
	}
}

func TestUpdateUserSettingsCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	if err := updateSettings(t, path, []string{"theme"}, `"light"`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"theme\": \"light\"\n}" {
		t.Fatalf("created file = %q", data)
	}
	requireSettingsMode(t, path, 0o600)
}

func TestUpdateUserSettingsFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSettingsFileForTest(t, target, `{"theme":"dark"}`, 0o600)
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := updateSettings(t, link, []string{"theme"}, `"light"`); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("settings.json is no longer a symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"light"`) {
		t.Fatalf("target not updated: %s", data)
	}
}

func TestUpdateUserSettingsNullParentBecomesObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, `{"terminal":null}`, 0o600)
	if err := updateSettings(t, path, []string{"terminal", "trueColor"}, "true"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"terminal": {`) || !strings.Contains(string(data), `"trueColor": true`) {
		t.Fatalf("null parent not replaced:\n%s", data)
	}
}

func TestUpdateUserSettingsStripsBOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, "\xef\xbb\xbf{\"theme\":\"dark\"}", 0o600)
	if err := updateSettings(t, path, []string{"theme"}, `"light"`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\xef\xbb\xbf") {
		t.Fatal("BOM kept")
	}
}

func TestUpdateUserSettingsRejectsBadInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := UpdateUserSettings(path, []SettingsUpdate{{Path: nil, Value: json.RawMessage("1")}}); err == nil {
		t.Fatal("empty path accepted")
	}
	if err := UpdateUserSettings(path, []SettingsUpdate{{Path: []string{"a", "b", "c"}, Value: json.RawMessage("1")}}); err == nil {
		t.Fatal("three-part path accepted")
	}
	if err := UpdateUserSettings(path, []SettingsUpdate{{Path: []string{"a"}, Value: json.RawMessage("{")}}); err == nil {
		t.Fatal("invalid value accepted")
	}
}

func TestUpdateUserSettingsLockConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeSettingsFileForTest(t, path, `{"theme":"dark"}`, 0o600)
	release, err := lockSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = updateSettings(t, path, []string{"theme"}, `"light"`)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("error = %v, want lock conflict", err)
	}
}

func TestUpdateUserSettingsKeepsTopLevelOrderWhenAppending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, `{"z":1,"a":2}`, 0o600)
	if err := UpdateUserSettings(path, []SettingsUpdate{
		{Path: []string{"z"}, Value: json.RawMessage("3")},
		{Path: []string{"new"}, Value: json.RawMessage("4")},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Index(text, `"z"`) > strings.Index(text, `"a"`) || strings.Index(text, `"a"`) > strings.Index(text, `"new"`) {
		t.Fatalf("order = %s", text)
	}
}

func TestUpdateUserSettingsCreatesNestedParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := updateSettings(t, path, []string{"markdown", "codeBlockIndent"}, `"    "`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Markdown map[string]string `json:"markdown"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Markdown["codeBlockIndent"] != "    " {
		t.Fatalf("markdown = %v\n%s", decoded.Markdown, data)
	}
}

func TestUpdateUserSettingsRefusesNonObjectParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsFileForTest(t, path, `{"terminal":"text"}`, 0o600)
	if err := updateSettings(t, path, []string{"terminal", "trueColor"}, "true"); err == nil {
		t.Fatal("non-object parent accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"terminal":"text"}` {
		t.Fatalf("file changed: %q", data)
	}
}
