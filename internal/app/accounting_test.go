package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"psi/internal/agent"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
)

func TestSessionCommandUsesStoredAttemptAccountingAfterRestart(t *testing.T) {
	r := testRuntime(t)
	cache, reasoning := int64(8), int64(5)
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "synthetic answer", Model: "synthetic-returned-model", ResponseID: "synthetic-response", Usage: provider.Usage{Input: 11, Output: 7, CacheRead: 3, CacheWrite: 14, TotalTokens: 35, CacheWrite1h: &cache, Reasoning: &reasoning}}}}
	loop := agent.New(r.store, r.reg, fake, nil)
	if err := loop.Submit(context.Background(), "synthetic prompt", r.cfg); err != nil {
		t.Fatal(err)
	}
	one := float64(1)
	if _, err := r.store.AppendCompactionWithUsage("synthetic summary", "", 100, nil, &session.Usage{Input: 2, Output: 3, TotalTokens: 5, CacheWrite1h: &one, Reasoning: &one}); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.OpenFile(r.store.File(), r.store.Cwd(), false)
	if err != nil {
		t.Fatal(err)
	}
	accounting, err := reopened.Accounting()
	if err != nil {
		t.Fatal(err)
	}
	if accounting.Attempts != 2 || accounting.Usage.Input != 13 || accounting.Usage.Output != 10 || accounting.Usage.TotalTokens != 40 || accounting.ReturnedModel != "synthetic-returned-model" {
		t.Fatalf("accounting: %+v", accounting)
	}
	if accounting.Usage.CacheWrite1h == nil || *accounting.Usage.CacheWrite1h != 9 || accounting.Usage.Reasoning == nil || *accounting.Usage.Reasoning != 6 {
		t.Fatal("optional cache/reasoning counters lost")
	}
	r.store.Unlock()
	r.store = reopened
	var output bytes.Buffer
	if _, err := r.command(&terminal.Command{Name: "session"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"requested model m", "last returned model synthetic-returned-model", "estimated context tokens", "stored usage: input 13, output 10, cache read 3, cache write 14, total 40 (2 attempts)", "price unknown"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "$0") {
		t.Fatal("unknown price presented as free")
	}
}
