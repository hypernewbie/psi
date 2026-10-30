package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

type reviewProvider func(context.Context, provider.Request, func(string)) (*provider.Response, error)

func (f reviewProvider) Send(ctx context.Context, req provider.Request, text func(string)) (*provider.Response, error) {
	return f(ctx, req, text)
}
func (f reviewProvider) CloseIdle() {}

func TestTextEventsHaveBoundedUTF8Payloads(t *testing.T) {
	text := strings.Repeat("界🌍", 5000)
	var visible strings.Builder
	loop := &Loop{sink: func(e event.Event) {
		if len(e.Text) > 8*1024 || !utf8.ValidString(e.Text) {
			t.Fatal("unbounded or split UTF-8 event")
		}
		visible.WriteString(e.Text)
	}}
	loop.emitText(text, event.OutcomeTransient)
	if visible.String() != text {
		t.Fatal("event chunks lost or repeated text")
	}
}

func TestFinalTextSettlementMatchesVisibleText(t *testing.T) {
	for _, tc := range []struct {
		name, delta, final string
		content            []provider.Block
		wantErr            bool
	}{
		{name: "no deltas", final: "complete"},
		{name: "missing suffix", delta: "com", final: "complete"},
		{name: "content canonical", final: "wrong cache", content: []provider.Block{{Type: "text", Text: "canonical"}}},
		{name: "contradiction", delta: "visible", final: "different", wantErr: true},
		{name: "shorter final", delta: "visible", final: "vis", wantErr: true},
		{name: "empty final", delta: "visible", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, _ := session.New(session.OpenOptions{Cwd: dir, Persist: false})
			p := reviewProvider(func(_ context.Context, _ provider.Request, text func(string)) (*provider.Response, error) {
				if tc.delta != "" {
					text(tc.delta)
				}
				return &provider.Response{Text: tc.final, Content: tc.content, StopReason: "stop"}, nil
			})
			var visible strings.Builder
			loop := New(store, tools.NewRegistry(dir), p, func(e event.Event) {
				if e.Type == event.TextDelta {
					visible.WriteString(e.Text)
				}
			})
			err := loop.Submit(context.Background(), "hi", Config{Model: provider.ModelMeta{ID: "m"}})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error: %v", err)
			}
			want := tc.final
			if len(tc.content) > 0 {
				want = "canonical"
			}
			if tc.wantErr {
				want = tc.delta
			}
			if visible.String() != want {
				t.Fatalf("visible %q != %q", visible.String(), want)
			}
			proj, _ := store.Projection()
			last := proj.Flat[len(proj.Flat)-1].Assistant
			if last == nil || len(last.Content) != 1 || last.Content[0].Text.Text != want {
				t.Fatalf("durable response differs: %+v", last)
			}
			if tc.wantErr && last.StopReason != "error" {
				t.Fatal("contradiction stored as a successful final")
			}
		})
	}
}

func TestInterruptedVisibleTextIsDurableWithoutExecutableCalls(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			dir := t.TempDir()
			store, _ := session.New(session.OpenOptions{Cwd: dir, Persist: false})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := reviewProvider(func(_ context.Context, _ provider.Request, text func(string)) (*provider.Response, error) {
				text("partial")
				if cancelled {
					cancel()
					return nil, ctx.Err()
				}
				return nil, errors.New("synthetic transport interruption")
			})
			err := New(store, tools.NewRegistry(dir), p, nil).Submit(ctx, "hi", Config{Model: provider.ModelMeta{ID: "m"}})
			if err == nil {
				t.Fatal("interruption accepted")
			}
			proj, _ := store.Projection()
			a := proj.Flat[len(proj.Flat)-1].Assistant
			want := "error"
			if cancelled {
				want = "aborted"
			}
			if a == nil || a.StopReason != want || len(a.Content) != 1 || a.Content[0].Text.Text != "partial" {
				t.Fatalf("partial not saved: %+v", a)
			}
			if len(session.UnresolvedToolCalls(proj.Flat)) != 0 {
				t.Fatal("partial became a call")
			}
		})
	}
}

func TestCancellationSettlesUnstartedCallsWithoutRunningThem(t *testing.T) {
	dir := t.TempDir()
	store, _ := session.New(session.OpenOptions{Cwd: dir, Persist: false})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Sequential path: one write per response, so cancelling after the first
	// result must keep the second call from ever running. Parallel batches
	// have their own cancellation regressions; this one pins turn boundaries.
	p := &provider.Fake{Responses: []*provider.Response{
		{ToolCalls: []provider.Block{
			{Type: "toolCall", ID: "first", Name: "write", Arguments: map[string]any{"path": "first.txt", "content": "done"}},
		}},
		{ToolCalls: []provider.Block{
			{Type: "toolCall", ID: "second", Name: "write", Arguments: map[string]any{"path": "second.txt", "content": "must not run"}},
		}},
		{Text: "next", StopReason: "stop"},
	}}
	loop := New(store, tools.NewRegistry(dir), p, func(e event.Event) {
		if e.Type == event.ToolEnd && e.ToolCallID == "first" {
			cancel()
		}
	})
	cfg := Config{Model: provider.ModelMeta{ID: "m"}, Tools: tools.Definitions([]string{"write"})}
	if err := loop.Submit(ctx, "do it", cfg); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "second.txt")); !os.IsNotExist(err) {
		t.Fatal("unstarted mutation ran")
	}
	proj, _ := store.Projection()
	if len(session.UnresolvedToolCalls(proj.Flat)) != 0 {
		t.Fatal("known unstarted calls left unresolved")
	}
	if err := loop.Submit(context.Background(), "continue", cfg); err != nil {
		t.Fatalf("safe continuation blocked: %v", err)
	}
}
