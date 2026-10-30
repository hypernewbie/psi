package tools

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestSummarizeArgumentsSurrogatePairsAndLoneSurrogates(t *testing.T) {
	// 1. Valid surrogate pair: \uD83D\uDE00 -> 😀 (U+1F600)
	raw := []byte(`{"command":"echo \uD83D\uDE00"}`)
	s := SummarizeArgumentsJSON("bash", raw, "", "")
	if !strings.Contains(s.Command, "😀") {
		t.Fatalf("expected surrogate pair to decode to 😀, got %q", s.Command)
	}
	if !utf8.ValidString(s.Command) {
		t.Fatalf("command preview contains invalid UTF-8: %q", s.Command)
	}

	// Another valid pair: \uD83C\uDF89 -> 🎉 (party popper)
	rawParty := []byte(`{"path":"src/\uD83C\uDF89.go"}`)
	sParty := SummarizeArgumentsJSON("read", rawParty, "", "")
	if !strings.Contains(sParty.Path, "🎉") {
		t.Fatalf("expected surrogate pair to decode to 🎉, got %q", sParty.Path)
	}

	// 2. Lone high surrogate: \uD83D not followed by low surrogate
	rawLoneHigh := []byte(`{"command":"echo \uD83D alone"}`)
	sHigh := SummarizeArgumentsJSON("bash", rawLoneHigh, "", "")
	if !utf8.ValidString(sHigh.Command) {
		t.Fatalf("lone high surrogate produced invalid UTF-8: %q", sHigh.Command)
	}

	// 3. Lone low surrogate: \uDE00 alone
	rawLoneLow := []byte(`{"command":"echo \uDE00 alone"}`)
	sLow := SummarizeArgumentsJSON("bash", rawLoneLow, "", "")
	if !utf8.ValidString(sLow.Command) {
		t.Fatalf("lone low surrogate produced invalid UTF-8: %q", sLow.Command)
	}

	// 4. Incomplete escape at the end
	rawIncomplete := []byte(`{"command":"echo \u12"}`)
	sIncomp := SummarizeArgumentsJSON("bash", rawIncomplete, "", "")
	_ = sIncomp
}

func TestSummarizeArgumentsInvalidUTF8(t *testing.T) {
	invalidSequences := [][]byte{
		[]byte("\xff\xfe"),
		[]byte("hello \xc0\xaf world"),
		[]byte("trunc \xf0\x9f\x98"), // cut 4-byte UTF-8
	}

	for _, invalid := range invalidSequences {
		// In SummarizeArguments
		s := SummarizeArguments("bash", map[string]any{"command": string(invalid)}, "", "")
		if !utf8.ValidString(s.Command) {
			t.Fatalf("invalid UTF-8 in summary command: %q", s.Command)
		}

		// In SummarizeArgumentsJSON
		jsonPayload := append([]byte(`{"command":"`), invalid...)
		jsonPayload = append(jsonPayload, []byte(`"}`)...)
		sJSON := SummarizeArgumentsJSON("bash", jsonPayload, "", "")
		if !utf8.ValidString(sJSON.Command) {
			t.Fatalf("invalid UTF-8 from JSON scanner: %q", sJSON.Command)
		}
	}
}

func TestSummarizeArgumentsDeeplyNestedObjects(t *testing.T) {
	// Deeply nested objects: 100 levels
	var nest strings.Builder
	for i := 0; i < 100; i++ {
		nest.WriteString(`{"a":`)
	}
	nest.WriteString(`"target"`)
	for i := 0; i < 100; i++ {
		nest.WriteString(`}`)
	}

	// Must not stack overflow or panic
	s := SummarizeArgumentsJSON("read", []byte(nest.String()), "", "")
	_ = s

	// Deeply nested arrays
	var arr strings.Builder
	for i := 0; i < 100; i++ {
		arr.WriteString(`[`)
	}
	arr.WriteString(`1`)
	for i := 0; i < 100; i++ {
		arr.WriteString(`]`)
	}
	s2 := SummarizeArgumentsJSON("bash", []byte(arr.String()), "", "")
	_ = s2
}

