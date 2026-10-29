package terminal

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"

	pitheme "psi/internal/theme"
)

func markdownHeadingTheme(t *testing.T, color string) *pitheme.Theme {
	t.Helper()
	data, err := pitheme.BuiltinJSON("dark")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc["colors"].(map[string]any)["text"] = color
	data, err = json.Marshal(doc)
	if err != nil {
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
	return palette
}

func TestThemeChangeRendersOnlyActiveMarkdownTail(t *testing.T) {
	oldTheme := markdownHeadingTheme(t, "#123456")
	newTheme := markdownHeadingTheme(t, "#abcdef")
	oldHeading, err := oldTheme.ForegroundANSI("text")
	if err != nil {
		t.Fatal(err)
	}
	newHeading, err := newTheme.ForegroundANSI("text")
	if err != nil {
		t.Fatal(err)
	}
	if oldHeading == newHeading {
		t.Fatalf("fixture headings quantized to the same color %q", oldHeading)
	}
	stream := NewMarkdownStream(64, false)
	stream.SetTheme(oldTheme)
	stable, _ := stream.Feed("committed **heading**\n\n")
	committed := append([]string(nil), stable...)
	stream.Feed("active **heading**")
	oldTail := append([]string(nil), stream.DisplayTail()...)
	if !strings.Contains(strings.Join(oldTail, "\n"), oldHeading) {
		t.Fatalf("active Markdown did not use initial theme: %q", oldTail)
	}
	stream.SetTheme(newTheme)
	newTail := stream.DisplayTail()
	if !strings.Contains(strings.Join(newTail, "\n"), newHeading) || strings.Contains(strings.Join(newTail, "\n"), oldHeading) {
		t.Fatalf("active Markdown did not use reloaded theme: old=%q new=%q", oldTail, newTail)
	}
	if !reflect.DeepEqual(committed, stable) || strings.Contains(strings.Join(committed, "\n"), newHeading) {
		t.Fatalf("theme change rewrote committed scrollback: before=%q after=%q", committed, stable)
	}
	if got, want := strings.Join(newTail, "\n"), strings.Join(oldTail, "\n"); strings.ReplaceAll(got, newHeading, "<text>") != strings.ReplaceAll(want, oldHeading, "<text>") {
		t.Fatalf("active source changed during theme refresh: old=%q new=%q", oldTail, newTail)
	}
}

func TestOneMiBThemeChangeReformatsOnlyBoundedActiveBlock(t *testing.T) {
	oldTheme := markdownHeadingTheme(t, "#123456")
	newTheme := markdownHeadingTheme(t, "#abcdef")
	oldText, _ := oldTheme.ForegroundANSI("text")
	newText, _ := newTheme.ForegroundANSI("text")
	source := strings.Repeat("committed paragraph text.\n\n", 40_000) + "active **mutable tail**"
	if len(source) < 1<<20 {
		t.Fatalf("fixture is only %d bytes", len(source))
	}
	stream := NewMarkdownStream(80, false)
	stream.SetTheme(oldTheme)
	stable, _ := stream.Feed(source)
	if len(stream.pending) > maxMarkdownActiveSource || len(stable) == 0 {
		t.Fatalf("large response was not split into stable and bounded active regions: stable=%d active=%d", len(stable), len(stream.pending))
	}
	committedHash := func(rows []string) [32]byte {
		h := sha256.New()
		for _, row := range rows {
			h.Write([]byte(row))
			h.Write([]byte{0})
		}
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		return sum
	}
	before := committedHash(stable)
	runtime.GC()
	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	stream.SetTheme(newTheme)
	runtime.ReadMemStats(&memAfter)
	allocated := memAfter.TotalAlloc - memBefore.TotalAlloc
	if allocated > 2*1024*1024 {
		t.Fatalf("theme change allocated %d bytes for a %d-byte response", allocated, len(source))
	}
	active := stream.DisplayTail()
	if !strings.Contains(strings.Join(active, "\n"), newText) || strings.Contains(strings.Join(active, "\n"), oldText) {
		t.Fatalf("active tail did not use the replacement palette: %q", active)
	}
	if got := committedHash(stable); got != before {
		t.Fatal("theme change mutated committed rows from the 1 MiB response")
	}
	t.Logf("1 MiB theme refresh re-rendered %d active bytes and allocated %d bytes", len(stream.pending), allocated)
}
