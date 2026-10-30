package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"psi/internal/config"
	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/testutil/leakcheck"
	pitheme "psi/internal/theme"
)

func TestThemeStateReloadIsTransactionalAndRetainsLastGoodSnapshot(t *testing.T) {
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"theme":"selected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(themeDir, "selected.json")
	writeTheme := func(color string) []byte {
		t.Helper()
		data, err := pitheme.BuiltinJSON("dark")
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		document["name"] = "selected"
		document["colors"].(map[string]any)["accent"] = color
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	if err := os.WriteFile(path, writeTheme("#123456"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := session.New(session.OpenOptions{Cwd: root, SessionDir: filepath.Join(root, "sessions"), AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Unlock()
	mode := pitheme.ColorMode256
	initial, name, fallback := resolveThemeSnapshot(pitheme.LoadCatalog(themeCatalogOptions(config.Settings{Theme: "selected"}, Options{}, agentDir, root, false, mode)), "selected", "dark", pitheme.TerminalColors{}, 0, mode)
	if initial == nil || fallback != "" || name != "selected" {
		t.Fatalf("initial theme=%v name=%q fallback=%q", initial, name, fallback)
	}
	r := &runtime{store: store, agentDir: agentDir, theme: initial, themeMode: mode, themeAppearance: "dark"}
	if err := r.reloadTheme(); err != nil {
		t.Fatal(err)
	}
	good := r.theme
	if good == initial || good.Name != "selected" {
		t.Fatalf("successful reload did not install a resolved snapshot: %p %p %q", initial, good, good.Name)
	}
	if err := os.WriteFile(path, []byte(`{"name":"selected","colors":{"accent":"not-a-color"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadTheme(); err == nil {
		t.Fatal("invalid theme reload succeeded")
	}
	if r.theme != good {
		t.Fatal("failed reload replaced the last good immutable theme")
	}
}

func TestThemeStateTerminalReplies(t *testing.T) {
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})
	r := testRuntime(t)

	// 1. handleTerminalReply with nil collector returns false
	r.themeColorCollector = nil
	if r.handleTerminalReply(nil, []byte("\x1b]11;rgb:0000/0000/0000\x07")) {
		t.Error("nil collector should not handle reply")
	}

	// Activate collector
	r.themeColorCollector = newTerminalColorCollector(pitheme.TerminalColors{})

	// 2. Initial background OSC reply
	bgReply := []byte("\x1b]11;rgb:1111/2222/3333\x07")
	if !r.handleTerminalReply(nil, bgReply) {
		t.Fatal("first background reply should be accepted")
	}
	if !r.themeColorsReceived {
		t.Error("themeColorsReceived should be true")
	}
	if r.themeColors.Background == nil {
		t.Fatal("expected background color to be set")
	}
	bgRGB := *r.themeColors.Background
	if bgRGB.R != 0x11 || bgRGB.G != 0x22 || bgRGB.B != 0x33 {
		t.Errorf("unexpected background color: %+v", bgRGB)
	}

	// 3. Duplicate background reply should be ignored
	if r.handleTerminalReply(nil, bgReply) {
		t.Error("duplicate background reply should return false")
	}

	// 4. Interleaved user keystrokes during collector lifecycle
	userInputs := [][]byte{
		[]byte("plain user keystroke"),
		[]byte("\r"),
		[]byte("\x1b[A"),
		[]byte("\x03"),
		[]byte("\x04"),
		[]byte("\x1b[?25h"),
	}
	for _, input := range userInputs {
		if r.handleTerminalReply(nil, input) {
			t.Errorf("user input %q should not be handled as terminal reply", input)
		}
	}
	// Verify background is untouched
	if *r.themeColors.Background != bgRGB {
		t.Error("user keystrokes corrupted theme colors")
	}

	// 5. Foreground OSC reply
	fgReply := []byte("\x1b]10;rgb:ffff/eeee/dddd\x07")
	if !r.handleTerminalReply(nil, fgReply) {
		t.Fatal("first foreground reply should be accepted")
	}
	if r.themeColors.Foreground == nil {
		t.Fatal("expected foreground color to be set")
	}
	fgRGB := *r.themeColors.Foreground
	if fgRGB.R != 0xff || fgRGB.G != 0xee || fgRGB.B != 0xdd {
		t.Errorf("unexpected foreground color: %+v", fgRGB)
	}

	// Duplicate foreground reply
	if r.handleTerminalReply(nil, fgReply) {
		t.Error("duplicate foreground reply should return false")
	}

	// 6. Device Attributes (DA) terminator completes color collection
	daReply := []byte("\x1b[?1;2c")
	if r.handleTerminalReply(nil, daReply) {
		t.Error("DA reply should not trigger theme update")
	}
	if !r.themeColorCollector.finished {
		t.Fatal("DA reply should mark collector finished")
	}

	// 7. Late replies after collector is complete should be ignored
	lateReply := []byte("\x1b]4;0;rgb:0000/1111/2222\x07")
	if r.handleTerminalReply(nil, lateReply) {
		t.Error("late reply after completion should return false")
	}

	// 8. Terminal color scheme reports (appearance switch notifications)
	darkReport := []byte("\x1b[?997;1n")
	lightReport := []byte("\x1b[?997;2n")

	// Switch to dark
	r.reportedColorScheme = "light"
	if !r.handleTerminalReply(nil, darkReport) {
		t.Fatal("scheme change to dark should be handled")
	}
	if r.reportedColorScheme != "dark" {
		t.Errorf("reportedColorScheme = %q, want dark", r.reportedColorScheme)
	}

	// Duplicate dark notification
	if r.handleTerminalReply(nil, darkReport) {
		t.Error("duplicate color scheme report should return false")
	}

	// Switch to light
	if !r.handleTerminalReply(nil, lightReport) {
		t.Fatal("scheme change to light should be handled")
	}
	if r.reportedColorScheme != "light" {
		t.Errorf("reportedColorScheme = %q, want light", r.reportedColorScheme)
	}
}

func TestThemeStateQueryHoldsInterleavedUserKeystrokes(t *testing.T) {
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})
	r := testRuntime(t)
	inputPipe, outputPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputPipe.Close()
	defer outputPipe.Close()

	console := terminal.NewConsole(inputPipe, io.Discard)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	defer console.Close()

	// Write user keystrokes into the console pipe
	if _, err := outputPipe.Write([]byte("user-typed-query")); err != nil {
		t.Fatal(err)
	}

	// Query terminal colors with short timeout
	r.queryTerminalColors(console, 50*time.Millisecond)

	// Verify the user keystroke was held and not discarded
	held := console.TakeHeldInputs()
	if len(held) == 0 {
		t.Fatal("expected user keystroke to be held during color query")
	}
	var text strings.Builder
	for _, h := range held {
		text.Write(h.Key)
	}
	if text.String() != "user-typed-query" {
		t.Fatalf("held text = %q, want 'user-typed-query'", text.String())
	}
}
