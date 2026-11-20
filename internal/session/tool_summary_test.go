package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/event"
)

func TestTimelineExtractsBoundedToolSummaryWithoutBodyCanary(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(OpenOptions{Cwd: cwd, SessionDir: filepath.Join(root, "sessions"), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "write a file"}}); err != nil {
		t.Fatal(err)
	}
	const canary = "large-write-body-must-not-reach-timeline"
	body := strings.Repeat(canary, 12_000)
	call := ToolCall{Type: "toolCall", ID: "write-call", Name: "write", Arguments: map[string]any{"path": filepath.Join(cwd, "src", "result.go"), "content": body}}
	if _, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", Content: []ContentBlock{{Type: "toolCall", Call: &call}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult", ToolCallID: call.ID, ToolName: call.Name, Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "hidden-result-canary"}}}}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFile(store.File(), cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	var found *TranscriptItem
	complete, err := reopened.Timeline(false, 0, func(item TranscriptItem) bool {
		if item.Kind == KindTool {
			copy := item
			found = &copy
		}
		return true
	})
	if err != nil || !complete || found == nil {
		t.Fatalf("timeline complete=%v found=%+v err=%v", complete, found, err)
	}
	if found.ToolName != "write" || found.ToolState != "done" || found.ToolSummary.Path != "src/result.go" {
		t.Fatalf("structured replay summary=%+v", found)
	}
	encoded, err := json.Marshal(found)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 1024 || strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), "hidden-result-canary") {
		t.Fatalf("display item retained hidden content (%d bytes)", len(encoded))
	}
	if found.ToolSummary.ExitCode != nil || found.ToolSummary.Note != "" {
		t.Fatalf("spurious metadata: %+v", found.ToolSummary)
	}
}

