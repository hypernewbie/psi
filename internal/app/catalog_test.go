package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/session"
	"psi/internal/terminal"
)

func TestEveryAdvertisedCatalogModelHasAnAppRequestPath(t *testing.T) {
	r := testRuntime(t)
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{}}
	count := 0
	for _, family := range []string{"anthropic", "openai-codex"} {
		for _, meta := range config.BundledModels(family) {
			t.Run(family+"/"+meta.ID, func(t *testing.T) {
				count++
				if err := r.configure(config.StartupOverrides{Provider: family, Model: meta.ID, Thinking: "high"}); err != nil {
					t.Fatal(err)
				}
				model := r.cfg.Model
				if model.ContextWindow != meta.ContextWindow || model.MaxTokens != meta.MaxTokens || !model.InputImage || !model.Reasoning {
					t.Fatal("catalog capability lost during app selection")
				}
				signature := "synthetic-thinking-signature"
				if family == "openai-codex" {
					signature = `{"id":"rs_synthetic","type":"reasoning","summary":[],"encrypted_content":"synthetic-encrypted-reasoning"}`
				}
				req := provider.Request{Model: model, Thinking: "high", System: "synthetic instructions", Tools: r.cfg.Tools, Messages: []provider.Message{
					{Role: "user", Content: []provider.Block{{Type: "text", Text: "synthetic prompt"}, {Type: "image", MimeType: "image/png", Data: "c3ludGhldGlj"}}},
					{Role: "assistant", Provider: family, API: model.API, Model: model.ID, ThinkingLevel: "medium", Content: []provider.Block{{Type: "thinking", Thinking: "synthetic", ThinkingSignature: signature}, {Type: "text", Text: "check files", TextSignature: `{"v":1,"id":"msg_synthetic","phase":"commentary"}`}, {Type: "toolCall", ID: "call_a|fc_a", Name: "read", Arguments: map[string]any{"path": "a"}}, {Type: "toolCall", ID: "call_b|fc_b", Name: "read", Arguments: map[string]any{"path": "b"}}}},
					{Role: "toolResult", ToolCallID: "call_a|fc_a", ToolName: "read", Content: []provider.Block{{Type: "text", Text: "synthetic a"}}},
					{Role: "toolResult", ToolCallID: "call_b|fc_b", ToolName: "read", IsError: true, Content: []provider.Block{{Type: "text", Text: "synthetic failure"}}},
				}}
				body, err := provider.BuildRequest(req)
				if err != nil {
					t.Fatal(err)
				}
				if body["model"] != meta.ID {
					t.Fatal("requested model substituted")
				}
				if family == "anthropic" {
					thinking := body["thinking"].(map[string]any)
					expected := "enabled"
					forceAdaptive, _, _ := meta.Compat.Bool("forceAdaptiveThinking")
					if forceAdaptive {
						expected = "adaptive"
					}
					if thinking["type"] != expected {
						t.Fatalf("thinking profile: %+v", thinking)
					}
					messages := body["messages"].([]map[string]any)
					grouped := false
					for _, message := range messages {
						if content, ok := message["content"].([]any); ok && len(content) == 2 {
							first, ok := content[0].(map[string]any)
							if ok && first["type"] == "tool_result" {
								grouped = true
							}
						}
					}
					if !grouped {
						t.Fatal("consecutive results were not grouped")
					}
					midConvoEffort, _, _ := meta.Compat.Bool("supportsMidConvoEffort")
					if midConvoEffort {
						last := messages[len(messages)-1]
						if last["role"] != "system" || last["output_config"].(map[string]any)["effort"] != "high" {
							t.Fatal("active effort marker missing")
						}
					}
				} else {
					if body["store"] != false || body["stream"] != true || body["max_tokens"] != nil || body["max_output_tokens"] != nil {
						t.Fatal("wrong Codex endpoint request profile")
					}
					encoded, _ := json.Marshal(body)
					for _, needed := range []string{"synthetic-encrypted-reasoning", "msg_synthetic", "commentary", "call_a", "call_b", "function_call_output"} {
						if !strings.Contains(string(encoded), needed) {
							t.Fatalf("Codex replay lost %s", needed)
						}
					}
				}
				for _, level := range provider.SupportedThinking(model) {
					req.Thinking = level
					if _, err := provider.BuildRequest(req); err != nil {
						t.Fatalf("advertised level %s: %v", level, err)
					}
				}
			})
		}
	}
	if count != 24 {
		t.Fatalf("public snapshot model count changed: %d", count)
	}
	wantPickerItems := 0
	for _, providerID := range providerregistry.IDs() {
		wantPickerItems += len(r.models.ModelOptions(providerID))
	}
	selector := modelSelector(r)
	if !selector.Models || len(selector.Items) != wantPickerItems || len(selector.PageItems()) != 20 {
		t.Fatalf("model picker did not page the complete local catalog: models=%v items=%d want=%d page=%d size=%d selected=%d", selector.Models, len(selector.Items), wantPickerItems, selector.Page, len(selector.PageItems()), selector.Selected)
	}
	for selector.Page < (wantPickerItems-1)/selector.PageSize {
		selector.NextPage()
	}
	if len(selector.PageItems()) != wantPickerItems-selector.Page*selector.PageSize {
		t.Fatal("last catalog page missing")
	}
	selector.SetFilter("claude-opus-4-6")
	selected := selector.SelectedItem()
	if selected == nil || selected.ID != "anthropic/claude-opus-4-6" {
		t.Fatal("filtered model selection lost its stable identity")
	}
}

