package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/session"
)

// systemRecords returns the "message" payload of every system record in the
// session file, in file order.
func systemRecords(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []json.RawMessage
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("decode session line: %v", err)
		}
		var probe struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(envelope["message"], &probe); err != nil {
			continue
		}
		if probe.Role == "system" {
			out = append(out, envelope["message"])
		}
	}
	return out
}

func sectionFields(t *testing.T, record json.RawMessage) ([]string, map[string]*string) {
	t.Helper()
	var message map[string]json.RawMessage
	if err := json.Unmarshal(record, &message); err != nil {
		t.Fatal(err)
	}
	if string(message["content"]) != `""` {
		t.Fatalf("system content = %s, want an empty string", message["content"])
	}
	dec := json.NewDecoder(bytes.NewReader(message["sections"]))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("sections is not an object: %s", message["sections"])
	}
	var keys []string
	values := map[string]*string{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		key := token.(string)
		var value *string
		if err := dec.Decode(&value); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		values[key] = value
	}
	return keys, values
}

func TestSystemSectionsWrittenInPromptOrder(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	sections := []config.PromptSection{
		{Name: "preamble", Body: "You are psi"},
		{Name: "tools", Body: "<tools>\n- read: Read file contents\n</tools>"},
		{Name: "rules", Body: "<rules>\n- Be concise in your responses\n</rules>"},
		{Name: "docs", Body: "<docs>\nquickstart\n</docs>"},
		{Name: "addendum", Body: "<addendum>\nextra\n</addendum>"},
		{Name: "project_context", Body: "<project_context>\nctx\n</project_context>"},
		{Name: "skills", Body: "<skills>\nskill\n</skills>"},
		{Name: "cwd", Body: "<cwd>\n/ws\n</cwd>"},
	}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: config.RenderSections(sections), Sections: sections}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	records := systemRecords(t, store.File())
	if len(records) != 1 {
		t.Fatalf("system records = %d, want 1", len(records))
	}
	keys, _ := sectionFields(t, records[0])
	want := []string{"preamble", "tools", "rules", "docs", "addendum", "project_context", "skills", "cwd"}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Fatalf("session section order = %v, want %v", keys, want)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].System != config.RenderSections(sections) {
		t.Fatalf("request system prompt drifted from rendered sections: %q", fake.Calls[0].System)
	}

	// A second identical run must not append another system record.
	fake.Responses = append(fake.Responses, &provider.Response{Text: "again", StopReason: "stop"})
	if err := loop.Submit(context.Background(), "again", cfg); err != nil {
		t.Fatal(err)
	}
	if records := systemRecords(t, store.File()); len(records) != 1 {
		t.Fatalf("second run appended another system record: %d", len(records))
	}
}

func TestSystemSectionsLegacyFallbackWithoutSections(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base prompt"}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	keys, _ := sectionFields(t, systemRecords(t, store.File())[0])
	if len(keys) != 1 || keys[0] != "psi" {
		t.Fatalf("legacy sections = %v, want [psi]", keys)
	}
	if fake.Calls[0].System != "base prompt" {
		t.Fatalf("legacy request system = %q", fake.Calls[0].System)
	}
}

func TestSystemSectionRemovalIsPersisted(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}, {Text: "done", StopReason: "stop"}, {Text: "done", StopReason: "stop"}}
	base := []config.PromptSection{{Name: "preamble", Body: "You are psi"}, {Name: "cwd", Body: "<cwd>\n/ws\n</cwd>"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: config.RenderSections(base), Sections: base}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	withLoop := append(append([]config.PromptSection(nil), base...), config.PromptSection{Name: "loop", Body: "<loop>\ngo</loop>"})
	cfg.Sections = withLoop
	cfg.System = config.RenderSections(withLoop)
	if err := loop.Submit(context.Background(), "next", cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.Calls[1].System, "<loop>") {
		t.Fatalf("active loop section missing from its request: %q", fake.Calls[1].System)
	}
	records := systemRecords(t, store.File())
	keys, values := sectionFields(t, records[len(records)-1])
	if strings.Join(keys, " ") != "preamble cwd loop" || values["loop"] == nil || *values["loop"] != "<loop>\ngo</loop>" {
		t.Fatalf("loop section not active on disk: keys=%v", keys)
	}
	cfg.Sections = base
	cfg.System = config.RenderSections(base)
	if err := loop.Submit(context.Background(), "after", cfg); err != nil {
		t.Fatal(err)
	}
	records = systemRecords(t, store.File())
	keys, values = sectionFields(t, records[len(records)-1])
	if strings.Join(keys, " ") != "preamble cwd loop" || values["loop"] != nil {
		t.Fatalf("removal tombstone missing: keys=%v", keys)
	}
	if strings.Contains(fake.Calls[2].System, "<loop>") {
		t.Fatalf("removed loop section replayed: %q", fake.Calls[2].System)
	}
}

func TestOldSingleSectionSessionIsReplaced(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	legacy := "old flat prompt"
	if _, err := store.AppendMessage(session.AgentMessage{Role: "system", System: &session.SystemMessage{
		Role: "system", Content: "", Sections: map[string]*string{"psi": &legacy}, Timestamp: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	sections := []config.PromptSection{{Name: "preamble", Body: "You are psi"}, {Name: "cwd", Body: "<cwd>\n/ws\n</cwd>"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: config.RenderSections(sections), Sections: sections}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	records := systemRecords(t, store.File())
	keys, values := sectionFields(t, records[len(records)-1])
	if strings.Join(keys, " ") != "preamble cwd psi" || values["psi"] != nil {
		t.Fatalf("legacy psi section not retired: keys=%v psi=%v", keys, values["psi"])
	}
	if fake.Calls[0].System != config.RenderSections(sections) {
		t.Fatalf("replayed system = %q, want the assembled prompt", fake.Calls[0].System)
	}
}

func TestSectionsSurviveCompaction(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{
		{Text: "first", StopReason: "stop"},
		{Text: "## Goal\nkeep working", StopReason: "stop", Usage: provider.Usage{Input: 10, Output: 5}},
		{Text: "after compaction", StopReason: "stop"},
		{Text: "final", StopReason: "stop"},
	}
	sections := []config.PromptSection{{Name: "preamble", Body: "You are psi"}, {Name: "cwd", Body: "<cwd>\n/ws\n</cwd>"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: config.RenderSections(sections), Sections: sections, KeepRecentTokens: 1}
	ctx := context.Background()
	if _, err := store.AppendMessage(userMsg("earlier work")); err != nil {
		t.Fatal(err)
	}
	if err := loop.Submit(ctx, "hello", cfg); err != nil {
		t.Fatal(err)
	}
	if err := loop.Compact(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err := loop.Submit(ctx, "continue", cfg); err != nil {
		t.Fatal(err)
	}
	last := fake.Calls[len(fake.Calls)-1].System
	if !strings.Contains(last, "<cwd>\n/ws\n</cwd>") || !strings.HasPrefix(last, "You are psi") {
		t.Fatalf("post-compaction system lost sections: %q", last)
	}
	records := systemRecords(t, store.File())
	keys, _ := sectionFields(t, records[len(records)-1])
	if strings.Join(keys, " ") != "preamble cwd" {
		t.Fatalf("compaction rewrote sections: %v", keys)
	}
}
