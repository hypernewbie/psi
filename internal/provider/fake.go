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

	var deltas []string
	if idx < len(f.TextDeltas) && f.TextDeltas[idx] != nil {
		deltas = make([]string, len(f.TextDeltas[idx]))
		copy(deltas, f.TextDeltas[idx])
	}
	var errVal error
	if idx < len(f.Errors) {
		errVal = f.Errors[idx]
	}
	var respVal *Response
	hasResp := false
	if idx < len(f.Responses) {
		respVal = f.Responses[idx]
		hasResp = true
	}
	f.mu.Unlock()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, d := range deltas {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if onText != nil {
			onText(d)
		}
	}
	if errVal != nil {
		return nil, errVal
	}
	if hasResp {
		return respVal, nil
	}
	return nil, fmt.Errorf("fake provider: no scripted response %d", idx)
}

// Requests returns a copied snapshot of f.Calls under f.mu.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Calls == nil {
		return nil
	}
	cp := make([]Request, len(f.Calls))
	copy(cp, f.Calls)
	return cp
}

// CallCount returns len(f.Calls) under f.mu.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

func (f *Fake) CloseIdle() {
	f.mu.Lock()
	defer f.mu.Unlock()
}
