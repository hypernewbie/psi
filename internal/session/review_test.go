package session

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryUsesResultFlagsAndWholeViewCallLinkage(t *testing.T) {
	for _, tc := range []struct {
		name, body, want        string
		isError, result, orphan bool
		details                 json.RawMessage
	}{
		{name: "successful literal error text", body: "error failed cancelled exit 1", want: "read: done", result: true},
		{name: "failed neutral text", body: "permission denied", want: "read: failed", isError: true, result: true},
		{name: "cancelled", want: "read: cancelled", isError: true, result: true, details: json.RawMessage(`{"psiOutcome":"cancelled"}`)},
		{name: "unstarted", want: "read: not started", isError: true, result: true, details: json.RawMessage(`{"psiOutcome":"not_started"}`)},
		{name: "orphan failed", want: "read: failed", isError: true, result: true, orphan: true},
		{name: "unresolved", want: "read: interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, _ := New(OpenOptions{Cwd: dir, SessionDir: dir, AgentDir: dir, Persist: false})
			if !tc.orphan {
				_, err := s.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", Content: []ContentBlock{{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: "call", Name: "read", Arguments: map[string]any{}}}}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.result {
				_, err := s.AppendMessage(AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult", ToolCallID: "call", ToolName: "read", IsError: tc.isError, Details: tc.details, Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: tc.body}}}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			path, _ := s.ActivePath()
			for _, view := range [][]TranscriptItem{CurrentView(path), AllHistoryView(path)} {
				count := 0
				for _, it := range view {
					if it.Kind == KindTool {
						count++
						if it.Text != tc.want {
							t.Fatalf("outcome %q != %q", it.Text, tc.want)
						}
					}
				}
				if count != 1 {
					t.Fatalf("%d tool rows", count)
				}
			}
		})
	}
}

func TestTimelineUsesInputProvenanceWithoutChangingContext(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(OpenOptions{Cwd: dir, SessionDir: dir, AgentDir: dir, Persist: false})
	id, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "expanded synthetic skill body"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendProvenance(id, "/skill:review fix", "skill"); err != nil {
		t.Fatal(err)
	}
	path, _ := s.ActivePath()
	for _, view := range [][]TranscriptItem{CurrentView(path), AllHistoryView(path)} {
		for _, it := range view {
			if it.Kind == KindUser && it.Text != "/skill:review fix" {
				t.Fatalf("invocation hidden: %+v", it)
			}
		}
	}
	proj, _ := s.Projection()
	if proj.Flat[0].User.Content != "expanded synthetic skill body" {
		t.Fatal("display changed model context")
	}
}

type repeatedByteReader struct{ count int64 }

func (r *repeatedByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.count += int64(len(p))
	return len(p), nil
}

func TestIndexBoundsWhileReadingAndRejectsMalformedRecords(t *testing.T) {
	r := &repeatedByteReader{}
	_, err := BuildIndexFrom(io.LimitReader(r, MaxRecordBytes+1024*1024), "synthetic.jsonl")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal(err)
	}
	if r.count > MaxRecordBytes+64*1024+2 {
		t.Fatalf("unbounded record allocation: read %d", r.count)
	}
	if _, err := BuildIndexFrom(strings.NewReader("{bad json}\n"), "synthetic.jsonl"); err == nil {
		t.Fatal("malformed index accepted")
	}
	idx := &RecordIndex{Path: filepath.Join(t.TempDir(), "absent.jsonl")}
	for _, meta := range []RecordMeta{{Length: -1}, {Length: MaxRecordBytes + 3}, {Offset: -1, Length: 1}} {
		if _, err := idx.LoadEntry(meta); err == nil || !strings.Contains(err.Error(), "bounds") {
			t.Fatal(err)
		}
	}
}

func TestStorageFailureRemainsBlockedAfterDirectoryRestored(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := New(OpenOptions{Cwd: dir, SessionDir: dir, AgentDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	message := AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "synthetic"}}
	if _, err := s.AppendMessage(message); err == nil {
		t.Fatal("missing directory accepted")
	}
	if s.WriteError() == nil {
		t.Fatal("storage failure forgotten")
	}
	os.MkdirAll(dir, 0700)
	if _, err := s.AppendMessage(message); err == nil {
		t.Fatal("unsafe retry after storage failure")
	}
	if _, err := os.Stat(s.File()); !os.IsNotExist(err) {
		t.Fatal("blocked retry created a new file")
	}
}

func TestVisibleCustomImageDoesNotDumpEncodedPayload(t *testing.T) {
	payload := "SYNTHETIC_IMAGE_BYTES"
	content := []any{map[string]any{"type": "image", "data": payload, "mimeType": "image/png"}}
	entries := []*Entry{{Kind: "custom_message", CustomMessage: &CustomMessageEntry{Display: true, CustomType: "image", Content: content}}}
	view := CurrentView(entries)
	var text bytes.Buffer
	for _, it := range view {
		text.WriteString(it.Text)
	}
	if strings.Contains(text.String(), payload) || !strings.Contains(text.String(), "[image]") {
		t.Fatalf("unsafe custom display: %q", text.String())
	}
}
