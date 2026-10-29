//go:build acceptance && unix

package app

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"psi/internal/session"
)

func TestScaleActivationProcess(t *testing.T) {
	if os.Getenv("PSI_SCALE_PROCESS") != "1" || flag.Lookup("test.run").Value.String() != "^TestScaleActivationProcess$" {
		return
	}
	start := time.Now()
	store, err := session.OpenFile(os.Getenv("PSI_SCALE_FILE"), "/synthetic/work", false)
	if err != nil {
		t.Fatal(err)
	}
	act, err := PrepareActivation(store)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if len(act.Proj.Flat) != 3 || len(act.Recall) != 2 {
		t.Fatalf("wrong projection/recall: %d %d", len(act.Proj.Flat), len(act.Recall))
	}
	var rss unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &rss); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("SCALE activation_ms=%d peak_rss_kib=%d entries=%d\n", elapsed.Milliseconds(), rss.Maxrss, store.EntryCount())
	if elapsed > 3*time.Second {
		t.Fatalf("activation %v exceeds 3s", elapsed)
	}
	if rss.Maxrss >= 160*1024 {
		t.Fatalf("RSS %d KiB exceeds 160 MiB", rss.Maxrss)
	}
	count := 0
	complete, err := store.Timeline(true, 0, func(item session.TranscriptItem) bool {
		count++
		if strings.Contains(item.Text, "HIDDEN_BULK_BODY") {
			t.Fatal("hidden body reached display")
		}
		return count < 5
	})
	if err != nil || complete || count != 5 {
		t.Fatalf("lazy replay did not stop: %v %v %d", err, complete, count)
	}
}

func TestScale10000MetadataFirstPage(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10000; i++ {
		data := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":\"synthetic-%05d\",\"cwd\":\"/synthetic/work\"}\n{\"type\":\"message\",\"id\":\"u\",\"parentId\":null,\"message\":{\"role\":\"user\",\"content\":\"synthetic prompt\"}}\n", i)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%05d.jsonl", i)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	items, err := session.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	selector := NewSelector(items, "")
	rows := selector.RenderPickerRows(80, 19, true)
	elapsed := time.Since(start)
	if len(items) != 10000 || len(selector.PageItems()) != 20 || len(rows) == 0 || len(rows) > 20 {
		t.Fatal("first page incomplete")
	}
	t.Logf("10000 metadata records to usable 20-item first page: %v", elapsed)
	if elapsed > 2*time.Second {
		t.Fatalf("metadata first page %v exceeds 2s", elapsed)
	}
}

func TestScale512MiBActivation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "scale.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(file, 1024*1024)
	fmt.Fprintln(w, `{"type":"session","version":3,"id":"scale","timestamp":"2026-01-01T00:00:00Z","cwd":"/synthetic/work"}`)
	fmt.Fprintln(w, `{"type":"message","id":"u0","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"old prompt","timestamp":1}}`)
	payload := strings.Repeat("HIDDEN_BULK_BODY ", 2048)
	encoded, _ := json.Marshal(payload)
	parent := "u0"
	size := int64(0)
	records := 0
	for size < 512*1024*1024 {
		id := fmt.Sprintf("t%d", records)
		n, err := fmt.Fprintf(w, "{\"type\":\"message\",\"id\":%q,\"parentId\":%q,\"timestamp\":\"2026-01-01T00:00:02Z\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":%q,\"toolName\":\"read\",\"content\":[{\"type\":\"text\",\"text\":%s}],\"timestamp\":2}}\n", id, parent, id, encoded)
		if err != nil {
			t.Fatal(err)
		}
		size += int64(n)
		records++
		parent = id
	}
	fmt.Fprintf(w, "{\"type\":\"compaction\",\"id\":\"cp\",\"parentId\":%q,\"timestamp\":\"2026-01-01T00:00:03Z\",\"summary\":\"synthetic summary\",\"firstKeptEntryId\":\"cp\",\"tokensBefore\":100,\"systemMessage\":{\"role\":\"system\",\"content\":\"synthetic instructions\",\"timestamp\":3}}\n", parent)
	fmt.Fprintln(w, `{"type":"message","id":"u1","parentId":"cp","timestamp":"2026-01-01T00:00:04Z","message":{"role":"user","content":"active prompt","timestamp":4}}`)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestScaleActivationProcess$", "-test.v")
	cmd.Env = append(appTestEnv(root, filepath.Join(root, "agent")), "PSI_SCALE_PROCESS=1", "PSI_SCALE_FILE="+path)
	out, err := cmd.CombinedOutput()
	t.Logf("generator: >=512 MiB, %d historical tool records, ~32 KiB payload/record; %s", records, out)
	if err != nil {
		t.Fatal("scale process failed", err)
	}
}
