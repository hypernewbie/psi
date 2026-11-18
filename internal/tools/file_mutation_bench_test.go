package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkWriteFileTool(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{"4KiB", 4 * 1024},
		{"32KiB", 32 * 1024},
	} {
		b.Run(size.name, func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "target.txt")
			content := strings.Repeat("x", size.bytes)
			if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
				b.Fatal(err)
			}
			tool := &WriteTool{Cwd: dir}
			inv := Invocation{ID: "write-benchmark", Name: Write,
				Arguments: map[string]any{"path": "target.txt", "content": content}}
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if result := tool.ExecuteCtx(ctx, inv); result.IsError {
					b.Fatalf("write failed: %+v", result)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				b.Fatal(err)
			}
			if string(data) != content {
				b.Fatal("written bytes differ from the payload")
			}
		})
	}
}

func BenchmarkEditFileTool(b *testing.B) {
	for _, edits := range []struct {
		name  string
		count int
	}{
		{"1Replacement", 1},
		{"4Replacements", 4},
		{"8Replacements", 8},
	} {
		b.Run(edits.name, func(b *testing.B) {
			const blockSize = 4 * 1024
			markers := make([]string, 8)
			replacements := []string{"longer-marker-01", "edited-02", "", "edited-04",
				"edited-05", "edited-06", "edited-07", "edited-08"}
			var original, expected strings.Builder
			for i := range markers {
				marker := fmt.Sprintf("marker-%02d", i+1)
				markers[i] = marker
				padding := strings.Repeat("x", blockSize-len(marker)-1) + "\n"
				original.WriteString(marker + padding)
				if i < edits.count {
					expected.WriteString(replacements[i] + padding)
				} else {
					expected.WriteString(marker + padding)
				}
			}
			var changes []any
			for i := edits.count - 1; i >= 0; i-- {
				changes = append(changes, map[string]any{"oldText": markers[i], "newText": replacements[i]})
			}
			dir := b.TempDir()
			path := filepath.Join(dir, "target.txt")
			tool := &EditTool{Cwd: dir}
			inv := Invocation{ID: "edit-benchmark", Name: Edit,
				Arguments: map[string]any{"path": "target.txt", "edits": changes}}
			ctx := context.Background()
			input, want := []byte(original.String()), expected.String()
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				if err := os.WriteFile(path, input, 0o666); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if result := tool.ExecuteCtx(ctx, inv); result.IsError {
					b.Fatalf("edit failed: %+v", result)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				b.Fatal(err)
			}
			if string(data) != want {
				b.Fatal("edited bytes differ from expected output")
			}
		})
	}
}
