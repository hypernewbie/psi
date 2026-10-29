package app

import (
	"strings"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
	pitheme "psi/internal/theme"
)

func TestReplayHonorsNoColorAndUsesCompactMetadataRole(t *testing.T) {
	document, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	dark, err := document.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	items := []session.TranscriptItem{{Kind: session.KindCompaction, Text: "Context compacted · ~28000 tokens before"}}
	var plain []string
	if !renderTimelineAtWidthWithDisplayAndColor(func(row string) bool { plain = append(plain, row); return true }, items, 60, terminal.DefaultDisplaySettings(), true, dark) {
		t.Fatal("replay stopped unexpectedly")
	}
	if len(plain) != 2 || plain[0] == "" || plain[1] != "" || strings.Contains(strings.Join(plain, "\n"), "\x1b[") {
		t.Fatalf("no-color metadata replay=%q", plain)
	}
	if !strings.Contains(plain[0], "[compaction]") || !strings.Contains(plain[0], "Context compacted") {
		t.Fatalf("compact metadata label missing: %q", plain[0])
	}
	var styled []string
	renderTimelineAtWidthWithDisplayAndColor(func(row string) bool { styled = append(styled, row); return true }, items, 60, terminal.DefaultDisplaySettings(), false, dark)
	if len(styled) == 0 || !strings.Contains(styled[0], "\x1b[") {
		t.Fatalf("replay stripped trusted theme styles: %q", styled)
	}
}
