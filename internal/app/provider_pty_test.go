//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"psi/internal/session"
)

func TestActualAppPTYProviderToolCycleAndRestart(t *testing.T) {
	cases := []struct {
		provider, api, basePath string
	}{
		{"minimax", "anthropic-messages", "/anthropic"},
		{"opencode-go", "openai-responses", "/inference/go/openai/v1"},
		{"deepseek", "openai-completions", ""},
		{"zai", "openai-completions", ""},
		{"google", "google-generative-ai", "/v1beta"},
	}
	for _, test := range cases {
		t.Run(test.provider, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			agentDir := filepath.Join(home, "agent")
			if err := os.WriteFile(filepath.Join(work, "fixture.txt"), []byte("SYNTHETIC_READ_RESULT"), 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turn := requests.Add(1)
				if strings.Contains(r.URL.String(), "synthetic-provider-key") {
					t.Error("provider credential appeared in request URL")
				}
				if path := providerPTYExpectedPath(test.provider, test.api); r.URL.Path != path {
					t.Errorf("%s endpoint path = %q, want %q", test.provider, r.URL.Path, path)
				}
				if !providerPTYAuthOK(test.provider, r) {
					t.Errorf("%s auth header = %#v", test.provider, r.Header)
				}
				if test.provider == "opencode-go" && r.Header.Get("X-OpenCode-Session") == "" {
					t.Error("OpenCode Go session header missing")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode %s request: %v", test.provider, err)
					return
				}
				encodedBody, _ := json.Marshal(body)
				if strings.Contains(string(encodedBody), "synthetic-provider-key") {
					t.Error("provider credential appeared in request body")
				}
				if turn >= 2 {
					encoded, _ := json.Marshal(body)
					if !strings.Contains(string(encoded), "SYNTHETIC_READ_RESULT") {
						t.Errorf("%s continuation did not replay tool output: %s", test.provider, encoded)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, providerPTYResponse(test.api, int(turn)))
			}))
			defer server.Close()

			baseURL := strings.TrimRight(server.URL+test.basePath, "/")
			compat := ""
			if test.provider == "opencode-go" {
				compat = `,"sessionAffinityFormat":"openai-nosession","supportsMidConvoSystemMessages":true,"supportsAdditionalTools":true`
			}
			settings := fmt.Sprintf(`{"defaultProvider":%q,"defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`, test.provider)
			models := fmt.Sprintf(`{"providers":{%q:{"models":[{"id":"synthetic-model","api":%q,"baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"],"compat":{%s}}]}}}`, test.provider, test.api, baseURL, strings.TrimPrefix(compat, ","))
			if compat == "" {
				models = fmt.Sprintf(`{"providers":{%q:{"models":[{"id":"synthetic-model","api":%q,"baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, test.provider, test.api, baseURL)
			}
			writeAppFile(t, filepath.Join(agentDir, "settings.json"), settings)
			writeAppFile(t, filepath.Join(agentDir, "models.json"), models)
			writeAppFile(t, filepath.Join(agentDir, "auth.json"), fmt.Sprintf(`{%q:{"type":"api_key","key":"synthetic-provider-key"}}`, test.provider))

			sessionDir := filepath.Join(home, "sessions")
			store, err := session.New(session.OpenOptions{Cwd: work, AgentDir: agentDir, SessionDir: sessionDir, Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendModelChange(test.provider, "synthetic-model"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendThinkingLevelChange("off"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(agentMessageUser("synthetic prior session context")); err != nil {
				t.Fatal(err)
			}
			sessionFile := store.File()
			if _, err := os.Stat(sessionFile); err != nil {
				t.Fatalf("synthetic session file was not created: %s: %v", sessionFile, err)
			}
			store.Unlock()

			args := []string{"--deny-project", "--session", sessionFile}
			binary := *reviewBinary
			first := startReviewProcess(t, home, work, agentDir, binary, "", args...)
			first.prompt()
			first.write("read fixture.txt\r")
			first.wait("PROVIDER_PTY_DONE")
			first.prompt()
			first.write("/quit\r")
			first.finish()
			if requests.Load() != 2 {
				t.Fatalf("first %s session used %d requests; want tool call + answer", test.provider, requests.Load())
			}

			second := startReviewProcess(t, home, work, agentDir, binary, "", args...)
			second.prompt()
			second.write("continue after restart\r")
			second.wait("PROVIDER_RESTART_DONE")
			second.prompt()
			second.write("/quit\r")
			second.finish()
			if requests.Load() != 3 {
				t.Fatalf("restarted %s session used %d requests; want one continuation", test.provider, requests.Load())
			}
			if strings.Contains(first.raw.String(), "synthetic-provider-key") || strings.Contains(second.raw.String(), "synthetic-provider-key") {
				t.Fatal("provider credential leaked to an app PTY")
			}
			sessionData, err := os.ReadFile(sessionFile)
			if err != nil || strings.Contains(string(sessionData), "synthetic-provider-key") {
				t.Fatalf("provider credential leaked to session file: %v", err)
			}
		})
	}
}

func providerPTYExpectedPath(provider, api string) string {
	switch api {
	case "anthropic-messages":
		if provider == "opencode-go" {
			return "/inference/go/anthropic/v1/messages"
		}
		return "/anthropic/v1/messages"
	case "openai-responses":
		return "/inference/go/openai/v1/responses"
	case "openai-completions":
		return "/chat/completions"
	case "google-generative-ai":
		return "/v1beta/models/synthetic-model:streamGenerateContent"
	default:
		return ""
	}
}

func providerPTYAuthOK(provider string, r *http.Request) bool {
	switch provider {
	case "minimax", "opencode-go":
		if r.URL.Path == "/inference/go/openai/v1/responses" {
			return r.Header.Get("Authorization") == "Bearer synthetic-provider-key"
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			return r.Header.Get("x-api-key") == "synthetic-provider-key"
		}
		return r.Header.Get("Authorization") == "Bearer synthetic-provider-key"
	case "google":
		return r.Header.Get("x-goog-api-key") == "synthetic-provider-key"
	default:
		return r.Header.Get("Authorization") == "Bearer synthetic-provider-key"
	}
}

func providerPTYResponse(api string, turn int) string {
	answer := "PROVIDER_PTY_DONE"
	if turn >= 3 {
		answer = "PROVIDER_RESTART_DONE"
	}
	first := turn == 1
	if api == "anthropic-messages" {
		if first {
			return "event: message_start\ndata: {\"message\":{\"id\":\"pty-1\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n" +
				"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"pty-read\",\"name\":\"read\",\"input\":{\"path\":\"fixture.txt\"}}}\n\n" +
				"event: content_block_stop\ndata: {\"index\":0}\n\n" +
				"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n" + "event: message_stop\ndata: {}\n\n"
		}
		return anthropicPTYText(answer)
	}
	if api == "openai-completions" {
		if first {
			return "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"pty-read\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"fixture.txt\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
		}
		return fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", answer)
	}
	if api == "google-generative-ai" {
		if first {
			return "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"pty-read\",\"name\":\"read\",\"args\":{\"path\":\"fixture.txt\"}}}]},\"finishReason\":\"STOP\"}]}\n\n"
		}
		return fmt.Sprintf("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]},\"finishReason\":\"STOP\"}]}\n\n", answer)
	}
	if first {
		return responsesPTYEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_pty", "call_id": "call_pty", "name": "read", "arguments": `{"path":"fixture.txt"}`}}) +
			responsesPTYEvent("response.completed", map[string]any{"response": map[string]any{"id": "pty-1", "model": "synthetic-model", "status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_pty", "call_id": "call_pty", "name": "read", "arguments": `{"path":"fixture.txt"}`}}}})
	}
	return responsesPTYEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_pty", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": answer}}}}) +
		responsesPTYEvent("response.completed", map[string]any{"response": map[string]any{"id": "pty-2", "model": "synthetic-model", "status": "completed", "output": []any{}}})
}

func anthropicPTYText(text string) string {
	return "event: message_start\ndata: {\"message\":{\"id\":\"pty-answer\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" + "event: message_stop\ndata: {}\n\n"
}

func responsesPTYEvent(event string, payload map[string]any) string {
	payload["type"] = event
	encoded, _ := json.Marshal(payload)
	return "event: " + event + "\ndata: " + string(encoded) + "\n\n"
}
