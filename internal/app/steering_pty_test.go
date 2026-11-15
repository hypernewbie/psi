//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/session"
)

// Pi's editor stays active while the agent runs. Enter queues a steering
// message that is delivered at the next turn boundary and rendered as a normal
// user message; Escape cancels and restores undelivered steering to the editor.
func TestActualAppSteeringWhileRunning(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requests atomic.Int32
	var bodiesMu sync.Mutex
	var bodies []string
	releaseCh := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseCh) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodiesMu.Lock()
		bodies = append(bodies, string(raw))
		bodiesMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		index := int(requests.Add(1))
		text := map[int]string{1: "FIRST_REPLY", 2: "AFTER_STEER", 3: "AFTER_SKILL_STEER", 4: "AFTER_FOLLOWUP", 5: "AFTER_SKILL_FOLLOWUP"}[index]
		if text == "" {
			text = "EXTRA_REPLY"
		}
		emit("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("steer-%d", index), "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if index == 1 {
			// Hold the stream open until every queued row proves admission, or
			// until the client cancels the request (including test failure).
			select {
			case <-releaseCh:
			case <-r.Context().Done():
				return
			}
		}
		emit("content_block_stop", map[string]any{"index": 0})
		emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	defer release()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	steerSkill := filepath.Join(agentDir, "skills", "qsteer", "SKILL.md")
	followSkill := filepath.Join(agentDir, "skills", "qfollow", "SKILL.md")
	writeAppFile(t, steerSkill, "---\nname: qsteer\ndescription: Queued steering skill\n---\nQSTEER_BODY\n")
	writeAppFile(t, followSkill, "---\nname: qfollow\ndescription: Queued follow-up skill\n---\nQFOLLOW_BODY\n")
	wantSteerSkill := "<skill name=\"qsteer\" location=\"" + steerSkill + "\">\n" +
		"References are relative to " + filepath.Dir(steerSkill) + ".\n\nQSTEER_BODY\n</skill>\n\nsteer-args"
	wantFollowSkill := "<skill name=\"qfollow\" location=\"" + followSkill + "\">\n" +
		"References are relative to " + filepath.Dir(followSkill) + ".\n\nQFOLLOW_BODY\n</skill>\n\nfollow-args"
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("ASK_UX\r")
	q.wait("FIRST_REPLY")
	// An unknown busy skill restores the draft without queue, store, or provider
	// effects; editor deletion, not Ctrl+C, clears it.
	q.write("/skill:missing nope\r")
	q.waitScreen("skill \"missing\" not found")
	q.waitScreen("/skill:missing nope")
	if requests.Load() != 1 {
		t.Fatalf("unknown busy skill reached the provider: %d requests", requests.Load())
	}
	q.write(strings.Repeat("\x7f", len("/skill:missing nope")))
	// Queue ordinary steering, ordinary follow-up, skill steering, then skill
	// follow-up while response one waits.
	q.write("STEER_ME\r")
	q.wait("Steering: STEER_ME")
	q.write("FOLLOW_ME\x1b\r")
	q.wait("Follow-up: FOLLOW_ME")
	q.write("/skill:qsteer steer-args\r")
	q.waitScreen("Steering: <skill name=\"qsteer\"")
	q.write("/skill:qfollow follow-args\x1b\r")
	q.waitScreen("Follow-up: <skill name=\"qfollow\"")
	// Admission is proven by all four pending rows; only then release the run.
	release()
	q.wait("AFTER_STEER")
	q.wait("AFTER_SKILL_STEER")
	q.wait("AFTER_FOLLOWUP")
	q.wait("AFTER_SKILL_FOLLOWUP")
	q.prompt()
	rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "› STEER_ME") || !strings.Contains(joined, "› FOLLOW_ME") {
		t.Fatalf("delivered messages were not rendered as user messages: %q", rows)
	}
	for _, want := range []string{
		"[skill] qsteer (ctrl+o to expand)", "› steer-args",
		"[skill] qfollow (ctrl+o to expand)", "› follow-args",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("delivered skill group missing %q: %q", want, rows)
		}
	}
	if strings.Contains(q.screen.Text(), "Steering:") || strings.Contains(q.screen.Text(), "Follow-up:") {
		t.Fatalf("delivered queued rows were not cleared: %q", q.screen.Text())
	}
	if requests.Load() != 5 {
		t.Fatalf("provider requests = %d, want initial plus four queued turns", requests.Load())
	}
	bodiesMu.Lock()
	gotBodies := append([]string(nil), bodies...)
	bodiesMu.Unlock()
	lastUser := func(raw string) string {
		t.Helper()
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("decode provider body: %v", err)
		}
		for i := len(body.Messages) - 1; i >= 0; i-- {
			if body.Messages[i].Role != "user" {
				continue
			}
			if text, ok := body.Messages[i].Content.(string); ok {
				return text
			}
			var builder strings.Builder
			blocks, _ := body.Messages[i].Content.([]any)
			for _, rawBlock := range blocks {
				block, _ := rawBlock.(map[string]any)
				if block["type"] == "text" {
					value, _ := block["text"].(string)
					builder.WriteString(value)
				}
			}
			return builder.String()
		}
		return ""
	}
	for i, want := range []string{"seedASK_UX", "STEER_ME", wantSteerSkill, "FOLLOW_ME", wantFollowSkill} {
		// Adjacent user messages merge on the wire, so the seeded turn and the
		// first ask share one provider message.
		if got := lastUser(gotBodies[i]); got != want {
			t.Fatalf("request %d last user = %q, want %q", i+1, got, want)
		}
	}
	q.write("/quit\r")
	q.finish()
	// The delivered skill messages and their invocations are durable.
	s, err := session.OpenFile(store.File(), work, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	provenance := map[string]string{}
	for _, entry := range s.Entries() {
		if entry.Kind == "custom" && entry.Custom != nil && entry.Custom.CustomType == "psi.input" {
			var p struct {
				TargetID   string `json:"targetId"`
				Invocation string `json:"invocation"`
			}
			if json.Unmarshal(entry.Custom.Data, &p) == nil {
				provenance[p.TargetID] = p.Invocation
			}
		}
	}
	storedSkills := 0
	for _, entry := range s.Entries() {
		if entry.Kind != "message" || entry.Message == nil || entry.Message.Message.User == nil {
			continue
		}
		content, _ := entry.Message.Message.User.Content.(string)
		wantInvocation := ""
		switch content {
		case wantSteerSkill:
			wantInvocation = "/skill:qsteer steer-args"
		case wantFollowSkill:
			wantInvocation = "/skill:qfollow follow-args"
		default:
			continue
		}
		storedSkills++
		if got := provenance[entry.ID()]; got != wantInvocation {
			t.Fatalf("stored provenance = %q, want %q", got, wantInvocation)
		}
	}
	if storedSkills != 2 {
		t.Fatalf("stored skill users = %d, want steering and follow-up", storedSkills)
	}
	for _, entry := range s.Entries() {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil {
			content, _ := entry.Message.Message.User.Content.(string)
			if strings.Contains(content, "/skill:missing") {
				t.Fatalf("refused busy skill was stored: %q", content)
			}
		}
	}
}

