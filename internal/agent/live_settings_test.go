package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/tools"
)

// TestLiveSettingsApplyAtTheNextTurnBoundary proves that a live configuration
// change reaches the next provider request of the running loop, and that the
// request already in flight keeps the configuration it was built with.
func TestLiveSettingsApplyAtTheNextTurnBoundary(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	ws := store.Cwd()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{
		{Text: "reading", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.txt"}}}},
		{Text: "done", StopReason: "stop"},
	}
	submitted := modelMeta()
	updated := provider.ModelMeta{ID: "claude-y", API: "anthropic-messages", MaxTokens: 16384, Reasoning: true}
	cfg := Config{Model: submitted, Thinking: "off", Tools: toolDefs(), System: "base"}
	cfg.LiveSettings = func() LiveSettings {
		live := LiveSettings{Provider: fake, Model: submitted, Thinking: "off"}
		if fake.CallCount() > 0 {
			live.Model = updated
			live.Thinking = "high"
		}
		return live
	}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	if requests[0].Model.ID != submitted.ID || requests[0].Thinking != "off" {
		t.Errorf("first request: model=%q thinking=%q", requests[0].Model.ID, requests[0].Thinking)
	}
	if requests[1].Model.ID != updated.ID || requests[1].Thinking != "high" {
		t.Errorf("second request: model=%q thinking=%q", requests[1].Model.ID, requests[1].Thinking)
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	users, assistants, results := 0, 0, 0
	for _, m := range proj.Flat {
		switch {
		case m.User != nil:
			users++
		case m.Assistant != nil:
			assistants++
		case m.ToolResult != nil:
			results++
		}
	}
	if users != 1 || assistants != 2 || results != 1 {
		t.Errorf("users=%d assistants=%d results=%d", users, assistants, results)
	}
}

func TestSplitCompactionAttributesUsageAcrossLiveModelSwitch(t *testing.T) {
	for _, mode := range []string{"success", "incomplete prefix", "cancel before prefix"} {
		t.Run(mode, func(t *testing.T) {
			store, _, history, loop, _ := testSetup(t)
			if _, err := store.AppendMessage(userMsg("older task")); err != nil {
				t.Fatal(err)
			}
			appendAssistantText(t, store, "older progress")
			if _, err := store.AppendMessage(userMsg("current task")); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 80_000))
			}
			before := len(store.Entries())
			oldModel, newModel := modelMeta(), modelMeta()
			newModel.ID = "claude-prefix"
			history.Responses = []*provider.Response{{Text: "history checkpoint", StopReason: "stop", Usage: provider.Usage{TotalTokens: 25}}}
			prefixResponse := &provider.Response{Text: "prefix checkpoint", StopReason: "stop", Usage: provider.Usage{TotalTokens: 13}}
			if mode == "incomplete prefix" {
				prefixResponse.StopReason = "length"
			}
			prefix := &provider.Fake{Responses: []*provider.Response{prefixResponse}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg := Config{Model: oldModel, KeepRecentTokens: 20_000, LiveSettings: func() LiveSettings {
				if history.CallCount() == 0 {
					return LiveSettings{Provider: history, Model: oldModel}
				}
				if mode == "cancel before prefix" {
					cancel()
				}
				return LiveSettings{Provider: prefix, Model: newModel}
			}}
			err := loop.Compact(ctx, cfg, "")
			if mode == "success" && err != nil || mode == "incomplete prefix" && !IsSummaryFailure(err) || mode == "cancel before prefix" && !errors.Is(err, context.Canceled) {
				t.Fatalf("compaction result: %v", err)
			}
			wantUsage := map[string]float64{oldModel.ID: 25}
			if mode != "cancel before prefix" {
				wantUsage[newModel.ID] = 13
				if calls := prefix.Requests(); len(calls) != 1 || calls[0].Model.ID != newModel.ID {
					t.Fatalf("prefix summary did not use the new model")
				}
			} else if prefix.CallCount() != 0 {
				t.Fatal("prefix request started after cancellation")
			}
			checkpoints := 0
			for _, entry := range store.Entries()[before:] {
				if entry.Compaction != nil {
					checkpoints++
					if entry.Compaction.Usage != nil {
						t.Fatal("checkpoint double-counts attributed usage")
					}
				}
				if saved := entry.Usage; saved != nil {
					if saved.Provider != oldModel.ProviderID() || wantUsage[saved.Model] != saved.Usage.TotalTokens {
						t.Fatalf("unexpected or duplicate attributed usage: %+v", saved)
					}
					delete(wantUsage, saved.Model)
				}
			}
			if len(wantUsage) != 0 || (checkpoints == 1) != (mode == "success") || checkpoints > 1 {
				t.Fatalf("missing usage: %v; checkpoints: %d", wantUsage, checkpoints)
			}
		})
	}
}

// TestApplyLiveSettingsRefreshesReadPolicy proves a model switch refreshes
// the loop's image-read policy at the turn boundary: the loop outlives
// set_model's registry rebuild, so without the refresh a stale policy would
// govern reads for the rest of the session.
func TestApplyLiveSettingsRefreshesReadPolicy(t *testing.T) {
	reg := tools.NewRegistry("")
	reg.Read.BlockImages = false
	loop := &Loop{reg: reg}
	fake := &provider.Fake{}
	cfg := Config{LiveSettings: func() LiveSettings {
		return LiveSettings{Provider: fake, Model: provider.ModelMeta{ID: "no-images", InputImage: false}}
	}}
	applyLiveSettings(&cfg, loop)
	if !reg.Read.BlockImages {
		t.Fatal("read policy still allows images after switching to a model without image input")
	}
	cfg.LiveSettings = func() LiveSettings {
		return LiveSettings{Provider: fake, Model: provider.ModelMeta{ID: "images", InputImage: true}}
	}
	applyLiveSettings(&cfg, loop)
	if reg.Read.BlockImages {
		t.Fatal("read policy still blocks images after switching to a model with image input")
	}
}
