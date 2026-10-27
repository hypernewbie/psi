package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReviewHeaderExactFields(t *testing.T) {
	data := []byte(`{"type":"session","version":3,"Version":99,"id":"canonical","ID":"alias","cwd":"/synthetic","CWD":"/alias"}`)
	f, err := UnmarshalFileEntry(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.ID != "canonical" || f.Header.Cwd != "/synthetic" || f.Header.Version == nil || *f.Header.Version != 3 {
		t.Fatalf("header aliases changed canonical fields: %+v", f.Header)
	}
	_, h, err := metadataRecord(data)
	if err != nil || !reflect.DeepEqual(h, f.Header) {
		t.Fatalf("header readers disagree: eager=%+v indexed=%+v err=%v", f.Header, h, err)
	}
}

func TestReviewNestedExactDecoders(t *testing.T) {
	var m AgentMessage
	data := []byte(`{"role":"assistant","Role":"user","stopReason":"stop","StopReason":"error","usage":{"cost":{"total":2.5,"TOTAL":99}},"content":[{"type":"text","text":"canonical","TEXT":"alias"}]}`)
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Role != "assistant" || m.Assistant == nil || m.Assistant.Role != "assistant" || m.Assistant.StopReason != "stop" || m.Assistant.Usage.Cost.Total != 2.5 || m.Assistant.Content[0].Text.Text != "canonical" {
		t.Fatalf("nested canonical fields changed: %+v", m.Assistant)
	}
	var block UserContentBlock
	if err := json.Unmarshal([]byte(`{"type":"image","data":"canonical","DATA":"alias","mimeType":"image/png"}`), &block); err != nil {
		t.Fatal(err)
	}
	if block.Image == nil || block.Image.Data != "canonical" {
		t.Fatalf("direct image decoder folded alias: %+v", block)
	}
}

func TestReviewImageResultPersistsAndReopens(t *testing.T) {
	dir := t.TempDir()
	store, err := New(OpenOptions{Cwd: dir, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	user := AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "read the synthetic image"}}
	if _, err := store.AppendMessage(user); err != nil {
		t.Fatal(err)
	}
	image := AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult", ToolCallID: "read-image", ToolName: "read", Content: []UserContentBlock{{Type: "image", Image: &ImageContent{Type: "image", Data: "eA==", MimeType: "image/png"}}}}}
	if _, err := store.AppendMessage(image); err != nil {
		t.Fatalf("image result could not be persisted: %v", err)
	}
	path := store.File()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFile(filepath.Clean(path), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reopened.ActivePath()
	if err != nil || len(entries) != 2 || entries[1].Message.Message.ToolResult.Content[0].Image.Data != "eA==" {
		t.Fatalf("image did not survive lazy reopen: entries=%+v err=%v", entries, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("reopen changed the imported prefix")
	}
}

func TestReviewMetadataSchemas(t *testing.T) {
	cases := []struct {
		name, record string
	}{
		{"image-result", `{"type":"message","id":"r","message":{"role":"toolResult","content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}}`},
		{"image-before-role", `{"type":"message","id":"r","message":{"content":[{"type":"image","data":"eA==","mimeType":"image/png"}],"role":"toolResult"}}`},
		{"irrelevant-typed-field", `{"type":"message","id":"u","message":{"role":"user","content":"hi","exitCode":"not an exit code"}}`},
		{"irrelevant-message", `{"type":"custom","id":"c","message":17}`},
		{"invalid-compaction-summary", `{"type":"compaction","id":"c","summary":123}`},
		{"invalid-thinking-boolean", `{"type":"message","id":"a","message":{"role":"assistant","content":[{"type":"thinking","redacted":"no"}]}}`},
		{"last-text-wins", `{"type":"message","id":"a","message":{"role":"assistant","content":[{"type":"text","text":123,"text":"valid"}]}}`},
		{"last-message-wins", `{"type":"message","id":"a","message":{"role":"assistant","content":"invalid"},"message":{"role":"user","content":"valid"}}`},
		{"invalid-header-leaf", `{"type":"session","version":3,"id":"h","cwd":123}`},
		{"deep-unknown", `{"type":"custom","id":"c","data":` + strings.Repeat("[", 10001) + `0` + strings.Repeat("]", 10001) + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full, eagerErr := UnmarshalFileEntry([]byte(tc.record))
			_, _, thinErr := metadataRecord([]byte(tc.record))
			if (eagerErr == nil) != (thinErr == nil) {
				t.Fatalf("schema disagreement: eager=%v metadata=%v full=%+v", eagerErr, thinErr, full)
			}
		})
	}
}

func TestReviewMetadataRetainsStateNotJustIdentity(t *testing.T) {
	cases := []string{
		`{"type":"message","id":"a","message":{"role":"assistant","role":null}}`,
		`{"type":"message","id":"a","message":{"role":"assistant","usage":{"input":777,"input":null,"cost":{"total":2.5}}}}`,
		`{"type":"message","id":"a","message":{"role":"assistant","usage":{"cost":{"total":2.5,"TOTAL":99}}}}`,
		`{"type":"message","id":"a","message":{"role":"assistant","content":[{"type":"text"}],"content":[]}}`,
	}
	for _, record := range cases {
		t.Run(record, func(t *testing.T) {
			full, err := UnmarshalEntry([]byte(record))
			if err != nil {
				t.Fatal(err)
			}
			thin, err := metadataEntry([]byte(record))
			if err != nil {
				t.Fatal(err)
			}
			if thin.Message.Message.Role != full.Message.Message.Role {
				t.Fatalf("role changed: eager=%q metadata=%q", full.Message.Message.Role, thin.Message.Message.Role)
			}
			if a := full.Message.Message.Assistant; a != nil {
				b := thin.Message.Message.Assistant
				if b == nil || !reflect.DeepEqual(a.Usage, b.Usage) {
					t.Fatalf("usage changed: eager=%+v metadata=%+v", a.Usage, b)
				}
			}
		})
	}
}

func TestReviewMetadataProvenanceUsesCorrectObject(t *testing.T) {
	for _, kind := range []string{"custom", "custom_message"} {
		record := `{"type":` + strconvJSON(kind) + `,"id":"p","customType":"psi.input","data":{"targetId":"user","psiOutcome":"other"},"details":{"targetId":"visible","psiOutcome":"other"}}`
		e, err := metadataEntry([]byte(record))
		if err != nil {
			t.Fatal(err)
		}
		want := "user"
		if kind == "custom_message" {
			want = "visible"
		}
		if e.provenanceTarget != want {
			t.Fatalf("%s target=%q want %q", kind, e.provenanceTarget, want)
		}
	}
}

func strconvJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

// Only retained semantic fields belong here. Hidden payload bytes must never
// appear in this comparison or in the index. This projection is independent
// of the production scanner and starts from either eager or thin entries.
func reviewMetadataState(e *Entry) map[string]any {
	if e == nil {
		return nil
	}
	out := map[string]any{"kind": e.Kind, "id": e.ID(), "parent": e.ParentID(), "time": e.Base.Timestamp}
	if e.Message != nil {
		m := e.Message.Message
		out["role"] = m.Role
		if a := m.Assistant; a != nil {
			out["provider"], out["model"], out["api"] = a.Provider, a.Model, a.API
			out["responseModel"], out["usage"] = a.ResponseModel, a.Usage
			text := false
			var calls [][2]string
			for _, b := range a.Content {
				text = text || b.Text != nil
				if b.Call != nil {
					calls = append(calls, [2]string{b.Call.ID, b.Call.Name})
				}
			}
			out["text"], out["calls"] = text, calls
		}
		if r := m.ToolResult; r != nil {
			out["call"], out["tool"], out["error"] = r.ToolCallID, r.ToolName, r.IsError
			obj, _ := exactObject(r.Details)
			out["outcome"], _ = exactString(obj, "psiOutcome")
		}
		if b := m.BashExecution; b != nil {
			out["cancelled"], out["exit"] = b.Cancelled, b.ExitCode
		}
	}
	if c := e.Compaction; c != nil {
		out["firstKept"], out["usage"] = c.FirstKeptEntryID, c.Usage
	}
	if c := e.ContextEdit; c != nil {
		out["target"] = c.TargetID
	}
	if m := e.ModelChange; m != nil {
		out["provider"], out["model"] = m.Provider, m.ModelID
	}
	if c := e.ThinkingLevelChange; c != nil {
		out["thinking"] = c.ThinkingLevel
	}
	if u := e.Usage; u != nil {
		out["usage"] = u.Usage
	}
	if i := e.SessionInfo; i != nil {
		out["name"] = i.Name
	}
	if c := e.Custom; c != nil {
		out["custom"] = c.CustomType
		if c.CustomType == "psi.input" {
			out["provenance"] = e.provenanceTarget
			if len(e.Raw) != 0 {
				obj, _ := exactObject(c.Data)
				out["provenance"], _ = exactString(obj, "targetId")
			}
		}
	}
	if c := e.CustomMessage; c != nil {
		out["custom"], out["display"] = c.CustomType, c.Display
		if c.CustomType == "psi.input" {
			out["provenance"] = e.provenanceTarget
			if len(e.Raw) != 0 {
				obj, _ := exactObject(c.Details)
				out["provenance"], _ = exactString(obj, "targetId")
			}
		}
	}
	return out
}