func TestSummarizeArgumentsHugeStrings(t *testing.T) {
	// 5 MB command argument
	hugeCommand := "echo " + strings.Repeat("x", 5*1024*1024)
	rawJSON := []byte(`{"command":"` + hugeCommand + `"}`)
	s := SummarizeArgumentsJSON("bash", rawJSON, "", "")

	if len(s.Command) > displaySummaryBytes {
		t.Fatalf("huge command exceeded max preview length: %d bytes", len(s.Command))
	}
	if !s.Truncated {
		t.Fatal("expected huge command to be marked truncated")
	}
	if !utf8.ValidString(s.Command) {
		t.Fatal("huge command produced invalid UTF-8")
	}

	// Huge path for write
	hugePath := strings.Repeat("sub/", 10_000) + "file.txt"
	rawWrite := []byte(`{"path":"` + hugePath + `","content":"body"}`)
	sWrite := SummarizeArgumentsJSON("write", rawWrite, "", "")
	if len(sWrite.Path) > displaySummaryBytes {
		t.Fatalf("huge path exceeded max preview length: %d bytes", len(sWrite.Path))
	}
	if !sWrite.Truncated {
		t.Fatal("expected huge path to be marked truncated")
	}
}

func TestSummarizeArgumentsTruncatedJSONAtEveryByte(t *testing.T) {
	cases := []struct {
		tool string
		json string
	}{
		{"bash", `{"command":"curl -H \"Authorization: Bearer mysecret\" https://example.com"}`},
		{"read", `{"path":"/workspace/deep/nested/file.go","offset":10,"limit":50}`},
		{"write", `{"path":"/workspace/out.txt","content":"complete body content"}`},
		{"edit", `{"path":"/workspace/main.go","edits":[{"oldText":"a","newText":"b"}]}`},
	}

	for _, tc := range cases {
		data := []byte(tc.json)
		for i := 0; i <= len(data); i++ {
			prefix := data[:i]
			// Invariant: never panics, always bounded
			s := SummarizeArgumentsJSON(tc.tool, prefix, "/workspace", "/home/user")
			if len(s.Path) > displaySummaryBytes {
				t.Fatalf("%s cut at %d: path exceeded bound: %d", tc.tool, i, len(s.Path))
			}
			if len(s.Command) > displaySummaryBytes {
				t.Fatalf("%s cut at %d: command exceeded bound: %d", tc.tool, i, len(s.Command))
			}
			if len(s.LineRange) > 128 {
				t.Fatalf("%s cut at %d: line range exceeded bound: %d", tc.tool, i, len(s.LineRange))
			}
			if !utf8.ValidString(s.Path) || !utf8.ValidString(s.Command) || !utf8.ValidString(s.LineRange) {
				t.Fatalf("%s cut at %d: produced invalid UTF-8: %+v", tc.tool, i, s)
			}
		}
	}
}

func TestSummarizeSetExitCode(t *testing.T) {
	var s event.ToolSummary

	// Integer types
	SetExitCode(&s, map[string]any{"exitCode": 0})
	if s.ExitCode == nil || *s.ExitCode != 0 {
		t.Fatalf("expected 0, got %v", s.ExitCode)
	}

	SetExitCode(&s, map[string]any{"exitCode": int32(42)})
	if s.ExitCode == nil || *s.ExitCode != 42 {
		t.Fatalf("expected 42, got %v", s.ExitCode)
	}

	SetExitCode(&s, map[string]any{"exitCode": int64(127)})
	if s.ExitCode == nil || *s.ExitCode != 127 {
		t.Fatalf("expected 127, got %v", s.ExitCode)
	}

	SetExitCode(&s, map[string]any{"exitCode": float64(1)})
	if s.ExitCode == nil || *s.ExitCode != 1 {
		t.Fatalf("expected 1, got %v", s.ExitCode)
	}

	// Floats with fractions or non-finite: must be ignored
	s.ExitCode = nil
	SetExitCode(&s, map[string]any{"exitCode": 1.5})
	if s.ExitCode != nil {
		t.Fatalf("expected nil for fractional float, got %v", *s.ExitCode)
	}

	SetExitCode(&s, map[string]any{"exitCode": math.NaN()})
	if s.ExitCode != nil {
		t.Fatal("expected nil for NaN")
	}

	SetExitCode(&s, map[string]any{"exitCode": math.Inf(1)})
	if s.ExitCode != nil {
		t.Fatal("expected nil for Inf")
	}

	// Out of bounds int32 float
	SetExitCode(&s, map[string]any{"exitCode": 1e12})
	if s.ExitCode != nil {
		t.Fatal("expected nil for 1e12")
	}

	// Invalid detail types
	SetExitCode(&s, "not a map")
	SetExitCode(&s, map[string]any{"otherKey": 1})
	SetExitCode(&s, nil)
}

