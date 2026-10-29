package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"psi/internal/session"
)

// P4-002: the picker rows are compared with a Pi-derived expected fixture.
// The fixture pins the pointer, the current marker, the provider badge, and
// the selected-only detail.
func TestPlan4PiPickerFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/plan4-pi-pickers.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Reference       string   `json:"reference"`
		Size            string   `json:"size"`
		Width           int      `json:"width"`
		ModelRows       []string `json:"modelRows"`
		SessionRows     []string `json:"sessionRows"`
		SourceComponent []string `json:"sourceComponents"`
		Omissions       []string `json:"omissions"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fixture.Reference, "d5b3de22d0e24a3e9c38a9410d3329b099717283") {
		t.Fatal("picker fixture must identify the pinned Pi reference")
	}
	if len(fixture.SourceComponent) == 0 || len(fixture.Omissions) == 0 {
		t.Fatal("picker fixture must record the source components and the omissions")
	}

	models := NewSelector([]session.SessionInfo{
		{ID: "anthropic/claude-sonnet-5", FirstMessage: "credential available; anthropic-messages, context 200000, output 64000", Modified: time.Unix(1, 0)},
		{ID: "anthropic/m", FirstMessage: "credential available; anthropic-messages, context 1000000, output 64000", Modified: time.Unix(2, 0)},
		{ID: "openai-codex/gpt-5.6-luna", FirstMessage: "OAuth login present (access checked on request); openai-responses, context 400000, output 128000", Modified: time.Unix(3, 0)},
	}, "anthropic/m")
	models.Models = true
	got := models.RenderPickerRows(fixture.Width, 19, true)
	if len(got) != len(fixture.ModelRows) {
		t.Fatalf("model rows=%q want=%q", got, fixture.ModelRows)
	}
	for i := range got {
		if got[i] != fixture.ModelRows[i] {
			t.Errorf("model row %d: got %q want %q", i, got[i], fixture.ModelRows[i])
		}
	}

	sessions := NewSelector([]session.SessionInfo{
		{ID: "01a18585-2222-3333-4444-555566667777", Name: "named session", FirstMessage: "first prompt", MessageCount: 4, Modified: time.Now().Add(-3 * time.Hour)},
		{ID: "01a18585-8888-9999-aaaa-bbbbccccdddd", FirstMessage: "second prompt", MessageCount: 2, Modified: time.Now().Add(-40 * 24 * time.Hour)},
	}, "01a18585-8888-9999-aaaa-bbbbccccdddd")
	got = sessions.RenderPickerRows(fixture.Width, 19, true)
	if len(got) != len(fixture.SessionRows) {
		t.Fatalf("session rows=%q want=%q", got, fixture.SessionRows)
	}
	for i := range got {
		if got[i] != fixture.SessionRows[i] {
			t.Errorf("session row %d: got %q want %q", i, got[i], fixture.SessionRows[i])
		}
	}
}
