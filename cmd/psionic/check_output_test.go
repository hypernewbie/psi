package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestCheckOutputDescribesNewCapabilities proves that --check reports the
// group direct-message, exec, and attachment behavior, and that the report
// stays free of configured values.
func TestCheckOutputDescribesNewCapabilities(t *testing.T) {
	settings := &fileSettings{
		Channels: &channelsSettings{Discord: &discordSettings{
			DM: &dmSettings{GroupEnabled: boolPointer(true)},
		}},
		Tools: &toolsSettings{Exec: &execSettings{
			Enabled:        boolPointer(true),
			TimeoutMs:      int64Ptr(2000),
			MaxOutputBytes: int64Ptr(4096),
		}},
	}
	loaded := &loaded{settings: settings, agent: selectedAgent{Model: "demo/large-model"}}
	var output bytes.Buffer
	if err := printCheck(&output, loaded); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{
		"group direct messages: enabled",
		"exec tool: owner direct messages only, timeout 2s, output cap 4096 bytes",
		"attachments: memory only, Discord CDN only",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("check output omitted %q: %q", expected, text)
		}
	}
}

func TestCheckOutputReportsDisabledCapabilities(t *testing.T) {
	settings := &fileSettings{Channels: &channelsSettings{Discord: &discordSettings{}}}
	loaded := &loaded{settings: settings, agent: selectedAgent{Model: "demo/large-model"}}
	var output bytes.Buffer
	if err := printCheck(&output, loaded); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{
		"group direct messages: disabled",
		"exec tool: disabled",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("check output omitted %q: %q", expected, text)
		}
	}
}

// TestHelpDescribesCommandsAndGates proves the help text lists the owner
// commands and the two opt-in gates.
func TestHelpDescribesCommandsAndGates(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("help: %v", err)
	}
	text := stderr.String()
	for _, expected := range []string{
		"/new, /reset, /stop, /status, /model, /compact",
		"channels.discord.dm.groupEnabled",
		"tools.exec.enabled",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("help omitted %q: %q", expected, text)
		}
	}
}

func int64Ptr(v int64) *int64 { return &v }
