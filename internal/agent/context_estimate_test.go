package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
)

func TestContextEstimateIncludesImportedImagesAndActualToolSchemas(t *testing.T) {
	var message session.AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image","mimeType":"image/png","data":"c3ludGhldGlj"}],"timestamp":1}`), &message); err != nil {
		t.Fatal(err)
	}
	proj := &session.Projection{Flat: []session.AgentMessage{message}}
	small := EstimatedContext(proj, Config{})
	if small < 6000 {
		t.Fatalf("imported image ignored: %d", small)
	}
	cfg := Config{System: strings.Repeat("s", 3000), Tools: []provider.ToolDef{{Name: "read", Description: strings.Repeat("d", 9000), Schema: map[string]any{"type": "object"}}}}
	large := EstimatedContext(proj, cfg)
	if large-small < 4000 {
		t.Fatalf("effective system or schema omitted: %d -> %d", small, large)
	}
}
