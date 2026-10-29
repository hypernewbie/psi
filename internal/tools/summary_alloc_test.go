package tools

import (
	"runtime"
	"strings"
	"testing"

	"psi/internal/event"
)

var summaryAllocationSink event.ToolSummary

func TestHiddenWriteBodyDoesNotScaleSummaryAllocations(t *testing.T) {
	makeRecord := func(size int) []byte {
		return []byte(`{"path":"src/result.go","content":"` + strings.Repeat("x", size) + `"}`)
	}
	measure := func(record []byte) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < 25; i++ {
			summaryAllocationSink = SummarizeArgumentsJSON("write", record, "/workspace", "/home/test")
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / 25
	}
	small, large := measure(makeRecord(1024)), measure(makeRecord(512*1024))
	if summaryAllocationSink.Path != "src/result.go" || summaryAllocationSink.Command != "" {
		t.Fatalf("wrong bounded summary: %+v", summaryAllocationSink)
	}
	if large > small+4096 {
		t.Fatalf("hidden body increased summary allocation per call: 1KiB=%d bytes, 512KiB=%d bytes", small, large)
	}
	t.Logf("write summary allocation per call (1KiB/512KiB hidden body): %d/%d bytes", small, large)
}
