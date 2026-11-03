package session

import "testing"

func appendUsageMessage(t *testing.T, store *Store, usage Usage, stop string) string {
	t.Helper()
	id, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", StopReason: stop, Usage: usage,
		Content: []ContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLastContextTokens(t *testing.T) {
	t.Run("no assistant", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := store.LastContextTokens(); ok {
			t.Fatal("empty store returned usage")
		}
	})

	t.Run("assistant usage", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{TotalTokens: 42}, "stop")
		tokens, ok := store.LastContextTokens()
		if !ok || tokens != 42 {
			t.Fatalf("tokens=%d ok=%v", tokens, ok)
		}
	})

	t.Run("before compaction ignored", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		userID, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi"}})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{TotalTokens: 42}, "stop")
		if _, err := store.AppendCompaction("summary", userID, 100, nil); err != nil {
			t.Fatal(err)
		}
		if tokens, ok := store.LastContextTokens(); ok {
			t.Fatalf("pre-compaction usage returned: %d", tokens)
		}
	})

	t.Run("after compaction accepted", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		userID, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendCompaction("summary", userID, 100, nil); err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{TotalTokens: 77}, "stop")
		tokens, ok := store.LastContextTokens()
		if !ok || tokens != 77 {
			t.Fatalf("tokens=%d ok=%v", tokens, ok)
		}
	})

	t.Run("aborted skipped", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{TotalTokens: 10}, "stop")
		appendUsageMessage(t, store, Usage{TotalTokens: 99}, "aborted")
		tokens, ok := store.LastContextTokens()
		if !ok || tokens != 10 {
			t.Fatalf("tokens=%d ok=%v", tokens, ok)
		}
	})

	t.Run("zero usage skipped", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{}, "stop")
		if tokens, ok := store.LastContextTokens(); ok {
			t.Fatalf("zero usage returned: %d", tokens)
		}
	})

	t.Run("total tokens wins", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{TotalTokens: 42, Input: 100}, "stop")
		tokens, ok := store.LastContextTokens()
		if !ok || tokens != 42 {
			t.Fatalf("tokens=%d ok=%v", tokens, ok)
		}
	})

	t.Run("dropped by context edit", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{TotalTokens: 50}, "stop")
		dropped := appendUsageMessage(t, store, Usage{TotalTokens: 90}, "stop")
		if _, err := store.AppendContextEdit(dropped, nil); err != nil {
			t.Fatal(err)
		}
		tokens, ok := store.LastContextTokens()
		if !ok || tokens != 50 {
			t.Fatalf("tokens=%d ok=%v", tokens, ok)
		}
	})

	t.Run("component sum", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{Input: 10, Output: 5, CacheRead: 3, CacheWrite: 2}, "stop")
		tokens, ok := store.LastContextTokens()
		if !ok || tokens != 20 {
			t.Fatalf("tokens=%d ok=%v", tokens, ok)
		}
	})
}
