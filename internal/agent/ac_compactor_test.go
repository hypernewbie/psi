package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"psi/internal/provider"
)

func TestCompactorProviderIsSummaryOnlyAndRestoresActiveRun(t *testing.T) {
	store, _, active, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("original task")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		appendAssistantText(t, store, strings.Repeat(fmt.Sprintf("older work %d ", i), 2_000))
	}
	summary := &provider.Fake{Responses: []*provider.Response{{Text: "summary from compactor", StopReason: "stop"}}}
	active.Responses = []*provider.Response{{Text: "continued on active model", StopReason: "stop"}}
	loop.Steer("queued while compacting")
	cfg := Config{
		Model:            modelMeta(),
		Thinking:         "off",
		KeepRecentTokens: 1_000,
		CompactionProvider: func() (provider.Provider, provider.ModelMeta, func(), error) {
			model := modelMeta()
			model.ID = "summary-model"
			return summary, model, nil, nil
		},
	}
	if err := loop.Compact(context.Background(), cfg, "preserve active task"); err != nil {
		t.Fatal(err)
	}
	if summary.CallCount() != 1 || summary.Calls[0].Model.ID != "summary-model" {
		t.Fatalf("summary provider/model = %d, %q", summary.CallCount(), summary.Calls[0].Model.ID)
	}
	if active.CallCount() != 1 || active.Calls[0].Model.ID != modelMeta().ID {
		t.Fatalf("active provider/model = %d, %q", active.CallCount(), active.Calls[0].Model.ID)
	}
	found := false
	for _, message := range active.Calls[0].Messages {
		for _, block := range message.Content {
			if message.Role == "user" && strings.Contains(block.Text, "queued while compacting") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("active-model continuation lost queued message: %+v", active.Calls[0].Messages)
	}
}
