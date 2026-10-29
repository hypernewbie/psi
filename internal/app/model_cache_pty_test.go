//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/session"
)

func TestResumedCodexModelFromPiCacheCanSubmit(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "synthetic-new-codex" {
			t.Errorf("wrong requested model: %v", body["model"])
		}
		if r.Header.Get("Authorization") != "Bearer dummy-test-access" {
			t.Error("missing synthetic OAuth credential")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, fields map[string]any) {
			fields["type"] = name
			data, _ := json.Marshal(fields)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		}
		message := map[string]any{"type": "message", "id": "msg_synthetic", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "CACHE_MODEL_OK"}}}
		emit("response.output_item.added", map[string]any{"output_index": 0, "item": message})
		emit("response.output_text.delta", map[string]any{"output_index": 0, "delta": "CACHE_MODEL_OK"})
		emit("response.output_item.done", map[string]any{"output_index": 0, "item": message})
		emit("response.completed", map[string]any{"response": map[string]any{"id": "resp_synthetic", "status": "completed", "output": []any{message}}})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"openai-codex":{"baseUrl":"`+server.URL+`"}}}`)
	writeAppFile(t, filepath.Join(agentDir, "models-store.json"), `{"openai-codex":{"lastModified":4102444800000,"models":[{"id":"synthetic-new-codex","provider":"openai-codex","api":"openai-codex-responses","type":"chat","contextWindow":500000,"maxTokens":128000,"reasoning":true,"input":["text","image"],"thinkingLevelMap":{"off":"none","xhigh":"xhigh"}}]}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), fmt.Sprintf(`{"openai-codex":{"type":"oauth","access":"dummy-test-access","refresh":"dummy-test-refresh","accountId":"test-account","expires":%d}}`, time.Now().Add(time.Hour).UnixMilli()))
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendModelChange("openai-codex", "synthetic-new-codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("earlier synthetic prompt")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("next synthetic prompt\r")
	q.wait("CACHE_MODEL_OK")
	q.prompt()
	q.write("/quit\r")
	q.finish()
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
}
