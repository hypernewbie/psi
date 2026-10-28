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
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		turn := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
		if turn == 1 {
			calls := []struct {
				name string
				args map[string]any
			}{
				{"write", map[string]any{"path": "standalone.sh", "content": "printf 'broken\\n'\n"}},
				{"read", map[string]any{"path": "standalone.sh"}},
				{"edit", map[string]any{"path": "standalone.sh", "oldText": "broken", "newText": "SYNTHETIC_TOOL_BODY_FIXED"}},
				{"bash", map[string]any{"command": "bash standalone.sh"}},
			}
			for i, call := range calls {
				block, _ := json.Marshal(map[string]any{"index": i, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("synthetic_call_%d", i), "name": call.name, "input": call.args}})
				fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\nevent: content_block_stop\ndata: {\"index\":%d}\n\n", block, i)
			}
			fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
		} else {
			if turn != 2 {
				t.Error("unexpected provider retry")
			}
			messages := request["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			results := last["content"].([]any)
			if len(results) != 4 {
				t.Errorf("four tool results not grouped: %+v", results)
			}
			for _, entry := range results {
				if result := entry.(map[string]any); result["type"] != "tool_result" || result["is_error"] != false {
					t.Errorf("tool failed: %+v", result)
				}
			}
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"STANDALONE_DONE\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		}
		fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, server.URL)
	// Only psi and bash exist on the child PATH. The workspace has no reference
	// checkout, Node executable, extensions, or network catalog implementation.
	q := startReviewProcess(t, home, work, agentDir, copied, bin, "--deny-project", "--session-dir", filepath.Join(home, "sessions"), "--tools", "read,write,edit,bash")
	q.prompt()
	q.write("complete synthetic standalone task\r")
	q.wait("STANDALONE_DONE")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	if requests.Load() != 2 {
		t.Fatalf("provider requests: %d", requests.Load())
	}
	script, err := os.ReadFile(filepath.Join(work, "standalone.sh"))
	if err != nil || !strings.Contains(string(script), "SYNTHETIC_TOOL_BODY_FIXED") {
		t.Fatal("write/read/edit/bash task did not complete", err)
	}
	if strings.Contains(q.raw.String(), "SYNTHETIC_TOOL_BODY_FIXED") {
		t.Fatal("tool body leaked into terminal")
	}
}
