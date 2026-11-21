package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/ac"
	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
)

// TestACStatusLeadsWithEffectiveState pins the reporting fix: a summary
// failure latches StoppedAfterError in pi-ac to avoid paying for the same
// failed summary every turn. The headline must say paused, not enabled, while
// native threshold protection stays available via a separate line.
func TestACStatusLeadsWithEffectiveState(t *testing.T) {
	home := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: t.TempDir(), SessionDir: filepath.Join(home, "sessions"), AgentDir: filepath.Join(home, "agent"), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{
		store: store,
		ac:    ac.NewController(),
		cfg: agent.Config{
			Model: provider.ModelMeta{ID: "m", Provider: "anthropic", API: "anthropic-messages", ContextWindow: 100000, MaxTokens: 1024},
		},
		settings: config.Settings{Compaction: config.CompactionConfig{Enabled: true}},
	}
	// Enabled with native on stays enabled.
	if got := r.acStatusText(); !strings.HasPrefix(got, "pi-ac: automatic compaction enabled") {
		t.Fatalf("enabled status = %q", got)
	}
	// A real failure pauses proactive attempts until /ac enable clears it.
	r.ac.RecordCompaction(false, "automatic", 60000)
	got := r.acStatusText()
	first := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(first, "paused after an error") {
		t.Fatalf("paused headline = %q", first)
	}
	if !strings.Contains(got, "/ac enable to retry") {
		t.Fatalf("paused status must say how to retry: %q", got)
	}
	if !strings.Contains(got, "Pi auto-compaction: enabled") {
		t.Fatalf("native protection must stay visible while paused: %q", got)
	}
	// /ac enable clears the latch, matching pi-ac.
	r.ac.SetEnabled(true)
	if got := r.acStatusText(); !strings.HasPrefix(got, "pi-ac: automatic compaction enabled") {
		t.Fatalf("after enable = %q", got)
	}
}