// TestWebCardHistoryBounds protects the message-envelope display payload: the
// bounded card reaches the timeline on fresh and reopened entries, an invalid
// or oversized payload is ignored without changing the outcome, and the
// model-facing message bytes stay untouched.
func TestWebCardHistoryBounds(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := New(OpenOptions{Cwd: cwd, SessionDir: filepath.Join(root, "sessions"), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "fetch the page"}}); err != nil {
		t.Fatal(err)
	}

	const canary = "web-body-canary-must-not-reach-display"
	call := ToolCall{Type: "toolCall", ID: "web-call", Name: "fetch_content",
		Arguments: map[string]any{"url": "https://example.com/page?q=" + canary}}
	assistantMsg := AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant",
		Content: []ContentBlock{{Type: "toolCall", Call: &call}}}}
	callDisplay := event.WebCall{Title: "fetch https://example.com/page", Mode: "raw"}
	assistantID, err := store.AppendMessageDisplay(assistantMsg, &event.WebMeta{Calls: map[string]event.WebCall{call.ID: callDisplay}})
	if err != nil {
		t.Fatal(err)
	}

	body := strings.Repeat(canary, 20_000)
	resultMsg := AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult",
		ToolCallID: call.ID, ToolName: call.Name,
		Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: body}}}}}
	resultDisplay := &event.WebResult{Title: "Example", Chars: 10, HasChars: true, Truncated: true, Preview: []string{"BODY OK 😀"}}
	resultID, err := store.AppendMessageDisplay(resultMsg, &event.WebMeta{CallID: call.ID, Result: resultDisplay})
	if err != nil {
		t.Fatal(err)
	}

	// A result whose call was not retained keeps its outcome and never gains
	// display data from the envelope.
	unmatched := AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult",
		ToolCallID: "missing-call", ToolName: "get_search_content",
		Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "unmatched-body-canary"}}}}}
	if _, err := store.AppendMessageDisplay(unmatched, &event.WebMeta{CallID: "missing-call",
		Result: &event.WebResult{Title: "Leaked", Chars: 1, HasChars: true, Preview: []string{"UNMATCHED_PREVIEW"}}}); err != nil {
		t.Fatal(err)
	}

	// Valid JSON with over-limit display data on a retained call: the whole
	// payload is ignored, while the call itself and its outcome stay visible.
	badResultCall := ToolCall{Type: "toolCall", ID: "bad-result-call", Name: "get_search_content",
		Arguments: map[string]any{"responseId": "r1"}}
	badResultCallMsg := AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant",
		Content: []ContentBlock{{Type: "toolCall", Call: &badResultCall}}}}
	if _, err := store.AppendMessageDisplay(badResultCallMsg, nil); err != nil {
		t.Fatal(err)
	}
	overLines := make([]string, event.WebMaxPreviewLines+1)
	for i := range overLines {
		overLines[i] = "line"
	}
	badResultMsg := AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult",
		ToolCallID: badResultCall.ID, ToolName: badResultCall.Name,
		Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}}}}
	if _, err := store.AppendMessageDisplay(badResultMsg, &event.WebMeta{CallID: badResultCall.ID,
		Result: &event.WebResult{Title: "Too many", Preview: overLines}}); err != nil {
		t.Fatal(err)
	}

	// A retained call ID is not enough: a result from a different web tool
	// must not supply its card data.
	mismatchedCall := ToolCall{Type: "toolCall", ID: "mismatched-result", Name: "fetch_content"}
	if _, err := store.AppendMessage(AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant",
		Content: []ContentBlock{{Type: "toolCall", Call: &mismatchedCall}}}}); err != nil {
		t.Fatal(err)
	}
	mismatchedResult := AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult",
		ToolCallID: mismatchedCall.ID, ToolName: "web_search",
		Content: []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: "ok"}}}}}
	if _, err := store.AppendMessageDisplay(mismatchedResult, &event.WebMeta{CallID: mismatchedCall.ID,
		Result: &event.WebResult{Kind: event.WebResultSearch, Sources: 4, HasSources: true}}); err != nil {
		t.Fatal(err)
	}

	// A call with an over-limit title is ignored as display data; the call
	// still shows its truthful outcome.
	badCall := ToolCall{Type: "toolCall", ID: "bad-call", Name: "web_search", Arguments: map[string]any{"query": "q"}}
	badMsg := AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant",
		Content: []ContentBlock{{Type: "toolCall", Call: &badCall}}}}
	oversized := event.WebCall{Title: strings.Repeat("t", event.WebMaxTitleBytes+1)}
	if _, err := store.AppendMessageDisplay(badMsg, &event.WebMeta{Calls: map[string]event.WebCall{badCall.ID: oversized}}); err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T, view *Store) {
		t.Helper()
		items := map[string]TranscriptItem{}
		complete, err := view.Timeline(false, 0, func(item TranscriptItem) bool {
			if item.Kind == KindTool && len(item.BlockIDs) > 0 {
				items[item.ToolName+"/"+item.BlockIDs[0]] = item
			}
			return true
		})
		if err != nil || !complete {
			t.Fatalf("timeline complete=%v err=%v", complete, err)
		}
		item, ok := items["fetch_content/web-call"]
		if !ok {
			t.Fatalf("missing web card item: %+v", items)
		}
		if item.ToolState != "done" || item.ToolSummary.Web == nil {
			t.Fatalf("web card lost its joined data: %+v", item)
		}
		if item.ToolSummary.Web.Call.Title != "fetch https://example.com/page" || item.ToolSummary.Web.Call.Mode != "raw" {
			t.Fatalf("call display changed: %+v", item.ToolSummary.Web.Call)
		}
		if result := item.ToolSummary.Web.Result; result == nil || result.Title != "Example" || result.Chars != 10 || !result.Truncated || len(result.Preview) != 1 {
			t.Fatalf("result display changed: %+v", item.ToolSummary.Web.Result)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 2048 || strings.Contains(string(encoded), canary) {
			t.Fatalf("display item retained hidden content (%d bytes)", len(encoded))
		}
		unmatchedItem, ok := items["get_search_content/missing-call"]
		if !ok {
			t.Fatalf("missing unmatched result item: %+v", items)
		}
		if unmatchedItem.ToolSummary.Web != nil || unmatchedItem.Note != "historical result without retained call" {
			t.Fatalf("unmatched result gained display data: %+v", unmatchedItem)
		}
		rejectedResult, ok := items["get_search_content/bad-result-call"]
		if !ok {
			t.Fatalf("missing over-limit result item: %+v", items)
		}
		if rejectedResult.ToolState != "done" || rejectedResult.ToolSummary.Web == nil || rejectedResult.ToolSummary.Web.Result != nil {
			t.Fatalf("over-limit result display changed the outcome: %+v", rejectedResult)
		}
		mismatched, ok := items["fetch_content/mismatched-result"]
		if !ok || mismatched.ToolState != "done" || mismatched.ToolSummary.Web == nil || mismatched.ToolSummary.Web.Result != nil {
			t.Fatalf("mismatched result should keep its outcome without attaching card data: %+v", mismatched)
		}
		badItem, ok := items["web_search/bad-call"]
		if !ok {
			t.Fatalf("missing invalid display item: %+v", items)
		}
		if badItem.ToolState != "interrupted" || badItem.ToolSummary.Web == nil || badItem.ToolSummary.Web.Call.Title != "" || badItem.ToolSummary.Web.Result != nil {
			t.Fatalf("invalid display changed the outcome: %+v", badItem)
		}
	}
	check(t, store)
	reopened, err := OpenFile(store.File(), cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	check(t, reopened)

	// The envelope is a sibling of `message`: the model-facing JSON must be
	// byte-identical to the appended message.
	recordByID := func(want string) jsonView {
		t.Helper()
		data, err := os.ReadFile(store.File())
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			obj, err := exactObject([]byte(line))
			if err != nil {
				continue
			}
			if id, _ := exactString(obj, "id"); id == want {
				return obj
			}
		}
		t.Fatalf("record %q not found", want)
		return nil
	}
	for _, tc := range []struct {
		id      string
		message AgentMessage
	}{
		{assistantID, assistantMsg},
		{resultID, resultMsg},
	} {
		record := recordByID(tc.id)
		if len(record["psiWeb"]) == 0 {
			t.Fatalf("record %q has no envelope", tc.id)
		}
		want, err := json.Marshal(tc.message)
		if err != nil {
			t.Fatal(err)
		}
		if string(record["message"]) != string(want) {
			t.Fatalf("message changed by the envelope:\n%s\n%s", record["message"], want)
		}
	}
	assistantRecord := recordByID(assistantID)
	if len(assistantRecord["psiWeb"]) > maxWebCallBytes+64 {
		t.Fatalf("assistant display payload is %d bytes", len(assistantRecord["psiWeb"]))
	}
	resultRecord := recordByID(resultID)
	if len(resultRecord["psiWeb"]) > maxWebResultBytes+64 {
		t.Fatalf("result display payload is %d bytes", len(resultRecord["psiWeb"]))
	}

	// Maximum producer payloads stay inside the documented caps.
	maxCall := event.WebCall{Title: strings.Repeat("u", event.WebMaxCallStringBytes), Mode: "raw"}
	for i := 0; i < event.WebMaxCallEntries; i++ {
		maxCall.Entries = append(maxCall.Entries, strings.Repeat("e", event.WebMaxCallStringBytes))
	}
	callMeta, err := json.Marshal(event.WebMeta{Calls: map[string]event.WebCall{"call": maxCall}})
	if err != nil {
		t.Fatal(err)
	}
	if len(callMeta) > maxWebCallBytes {
		t.Fatalf("call display payload is %d bytes", len(callMeta))
	}
	maxResult := &event.WebResult{Title: strings.Repeat("t", 300), Chars: 1 << 40, HasChars: true, Truncated: true}
	for i := 0; i < event.WebMaxPreviewLines; i++ {
		maxResult.Preview = append(maxResult.Preview, strings.Repeat("😀", event.WebMaxPreviewUnits/event.WebMaxPreviewLines/2))
	}
	resultMeta, err := json.Marshal(event.WebMeta{CallID: "call", Result: maxResult})
	if err != nil {
		t.Fatal(err)
	}
	if decodeWebMeta(resultMeta) == nil {
		t.Fatalf("bounded result payload was rejected")
	}
	if len(resultMeta) > maxWebResultBytes {
		t.Fatalf("result display payload is %d bytes", len(resultMeta))
	}

	// Wrong shapes and out-of-range values are ignored as a whole.
	for _, raw := range []string{
		`"not an object"`,
		`{"calls":[]}`,
		`{"calls":{"c":{"title":"ok","overflow":-1}}}`,
		`{"calls":{"c":{"title":"ok","entries":["1","2","3","4","5","6"]}}}`,
		`{"callId":"c","result":{"chars":-1}}`,
		`{"callId":"c","result":{"chars":5}}`,
		`{"callId":"c","result":{"kind":"batch","countsKnown":true,"ok":0,"total":0}}`,
		`{"callId":"c","result":{"preview":["1","2","3","4","5","6","7","8","9","10","11","12","13"]}}`,
		`{"callId":"c","result":{"kind":"page","charsKnown":true,"chars":20,"pagePreview":"\n\n\n\n\n\n\n\n\n\n\n\nextra"}}`,
	} {
		if decodeWebMeta([]byte(raw)) != nil {
			t.Fatalf("invalid display payload accepted: %s", raw)
		}
	}
	if decodeWebMeta([]byte(`{"calls":{"c":{"title":"fetch https://example.com"}}}`)) == nil {
		t.Fatal("valid display payload rejected")
	}
}

func TestWebMetaAcceptsTitleAtBound(t *testing.T) {
	atBound, err := json.Marshal(event.WebMeta{Calls: map[string]event.WebCall{"c": {Title: strings.Repeat("t", event.WebMaxTitleBytes)}}})
	if err != nil {
		t.Fatal(err)
	}
	if decodeWebMeta(atBound) == nil {
		t.Fatal("title at the shared bound was rejected")
	}
	over, err := json.Marshal(event.WebMeta{Calls: map[string]event.WebCall{"c": {Title: strings.Repeat("t", event.WebMaxTitleBytes+1)}}})
	if err != nil {
		t.Fatal(err)
	}
	if decodeWebMeta(over) != nil {
		t.Fatal("title over the shared bound was accepted")
	}
}
