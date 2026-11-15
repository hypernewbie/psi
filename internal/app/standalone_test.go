//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestActualCopiedBinaryWithoutNodeUsesAllFourTools(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("requires explicit -psi-test-binary for the delivered executable")
	}
	root := t.TempDir()
	home, work, bin := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "bin")
	for _, dir := range []string{home, work, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// 2,189 skipped lines put the chosen text at the recorded p95 offset.
	// The NUL bytes verify that skipped text is not treated as binary.
	readWindowContent := strings.Repeat("skip\x00\r\n", 2189) + "chosen α\r\nchosen β\r\nunreturned\r\n"
	if err := os.WriteFile(filepath.Join(work, "read-window.txt"), []byte(readWindowContent), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(*reviewBinary)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(bin, "psi")
	if err := os.WriteFile(copied, data, 0700); err != nil {
		t.Fatal(err)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	shell, err = filepath.Abs(shell)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shell, filepath.Join(bin, "bash")); err != nil {
		t.Fatal(err)
	}
	const initialContent = "printf 'broken\\n'\n# remove me\n"
	const finalContent = "printf 'SYNTHETIC_TOOL_BODY_FIXED\\n'\n"
	const wantReadText = "chosen α\r\nchosen β\r\n\n\n[Output truncated. Use offset=2192 to continue; use bash for a line larger than 50 KiB.]"
	checkResults := func(request map[string]any, wantIDs ...string) map[string]map[string]any {
		t.Helper()
		messages, ok := request["messages"].([]any)
		if !ok || len(messages) == 0 {
			t.Error("request has no messages")
			return nil
		}
		last, ok := messages[len(messages)-1].(map[string]any)
		if !ok || last["role"] != "user" {
			t.Error("last message must contain tool results")
			return nil
		}
		results, ok := last["content"].([]any)
		if !ok || len(results) != len(wantIDs) {
			t.Errorf("expected %d tool results, got: %+v", len(wantIDs), last["content"])
			return nil
		}
		pending := make(map[string]bool, len(wantIDs))
		settled := make(map[string]map[string]any, len(wantIDs))
		for _, id := range wantIDs {
			pending[id] = true
		}
		for _, entry := range results {
			result, ok := entry.(map[string]any)
			if !ok || result["type"] != "tool_result" || result["is_error"] != false {
				t.Errorf("tool failed or result is malformed: %+v", entry)
				continue
			}
			id, ok := result["tool_use_id"].(string)
			if !ok || !pending[id] {
				t.Errorf("unexpected or duplicate tool result: %+v", result)
				continue
			}
			delete(pending, id)
			settled[id] = result
		}
		if len(pending) != 0 {
			t.Errorf("missing tool results: %+v", pending)
		}
		return settled
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		turn := requests.Add(1)
		switch turn {
		case 2:
			checkResults(request, "synthetic_call_1_0")
			data, err := os.ReadFile(filepath.Join(work, "standalone.sh"))
			if err != nil || string(data) != initialContent {
				t.Errorf("settled write bytes = %q, error = %v", data, err)
			}
		case 3:
			checkResults(request, "synthetic_call_2_0")
			data, err := os.ReadFile(filepath.Join(work, "standalone.sh"))
			if err != nil || string(data) != finalContent {
				t.Errorf("settled edit bytes = %q, error = %v", data, err)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
		// Separate mutations from their readers: parallel tool batches do not
		// guarantee ordering. Each following request must carry settled results.
		type toolCall struct {
			name string
			args map[string]any
		}
		var calls []toolCall
		switch turn {
		case 1:
			calls = []toolCall{{"write", map[string]any{"path": "standalone.sh", "content": initialContent}}}
		case 2:
			// Adjacent spans cover both endpoints, in reverse order, and delete
			// the trailing line. Every match refers to the original file.
			calls = []toolCall{{"edit", map[string]any{
				"path": "standalone.sh",
				"edits": []any{
					map[string]any{"oldText": "# remove me\n", "newText": ""},
					map[string]any{"oldText": "printf 'broken\\n'\n", "newText": finalContent},
				},
			}}}
		case 3:
			calls = []toolCall{
				{"read", map[string]any{"path": "read-window.txt", "offset": 2190, "limit": 2}},
				{"bash", map[string]any{"command": "bash standalone.sh"}},
			}
		default:
			if turn != 4 {
				t.Error("unexpected provider retry")
			}
			settled := checkResults(request, "synthetic_call_3_0", "synthetic_call_3_1")
			readResult := settled["synthetic_call_3_0"]
			content, ok := readResult["content"].([]any)
			if !ok || len(content) != 1 {
				t.Errorf("read result must contain one text block: %+v", readResult)
			} else if block, ok := content[0].(map[string]any); !ok || block["type"] != "text" || block["text"] != wantReadText {
				t.Errorf("read result text = %+v, want %q", content[0], wantReadText)
			}
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"STANDALONE_DONE\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		}
		if len(calls) > 0 {
			for i, call := range calls {
				block, _ := json.Marshal(map[string]any{"index": i, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("synthetic_call_%d_%d", turn, i), "name": call.name, "input": call.args}})
				fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\nevent: content_block_stop\ndata: {\"index\":%d}\n\n", block, i)
			}
			fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
		}
		fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, server.URL)
	// Only psi and bash exist on the child PATH. The workspace has no reference
	// checkout, Node executable, extensions, or network catalog implementation.
	q := startReviewProcess(t, home, work, agentDir, copied, bin, "", "--deny-project", "--session-dir", filepath.Join(home, "sessions"), "--tools", "read,write,edit,bash")
	q.prompt()
	q.write("complete synthetic standalone task\r")
	q.wait("STANDALONE_DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	if requests.Load() != 4 {
		t.Fatalf("provider requests: %d", requests.Load())
	}
	script, err := os.ReadFile(filepath.Join(work, "standalone.sh"))
	if err != nil || string(script) != finalContent {
		t.Fatal("write/read/edit/bash task did not complete", err)
	}
	if strings.Contains(q.raw.String(), "SYNTHETIC_TOOL_BODY_FIXED") {
		t.Fatal("tool body leaked into terminal")
	}
}
