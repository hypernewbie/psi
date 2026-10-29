package provider

import (
	"context"
	"strings"
	"testing"
)

func FuzzGoogleSSE(f *testing.F) {
	f.Add(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}` + "\n\n")
	f.Add("data: [DONE]\n\n")
	f.Add("data: {\"candidates\":[\n\n")
	f.Add("event: error\ndata: {\"error\":{\"message\":\"synthetic\"}}\n\n")
	request := Request{Model: googleTestModel("gemini-2.5-flash", "google"), Thinking: "off"}
	f.Fuzz(func(t *testing.T, data string) {
		_, _ = parseGoogleSSE(strings.NewReader(data), 0, context.Background(), nil, request)
	})
}
