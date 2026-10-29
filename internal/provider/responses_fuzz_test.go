package provider

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func FuzzResponsesSSE(f *testing.F) {
	f.Add([]byte(responsesEvent("response.completed", map[string]any{"response": map[string]any{"id": "synthetic", "status": "completed", "output": []any{}}})))
	f.Add([]byte("data: not-json\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxStreamBytes {
			return
		}
		_, _ = parseResponsesSSE(bytes.NewReader(data), time.Minute, context.Background(), nil, Request{Model: responsesTestModel("fuzz-model"), Thinking: "off"})
	})
}