func TestSummarizeRebaseSummaryPath(t *testing.T) {
	// Empty path
	sEmpty := RebaseSummaryPath(event.ToolSummary{Path: ""}, "/workspace", "/home")
	if sEmpty.Path != "" {
		t.Fatalf("expected empty, got %q", sEmpty.Path)
	}

	// Already truncated path
	sTrunc := RebaseSummaryPath(event.ToolSummary{Path: "…truncated/path.go", Truncated: true}, "/workspace", "/home")
	if sTrunc.Path != "…truncated/path.go" {
		t.Fatalf("truncated path changed: %q", sTrunc.Path)
	}

	// Absolute path inside cwd
	sInCwd := RebaseSummaryPath(event.ToolSummary{Path: "/workspace/src/app.go"}, "/workspace", "/home")
	if sInCwd.Path != "src/app.go" {
		t.Fatalf("expected src/app.go, got %q", sInCwd.Path)
	}

	// Absolute path inside home
	sInHome := RebaseSummaryPath(event.ToolSummary{Path: "/home/docs/notes.txt"}, "/workspace", "/home")
	if sInHome.Path != "~/docs/notes.txt" {
		t.Fatalf("expected ~/docs/notes.txt, got %q", sInHome.Path)
	}
}

func TestSummarizeBoundSuffixEdgeCases(t *testing.T) {
	// Test maxBytes = 0, 1, 2, 3, 10
	for maxB := 0; maxB <= 20; maxB++ {
		res, trunc := boundSuffix("hello world 🌍 🎉", maxB)
		if len(res) > maxB {
			t.Fatalf("boundSuffix(len=%d) exceeded maxBytes %d", len(res), maxB)
		}
		if !utf8.ValidString(res) {
			t.Fatalf("boundSuffix(max=%d) produced invalid UTF-8: %q", maxB, res)
		}
		_ = trunc
	}
}

func FuzzSummarizeArgumentsJSON(f *testing.F) {
	f.Add("bash", []byte(`{"command":"echo hello"}`), "/workspace", "/home")
	f.Add("read", []byte(`{"path":"src/main.go","offset":1,"limit":10}`), "/workspace", "/home")
	f.Add("write", []byte(`{"path":"test.txt","content":"data"}`), "/workspace", "/home")
	f.Add("edit", []byte(`{"path":"test.txt","edits":[{"oldText":"a","newText":"b"}]}`), "/workspace", "/home")

	f.Fuzz(func(t *testing.T, tool string, data []byte, cwd, home string) {
		s := SummarizeArgumentsJSON(tool, data, cwd, home)
		if len(s.Path) > displaySummaryBytes {
			t.Fatalf("path exceeds %d bytes: %d", displaySummaryBytes, len(s.Path))
		}
		if len(s.Command) > displaySummaryBytes {
			t.Fatalf("command exceeds %d bytes: %d", displaySummaryBytes, len(s.Command))
		}
		if len(s.LineRange) > 128 {
			t.Fatalf("lineRange exceeds 128 bytes: %d", len(s.LineRange))
		}
		if !utf8.ValidString(s.Path) || !utf8.ValidString(s.Command) || !utf8.ValidString(s.LineRange) {
			t.Fatalf("summary fields contain invalid UTF-8: %+v", s)
		}
	})
}
