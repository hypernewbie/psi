package agent

import (
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
)

// TestCompactEndTurnUsesEndTurnReason pins Pi's attribution: the idle
// end-of-turn timer must report end-of-turn, not manual command, so notices
// and last-compaction source stay accurate.
func TestCompactEndTurnUsesEndTurnReason(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("old task")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, strings.Repeat("older context ", 2000))
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	fake.Responses = []*provider.Response{
		{Text: "end-turn checkpoint", StopReason: "stop"},
	}
	cfg := Config{Model: modelMeta(), Thinking: "off", KeepRecentTokens: 10, AutoCompact: true}
	if err := loop.CompactEndTurn(t.Context(), cfg, ""); err != nil {
		t.Fatal(err)
	}
	foundStart, foundEnd := false, false
	for _, e := range events {
		if e.Type == event.CompactionStart && e.State == event.CompactionEndTurn {
			foundStart = true
		}
		if e.Type == event.CompactionEnd && e.State == event.CompactionEndTurn && e.Err == nil {
			foundEnd = true
		}
		if e.Type == event.CompactionStart && e.State == event.CompactionManual {
			t.Fatalf("end-turn reported as manual: %+v", e)
		}
	}
	if !foundStart || !foundEnd {
		t.Fatalf("missing end-of-turn compaction events: %+v", events)
	}
}
