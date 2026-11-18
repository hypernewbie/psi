package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func BenchmarkReadFileTool(b *testing.B) {
	line := strings.Repeat("x", 63) + "\n"
	manyLines := strings.Repeat(line, 6400)
	longLine := strings.Repeat("x", 60*1024) + "\n"
	notice := func(next int) string {
		return fmt.Sprintf("\n\n[Output truncated. Use offset=%d to continue; use bash for a line larger than 50 KiB.]", next)
	}
	cases := []struct {
		name    string
		content string
		offset  int
		limit   int
		want    string
	}{
		{
			name:    "FirstPage4KiB",
			content: manyLines,
			offset:  1,
			limit:   64,
			want:    strings.Repeat(line, 64) + notice(65),
		},
		{
			name:    "FirstPage32KiB",
			content: manyLines,
			offset:  1,
			limit:   512,
			want:    strings.Repeat(line, 512) + notice(513),
		},
		{
			name:    "DefaultByteCap",
			content: manyLines,
			offset:  1,
			limit:   DefaultMaxLines,
			want:    strings.Repeat(line, DefaultMaxBytes/len(line)) + notice(1+DefaultMaxBytes/len(line)),
		},
		{
			name:    "Offset2190",
			content: manyLines,
			offset:  2190,
			limit:   64,
			want:    strings.Repeat(line, 64) + notice(2254),
		},
		{
			name:    "Offset5850",
			content: manyLines,
			offset:  5850,
			limit:   64,
			want:    strings.Repeat(line, 64) + notice(5914),
		},
		{
			// A 60 KiB line fills whole 4 KiB output fragments. Twelve
			// fragments (49,152 bytes) fit; the next fragment exceeds the
			// remaining 2,048-byte cap and is not partially returned.
			name:    "LongSelectedLine",
			content: longLine + "end\n",
			offset:  1,
			limit:   DefaultMaxLines,
			want:    strings.Repeat("x", 12*4096) + notice(1),
		},
		{
			name:    "LongSkippedLine",
			content: longLine + manyLines,
			offset:  2,
			limit:   64,
			want:    strings.Repeat(line, 64) + notice(66),
		},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			dir := b.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte(tc.content), 0o644); err != nil {
				b.Fatal(err)
			}
			tool := &ReadTool{Cwd: dir}
			inv := Invocation{ID: "bench", Name: Read, Arguments: map[string]any{
				"path": "target.txt", "offset": tc.offset, "limit": tc.limit,
			}}
			want := Result{
				Content: []Block{{Type: "text", Text: tc.want}},
				Details: map[string]any{"truncated": true},
			}
			var got Result
			b.ReportAllocs()
			for b.Loop() {
				got = tool.ExecuteCtx(context.Background(), inv)
				if got.IsError {
					b.Fatalf("read failed: %+v", got)
				}
			}
			if !reflect.DeepEqual(got, want) {
				b.Fatalf("result = %+v, want %+v", got, want)
			}
		})
	}
}

func BenchmarkReadForwardPagination(b *testing.B) {
	line := strings.Repeat("x", 63) + "\n"
	content := strings.Repeat(line, 6400)
	notice := func(next int) string {
		return fmt.Sprintf("\n\n[Output truncated. Use offset=%d to continue; use bash for a line larger than 50 KiB.]", next)
	}
	offsets := []int{2001, 2065, 2129, 2193}
	invocations := make([]Invocation, len(offsets))
	wants := make([]Result, len(offsets))
	for i, offset := range offsets {
		invocations[i] = Invocation{ID: fmt.Sprintf("bench-%d", i), Name: Read, Arguments: map[string]any{
			"path": "target.txt", "offset": offset, "limit": 64,
		}}
		wants[i] = Result{
			Content: []Block{{Type: "text", Text: strings.Repeat(line, 64) + notice(offset+64)}},
			Details: map[string]any{"truncated": true},
		}
	}
	b.Run("FourForwardPages", func(b *testing.B) {
		dir := b.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
		tool := &ReadTool{Cwd: dir}
		results := make([]Result, len(invocations))
		b.ReportAllocs()
		for b.Loop() {
			for i, inv := range invocations {
				results[i] = tool.ExecuteCtx(context.Background(), inv)
				if results[i].IsError {
					b.Fatalf("read failed: %+v", results[i])
				}
			}
		}
		if !reflect.DeepEqual(results, wants) {
			b.Fatalf("results = %+v, want %+v", results, wants)
		}
	})
}
