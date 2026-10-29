package agent

import (
	"strings"
	"testing"

	"psi/internal/provider"
)

func TestReviewSplitTurnCompactionPreservesRequestedFocus(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("original request")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, strings.Repeat("old progress ", 10_000))
	appendAssistantText(t, store, strings.Repeat("retained continuation ", 4_000))
	fake.Responses = []*provider.Response{{Text: "focused checkpoint", StopReason: "stop"}}
	const focus = "Preserve the deployment constraints."
	if err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, focus); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("summary calls=%d, want one split-prefix request", len(fake.Calls))
	}
	prompt := fake.Calls[0].Messages[0].Content[0].Text
	if !strings.Contains(prompt, "Additional focus: "+focus) {
		t.Fatalf("split-prefix request omitted operator focus: %.500s", prompt)
	}
}