type observedProvider struct{ closed int }

func (p *observedProvider) CloseIdle() { p.closed++ }
func (p *observedProvider) Send(context.Context, provider.Request, func(string)) (*provider.Response, error) {
	panic("no provider call permitted")
}

func TestFailedDurableConfigurationChangeKeepsExactPriorRuntime(t *testing.T) {
	for _, command := range []terminal.Command{{Name: "thinking", Arg: "high"}, {Name: "model", Arg: "anthropic/claude-haiku-4-5"}} {
		t.Run(command.Name, func(t *testing.T) {
			r := testRuntime(t)
			if _, err := r.store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic"}}); err != nil {
				t.Fatal(err)
			}
			old := &observedProvider{}
			r.prov = old
			ov, cfg, reg := r.ov, r.cfg, r.reg
			file, err := os.OpenFile(r.store.File(), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("\n"); err != nil {
				t.Fatal(err)
			}
			file.Close()
			if _, err := r.command(&command, io.Discard); err == nil {
				t.Fatal("external generation change accepted")
			}
			if r.prov != old || old.closed != 0 || r.reg != reg || r.ov != ov || r.cfg.Model.ID != cfg.Model.ID || r.cfg.Thinking != cfg.Thinking {
				t.Fatal("failed change replaced or closed the old runtime")
			}
		})
	}
}

func TestAdaptiveThinkingDescriptionsAreExplicit(t *testing.T) {
	r := testRuntime(t)
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "claude-opus-4-6", Thinking: "xhigh"}); err != nil {
		t.Fatal(err)
	}
	if description := provider.ThinkingDescription(r.cfg.Model, "xhigh"); !strings.Contains(description, "effective effort high") {
		t.Fatal(description)
	}
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "claude-opus-5-5", Thinking: "minimal"}); err == nil {
		t.Fatal("explicitly unsupported reasoning level accepted")
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai-codex", Model: "gpt-6-astra", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	if description := provider.ThinkingDescription(r.cfg.Model, "off"); !strings.Contains(description, "provider default") {
		t.Fatal(description)
	}
}

// A4-12: a failed model persistence keeps the prior session, provider,
// footer, draft, cursor, and recall. The refused store must never leave a
// half-installed configuration behind.
func TestFailedModelPersistenceKeepsPriorRuntimeAndDraft(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic"}}); err != nil {
		t.Fatal(err)
	}
	providerMeta := r.models.Providers["anthropic"]
	providerMeta.Models = append(providerMeta.Models, config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	r.models.Providers["anthropic"] = providerMeta
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output strings.Builder
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	console.Editor.RestoreDraft("keep this draft", 5)
	console.Editor.SetHistory([]string{"synthetic"})
	r.draw(console)

	oldProvider, oldModel, oldThinking, oldThinkingLevel := r.ov.Provider, r.ov.Model, r.ov.Thinking, r.cfg.Thinking
	oldProv, oldFooter := r.prov, r.footerSnapshot
	before, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	r.store.FailWritesForTest(errPoison)
	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m2"}, io.Discard); err == nil {
		t.Fatal("model change accepted on a refused store")
	}
	if r.ov.Provider != oldProvider || r.ov.Model != oldModel || r.ov.Thinking != oldThinking || r.cfg.Thinking != oldThinkingLevel {
		t.Fatalf("failed model persistence replaced the configuration: %+v", r.ov)
	}
	if r.prov != oldProv {
		t.Fatal("failed model persistence replaced the provider")
	}
	if console.Editor.Text() != "keep this draft" || console.Editor.Pos() != 5 {
		t.Fatalf("failed model persistence changed the draft or cursor: %q %d", console.Editor.Text(), console.Editor.Pos())
	}
	if history := console.Editor.History(); len(history) != 1 || history[0] != "synthetic" {
		t.Fatalf("failed model persistence changed the recall history: %v", history)
	}
	after, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("failed model persistence changed the session file: %s", after)
	}
	r.draw(console)
	if r.footerSnapshot.Model != oldFooter.Model || r.footerSnapshot.Provider != oldFooter.Provider {
		t.Fatalf("failed model persistence changed the footer: %+v", r.footerSnapshot)
	}
}

// Pi's compaction indicator names the phase and the cancellation binding.
func TestCompactionActivityFollowsPiPhases(t *testing.T) {
	cases := map[string]string{
		event.CompactionManual:    "Compacting context… (Esc/Ctrl+C cancel)",
		event.CompactionThreshold: "Auto-compacting… (Esc/Ctrl+C cancel)",
		event.CompactionOverflow:  "Context overflow detected, Auto-compacting… (Esc/Ctrl+C cancel)",
	}
	for state, want := range cases {
		if got := compactionActivity(state); got != want {
			t.Fatalf("compaction %s: got %q, want %q", state, got, want)
		}
	}
}
