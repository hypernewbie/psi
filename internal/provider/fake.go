package provider

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a scripted provider for tests (Phase 1 Step 11 + Phase 6 boundary).
type Fake struct {
	mu         sync.Mutex
	Responses  []*Response
	Errors     []error
	Calls      []Request
	TextDeltas [][]string
}

func (f *Fake) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	idx := len(f.Calls) - 1
	f.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var deltas []string
	if idx < len(f.TextDeltas) {
		deltas = f.TextDeltas[idx]
	}
	for _, d := range deltas {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if onText != nil {
			onText(d)
		}
	}
	if idx < len(f.Errors) && f.Errors[idx] != nil {
		return nil, f.Errors[idx]
	}
	if idx < len(f.Responses) {
		return f.Responses[idx], nil
	}
	return nil, fmt.Errorf("fake provider: no scripted response %d", idx)
}

func (f *Fake) CloseIdle() {}
