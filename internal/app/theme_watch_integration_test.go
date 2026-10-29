//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pitheme "psi/internal/theme"
)

func writeThemeRole(t *testing.T, path, role, color string, atomic bool) string {
	t.Helper()
	data, err := pitheme.BuiltinJSON("dark")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc["name"] = "selected"
	doc["colors"].(map[string]any)[role] = color
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if atomic {
		temp := path + ".tmp"
		if err := os.WriteFile(temp, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, path); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := pitheme.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	palette, err := parsed.Compile(pitheme.ColorMode256)
	if err != nil {
		t.Fatal(err)
	}
	ansi, err := palette.ForegroundANSI(role)
	if err != nil {
		t.Fatal(err)
	}
	return ansi
}

func writeAccentTheme(t *testing.T, path, accent string, atomic bool) string {
	return writeThemeRole(t, path, "accent", accent, atomic)
}

func TestUserThemeAtomicSaveReloadsOpenCompletionWithoutLosingQuery(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	themePath := filepath.Join(themeDir, "selected.json")
	oldAccent := writeAccentTheme(t, themePath, "#123456", false)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session", "--use-theme", "selected")
	q.prompt()
	q.write("/")
	q.prompt()
	if !strings.Contains(q.raw.String(), oldAccent) {
		t.Fatalf("initial completion did not use selected accent %q", oldAccent)
	}
	newAccent := writeAccentTheme(t, themePath, "#abcdef", true)
	if newAccent == oldAccent {
		t.Fatalf("fixture accents quantized to the same terminal color %q", oldAccent)
	}
	q.wait(newAccent)
	q.prompt()
	if !strings.Contains(q.screen.Text(), "/") {
		t.Fatalf("theme reload lost the completion query: %q", q.screen.Text())
	}
	if !strings.Contains(q.raw.String(), oldAccent) || !strings.Contains(q.raw.String(), newAccent) {
		t.Fatalf("atomic save did not replace the live completion style: old=%q new=%q", oldAccent, newAccent)
	}
	q.write("\x15")
	q.prompt()
	thirdAccent := writeAccentTheme(t, themePath, "#ffcc00", true)
	q.write("/reload\r")
	q.wait("theme reloaded: selected")
	q.prompt()
	q.write("/")
	q.prompt()
	if !strings.Contains(q.raw.String(), thirdAccent) {
		t.Fatalf("explicit /reload did not install the updated palette %q", thirdAccent)
	}
	q.write("\x15/quit\r")
	q.finish()
}

func TestThemeReloadDuringStreamRestylesOnlyMutableTail(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	themePath := filepath.Join(themeDir, "selected.json")
	oldText := writeThemeRole(t, themePath, "text", "#123456", false)
	newText := writeThemeRole(t, filepath.Join(t.TempDir(), "expected.json"), "text", "#abcdef", false)
	if oldText == newText {
		t.Fatalf("fixture text colors quantized to the same terminal color %q", oldText)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"theme-stream\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"COMMITTED_SENTINEL\\n\\nACTIVE_PARTIAL\"}}\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"_STREAM_TAIL_DONE\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer func() {
		unblock()
		server.Close()
	}()
	reviewSettings(t, agentDir, server.URL)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session", "--use-theme", "selected")
	q.prompt()
	q.write("stream theme change\r")
	q.wait("ACTIVE_PARTIAL")
	beforeRows := strings.Split(q.screen.Text(), "\n")
	var sentinelBefore string
	for _, row := range beforeRows {
		if strings.Contains(row, "COMMITTED_SENTINEL") {
			sentinelBefore = row
			break
		}
	}
	if sentinelBefore == "" || !strings.Contains(q.raw.String(), oldText) {
		t.Fatalf("stream did not render its committed and active regions with the initial theme: %q", q.screen.Text())
	}
	writeThemeRole(t, themePath, "text", "#abcdef", true)
	q.wait(newText)
	var sentinelAfter string
	for _, row := range strings.Split(q.screen.Text(), "\n") {
		if strings.Contains(row, "COMMITTED_SENTINEL") {
			sentinelAfter = row
			break
		}
	}
	if sentinelAfter != sentinelBefore {
		t.Fatalf("theme reload repainted committed scrollback: before=%q after=%q", sentinelBefore, sentinelAfter)
	}
	unblock()
	q.wait("ACTIVE_PARTIAL_STREAM_TAIL_DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()
}

func TestInvalidThemeWatchKeepsLastGoodAndWarnsOnce(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	themePath := filepath.Join(themeDir, "selected.json")
	accent := writeAccentTheme(t, themePath, "#123456", false)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session", "--use-theme", "selected")
	q.prompt()
	q.write("/")
	q.prompt()
	invalidSave := func() {
		t.Helper()
		temp := themePath + ".tmp"
		if err := os.WriteFile(temp, []byte(`{"name":"selected","colors":{"accent":"invalid"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, themePath); err != nil {
			t.Fatal(err)
		}
	}
	invalidSave()
	q.wait("Theme reload failed")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "/") || !strings.Contains(q.raw.String(), accent) {
		t.Fatalf("invalid theme reload lost focus/query or last-good style: screen=%q", q.screen.Text())
	}
	invalidSave()
	time.Sleep(250 * time.Millisecond)
	q.prompt()
	if count := strings.Count(q.raw.String(), "Theme reload failed"); count != 1 {
		t.Fatalf("repeated invalid saves produced %d warning notices", count)
	}
	q.write("\x15/quit\r")
	q.finish()
}

func TestUserThemeReloadPreservesOpenModelPickerQuery(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	themePath := filepath.Join(themeDir, "selected.json")
	writeAccentTheme(t, themePath, "#123456", false)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session", "--use-theme", "selected")
	q.prompt()
	q.write("/model\r")
	q.wait("Models")
	q.write("anth")
	q.prompt()
	if !strings.Contains(q.screen.Text(), "anth") {
		t.Fatalf("model picker did not show its filter query: %q", q.screen.Text())
	}
	cursorRow, cursorCol := q.screen.CursorRow, q.screen.CursorCol
	accent := writeAccentTheme(t, themePath, "#abcdef", true)
	q.wait(accent)
	q.prompt()
	screen := q.screen.Text()
	if !strings.Contains(screen, "Models") || !strings.Contains(screen, "anth") || !strings.Contains(screen, "→ ✓ m [anthropic]") {
		t.Fatalf("atomic theme reload changed the open picker query or selection: %q", screen)
	}
	if q.screen.CursorRow != cursorRow || q.screen.CursorCol != cursorCol {
		t.Fatalf("atomic theme reload moved the picker cursor: before=(%d,%d) after=(%d,%d)", cursorRow, cursorCol, q.screen.CursorRow, q.screen.CursorCol)
	}
	q.write("\x03")
	q.prompt()
	q.write("\x15/quit\r")
	q.finish()
}
