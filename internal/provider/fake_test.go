package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"psi/internal/testutil/leakcheck"
)

func TestFakeProvider(t *testing.T) {
	defer leakcheck.Verify(t)

	t.Run("successful response and text streaming", func(t *testing.T) {
		respExpected := &Response{
			Text:       "hello world",
			StopReason: "stop",
		}
		fake := &Fake{
			Responses:  []*Response{respExpected},
			TextDeltas: [][]string{{"hello ", "world"}},
		}

		var streamed strings.Builder
		req := Request{Model: ModelMeta{ID: "fake-model"}}
		resp, err := fake.Send(context.Background(), req, func(s string) {
			streamed.WriteString(s)
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != respExpected {
			t.Fatalf("expected resp %v, got %v", respExpected, resp)
		}
		if streamed.String() != "hello world" {
			t.Fatalf("expected streamed %q, got %q", "hello world", streamed.String())
		}
		if len(fake.Calls) != 1 || fake.Calls[0].Model.ID != "fake-model" {
			t.Fatalf("unexpected recorded calls: %+v", fake.Calls)
		}

		// CloseIdle invocation
		fake.CloseIdle()
	})

	t.Run("scripted error response", func(t *testing.T) {
		testErr := errors.New("scripted provider failure")
		fake := &Fake{
			Errors: []error{testErr},
		}
		_, err := fake.Send(context.Background(), Request{}, nil)
		if !errors.Is(err, testErr) {
			t.Fatalf("expected %v, got %v", testErr, err)
		}
	})

	t.Run("missing scripted response returns error", func(t *testing.T) {
		fake := &Fake{}
		_, err := fake.Send(context.Background(), Request{}, nil)
		if err == nil || !strings.Contains(err.Error(), "no scripted response 0") {
			t.Fatalf("expected missing scripted response error, got %v", err)
		}
	})

	t.Run("context cancelled before send", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		fake := &Fake{
			Responses: []*Response{{Text: "should not return"}},
		}
		_, err := fake.Send(ctx, Request{}, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("context cancelled during text deltas", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fake := &Fake{
			Responses:  []*Response{{Text: "done"}},
			TextDeltas: [][]string{{"delta1", "delta2", "delta3"}},
		}
		count := 0
		_, err := fake.Send(ctx, Request{}, func(s string) {
			count++
			if count == 1 {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled during deltas, got %v", err)
		}
	})

	t.Run("concurrent calls to Fake.Send", func(t *testing.T) {
		const numCalls = 20
		responses := make([]*Response, numCalls)
		for i := 0; i < numCalls; i++ {
			responses[i] = &Response{Text: fmt.Sprintf("resp-%d", i)}
		}

		fake := &Fake{
			Responses: responses,
		}

		var wg sync.WaitGroup
		for i := 0; i < numCalls; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				req := Request{Model: ModelMeta{ID: fmt.Sprintf("model-%d", idx)}}
				r, err := fake.Send(context.Background(), req, nil)
				if err != nil || r == nil {
					t.Errorf("call %d failed: %v", idx, err)
				}
			}(i)
		}
		wg.Wait()

		if len(fake.Calls) != numCalls {
			t.Fatalf("expected %d recorded calls, got %d", numCalls, len(fake.Calls))
		}
	})
}
