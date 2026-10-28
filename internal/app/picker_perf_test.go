package app

import (
	"fmt"
	goruntime "runtime"
	"sort"
	"testing"
	"time"

	"psi/internal/session"
)

func TestTenThousandPickerQueriesStayResponsive(t *testing.T) {
	items := make([]session.SessionInfo, 10_000)
	for i := range items {
		items[i] = session.SessionInfo{
			ID:           fmt.Sprintf("session-%05d", i),
			Cwd:          "/synthetic/workspace",
			Name:         fmt.Sprintf("synthetic task %05d", i),
			FirstMessage: fmt.Sprintf("fixture prompt for record %05d", i),
		}
	}
	selector := NewSelector(items, items[0].ID)
	queries := []string{"s", "se", "ses", "sess", "session", "session-0", "session-00", "session-000", "session-0001", "fixture"}
	samples := make([]time.Duration, 100)
	for i := range samples {
		start := time.Now()
		selector.SetFilter(queries[i%len(queries)])
		rows := selector.RenderPickerRows(80, 19, true)
		if len(rows) > 13 {
			t.Fatalf("picker exceeded bounded rows: %d", len(rows))
		}
		samples[i] = time.Since(start)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[94]
	t.Logf("10k metadata filter+render: OS=%s arch=%s samples=%d p50=%s p95=%s max=%s", goruntime.GOOS, goruntime.GOARCH, len(samples), samples[49], p95, samples[99])
	if p95 > 50*time.Millisecond {
		t.Fatalf("picker query-to-rows p95 %s exceeds 50ms", p95)
	}
}