func TestActualAppCancelRestoresQueuedMessagesToEditor(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	writeAppFile(t, filepath.Join(agentDir, "skills", "cskill", "SKILL.md"), "---\nname: cskill\ndescription: Cancelled steering skill\n---\nCSKILL_BODY\n")
	writeAppFile(t, filepath.Join(agentDir, "skills", "cfollow", "SKILL.md"), "---\nname: cfollow\ndescription: Cancelled follow-up skill\n---\nCFOLLOW_BODY\n")
	started := make(chan struct{}, 1)
	releaseCh := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseCh) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		emit("message_start", map[string]any{"message": map[string]any{"id": "slow", "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "SLOW_START"}})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case started <- struct{}{}:
		default:
		}
		// The run ends only on cancellation; release is failure-safe cleanup.
		select {
		case <-releaseCh:
		case <-r.Context().Done():
			return
		}
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	defer release()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--session", store.File())
	q.prompt()
	q.write("SLOW_ASK\r")
	select {
	case <-started:
	case <-time.After(8 * time.Second):
		t.Fatal("first provider request did not start")
	}
	q.write("CANCEL_STEER\r")
	q.wait("Steering: CANCEL_STEER")
	q.write("/skill:cskill steer-args\r")
	q.waitScreen("Steering: <skill name=\"cskill\"")
	q.write("CANCEL_FOLLOWUP\x1b\r")
	q.wait("Follow-up: CANCEL_FOLLOWUP")
	q.write("/skill:cfollow follow-args\x1b\r")
	q.waitScreen("Follow-up: <skill name=\"cfollow\"")
	q.write("CANCEL_DRAFT")
	q.waitScreen("CANCEL_DRAFT")
	q.write("\x1b")
	q.wait("interrupted")
	q.waitScreen("CANCEL_STEER")
	q.waitScreen("/skill:cskill steer-args")
	q.waitScreen("CANCEL_FOLLOWUP")
	q.waitScreen("/skill:cfollow follow-args")
	q.waitScreen("CANCEL_DRAFT")
	q.prompt()
	draft := q.screen.Text()
	order := []string{"CANCEL_STEER", "/skill:cskill steer-args", "CANCEL_FOLLOWUP", "/skill:cfollow follow-args", "CANCEL_DRAFT"}
	last := -1
	for _, want := range order {
		i := strings.Index(draft, want)
		if i < 0 || i < last {
			t.Fatalf("recovery order lost %q: %q", want, draft)
		}
		last = i
	}
	if strings.Contains(draft, "Steering:") || strings.Contains(draft, "Follow-up:") {
		t.Fatalf("cancelled messages kept queued rows: %q", draft)
	}
	q.write("\x03")
	q.write("/quit\r")
	q.finish()
	// Cancelled skill queues are neither persisted nor given provenance.
	s, err := session.OpenFile(store.File(), work, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	for _, entry := range s.Entries() {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil {
			content, _ := entry.Message.Message.User.Content.(string)
			if strings.Contains(content, "<skill name=\"cskill\"") || strings.Contains(content, "<skill name=\"cfollow\"") {
				t.Fatalf("cancelled skill was persisted: %q", content)
			}
		}
		if entry.Kind == "custom" && entry.Custom != nil && entry.Custom.CustomType == "psi.input" {
			var p struct {
				Invocation string `json:"invocation"`
			}
			if json.Unmarshal(entry.Custom.Data, &p) == nil && (strings.Contains(p.Invocation, "cskill") || strings.Contains(p.Invocation, "cfollow")) {
				t.Fatalf("cancelled skill kept provenance: %q", p.Invocation)
			}
		}
	}
}

func TestActualAppSteeringDuringManualCompaction(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		requestNumber := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Keep the manual summary request open long enough to queue editor input.
		time.Sleep(500 * time.Millisecond)
		text := "MANUAL_COMPACT_CONTINUED"
		if requestNumber == 1 {
			text = "MANUAL_COMPACT_SUMMARY"
		}
		emit("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("manual-%d", requestNumber), "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		emit("content_block_stop", map[string]any{"index": 0})
		emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false,"keepRecentTokens":50},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":22000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("prior ", 6000)}}}}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("/compact\r")
	q.wait("Compacting context")
	q.write("STEER_MANUAL_COMPACT\r")
	q.wait("Steering: STEER_MANUAL_COMPACT")
	q.wait("MANUAL_COMPACT_CONTINUED")
	q.prompt()
	rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "› STEER_MANUAL_COMPACT") {
		t.Fatalf("queued message was not delivered after manual compaction: %q", rows)
	}
	mu.Lock()
	if len(bodies) != 2 {
		t.Fatalf("provider requests = %d, want summary and post-compaction continuation", len(bodies))
	}
	last := bodies[1]
	mu.Unlock()
	if !strings.Contains(last, "STEER_MANUAL_COMPACT") {
		t.Fatalf("post-compaction request lost queued input: %s", last)
	}
	q.write("/quit\r")
	q.finish()
}

// Compaction runs inside a busy run: the editor stays active, and a steering
// message queued while compaction streams is delivered at the next turn
// boundary, after the summary, before the next model request.
func TestActualAppSteeringDuringCompaction(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(500 * time.Millisecond)
		emit("message_start", map[string]any{"message": map[string]any{"id": "c", "model": "m", "usage": map[string]any{"input_tokens": 1}}})
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "AFTER_COMPACT"}})
		emit("content_block_stop", map[string]any{"index": 0})
		emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
		emit("message_stop", map[string]any{})
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":true,"reserveTokens":15000,"keepRecentTokens":50},"outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":22000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("prior ", 6000)}}}}}); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("ASK_COMPACT\r")
	q.wait("Auto-compacting")
	q.write("STEER_COMPACT\r")
	q.wait("Steering: STEER_COMPACT")
	q.wait("AFTER_COMPACT")
	q.prompt()
	rows := append(append([]string{}, q.screen.Scrollback...), strings.Split(q.screen.Text(), "\n")...)
	if !strings.Contains(strings.Join(rows, "\n"), "› STEER_COMPACT") {
		t.Fatalf("steering queued during compaction was never delivered: %q", rows)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("provider requests = %d, want 2 (one compaction summary and one post-compaction request with steering)", len(bodies))
	}
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, "STEER_COMPACT") || !strings.Contains(last, "ASK_COMPACT") {
		t.Fatalf("final request lost the steered message or the ask: %q", last)
	}
	q.write("/quit\r")
	q.finish()
}
