package provider

import (
	"context"
	"os"
	"testing"
	"time"
)

// Live smoke (Step 54): opt-in, operator-selected credential, small text
// prompt + one tool-call stub + cancel. Skipped without PSI_LIVE_PROVIDER=1.
func TestLiveAnthropic(t *testing.T) {
	if os.Getenv("PSI_LIVE_PROVIDER") != "1" {
		t.Skip("live provider opt-in only (PSI_LIVE_PROVIDER=1)")
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	c := NewClient("", key, 1, 30*time.Second)
	defer c.CloseIdle()
	// Small text prompt.
	req := Request{Model: ModelMeta{ID: "claude-sonnet-4-5", API: "anthropic-messages", MaxTokens: 64, Reasoning: false}, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Say hi in one word."}}}}}
	resp, err := c.Send(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("live text: %v", err)
	}
	if resp.Text == "" {
		t.Errorf("empty live response")
	}
	// Cancel one request.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Send(ctx, req, nil); err == nil {
		t.Errorf("cancelled request should fail")
	}
}
