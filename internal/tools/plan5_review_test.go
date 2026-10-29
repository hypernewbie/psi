package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewCommandPreviewRedactsBasicAuthorization(t *testing.T) {
	const secret = "dXNlcjpwYXNzd29yZA=="
	command := `curl -H "Authorization: Basic ` + secret + `" https://example.test`
	raw, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range []string{
		SummarizeArguments("bash", map[string]any{"command": command}, "", "").Command,
		SummarizeArgumentsJSON("bash", raw, "", "").Command,
	} {
		if strings.Contains(summary, secret) || !strings.Contains(summary, "[REDACTED]") {
			t.Errorf("Basic authorization leaked in preview: %q", summary)
		}
	}
}

func TestReviewLegacyToolPathDisplayDoesNotLoadBodies(t *testing.T) {
	const raw = `{"file_path":"src/legacy.go","content":"hidden-write-body","oldText":"hidden-edit-body"}`
	for _, name := range []string{"read", "write", "edit"} {
		summary := SummarizeArgumentsJSON(name, []byte(raw), "", "")
		if summary.Path != "src/legacy.go" {
			t.Errorf("%s legacy summary=%+v", name, summary)
		}
		encoded, err := json.Marshal(summary)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "hidden-") {
			t.Fatalf("body leaked: %s", encoded)
		}
	}
	summary := SummarizeArguments("read", map[string]any{"path": "actual.go", "file_path": "ignored.go"}, "", "")
	if summary.Path != "actual.go" {
		t.Fatalf("summary did not use execution path: %+v", summary)
	}
}
