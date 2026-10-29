package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"psi/internal/event"
)

func TestSummarizeArgumentsBoundsBodiesAndUsesLexicalWorkspacePaths(t *testing.T) {
	body := strings.Repeat("hidden-body-canary", 30_000)
	summary := SummarizeArguments("write", map[string]any{"path": "/workspace/src/file.go", "content": body}, "/workspace", "/home/test")
	if summary.Path != "src/file.go" || summary.Kind != "write" || !summary.Available {
		t.Fatalf("summary=%+v", summary)
	}
	encoded, err := json.Marshal(event.Event{Type: event.ToolStart, Summary: summary})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > displaySummaryBytes+128 || strings.Contains(string(encoded), "hidden-body-canary") {
		t.Fatalf("hidden body entered display event (%d bytes): %.300s", len(encoded), encoded)
	}
}

func TestSummarizeReadRangeAndHomePath(t *testing.T) {
	read := SummarizeArguments("read", map[string]any{"path": "/workspace/file.go", "offset": json.Number("4"), "limit": json.Number("3")}, "/workspace", "/home/test")
	if read.Path != "file.go" || read.LineRange != ":4-6" {
		t.Fatalf("read summary=%+v", read)
	}
	home := SummarizeArguments("edit", map[string]any{"path": "/home/test/private/file.go"}, "/workspace", "/home/test")
	if home.Path != "~/private/file.go" {
		t.Fatalf("home path=%q", home.Path)
	}
}

func TestSummarizeCommandRedactsCredentialsBeforeClipping(t *testing.T) {
	secret := "must-not-appear-anywhere"
	command := `curl -H "Authorization: Bearer ` + secret + `" --api-key='` + secret + `' https://alice:` + secret + `@example.test/path?token=` + secret + "\nsecond line"
	summary := SummarizeArguments("bash", map[string]any{"command": command}, "/workspace", "/home/test")
	if strings.Contains(summary.Command, secret) || strings.Contains(summary.Command, "alice:") {
		t.Fatalf("command preview leaked a credential: %q", summary.Command)
	}
	if !strings.Contains(summary.Command, "[REDACTED]") || !strings.Contains(summary.Command, "…") {
		t.Fatalf("expected redaction and continuation marker: %q", summary.Command)
	}
}

func TestSummarizeHugeCommandRetainsOnlyBoundedPreview(t *testing.T) {
	command := "TOKEN='" + strings.Repeat("secret", 200_000) + "'"
	summary := SummarizeArguments("bash", map[string]any{"command": command}, "", "")
	if len(summary.Command) > displaySummaryBytes || strings.Contains(summary.Command, "secret") || !strings.Contains(summary.Command, "[REDACTED]") {
		t.Fatalf("unsafe huge command summary length=%d value=%q", len(summary.Command), summary.Command)
	}
}

func TestSummarizeArgumentsJSONSkipsHiddenBodiesAndBoundsHugeFields(t *testing.T) {
	body := strings.Repeat("hidden-json-body-canary", 30_000)
	raw := []byte(`{"path":"/workspace/src/result.go","content":"` + body + `"}`)
	summary := SummarizeArgumentsJSON("write", raw, "/workspace", "/home/test")
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Path != "src/result.go" || len(encoded) > displaySummaryBytes+128 || strings.Contains(string(encoded), "hidden-json-body-canary") {
		t.Fatalf("bounded JSON summary=%+v (%d bytes)", summary, len(encoded))
	}
	longPath := strings.Repeat("nested/", 2_000) + "filename.go"
	pathJSON, err := json.Marshal(map[string]any{"path": longPath, "oldText": body, "newText": body})
	if err != nil {
		t.Fatal(err)
	}
	pathSummary := SummarizeArgumentsJSON("edit", pathJSON, "/workspace", "/home/test")
	if len(pathSummary.Path) > displaySummaryBytes || !strings.HasSuffix(pathSummary.Path, "filename.go") || !pathSummary.Truncated {
		t.Fatalf("huge path summary=%+v", pathSummary)
	}
}

func TestSummarizeArgumentsJSONRedactsBeforePreviewClipping(t *testing.T) {
	secret := "giant-json-secret-canary"
	command, err := json.Marshal(map[string]any{"command": "API_KEY=" + secret + strings.Repeat("x", 20_000)})
	if err != nil {
		t.Fatal(err)
	}
	summary := SummarizeArgumentsJSON("bash", command, "", "")
	if strings.Contains(summary.Command, secret) || !strings.Contains(summary.Command, "[REDACTED]") || !summary.Truncated {
		t.Fatalf("JSON command summary=%+v", summary)
	}
}
