//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func softWrapPTYParagraph() string {
	return "SOFTWRAP_PROBE " + strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima ", 3) + "SOFTWRAP_END"
}

func startSoftWrapPTY(t *testing.T, softWrap bool) (*reviewPTY, string) {
	t.Helper()
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	paragraph := softWrapPTYParagraph()
	encoded, err := json.Marshal(paragraph + "\n\n")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, anthropicPTYText(string(encoded[1:len(encoded)-1])))
	}))
	t.Cleanup(server.Close)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), fmt.Sprintf(`{"defaultProvider":"anthropic","defaultModel":"synthetic-model","defaultThinkingLevel":"off","defaultTools":[],"compaction":{"enabled":false},"outputPad":0,"softWrap":%t}`, softWrap))
	writeAppFile(t, filepath.Join(agentDir, "models.json"), fmt.Sprintf(`{"providers":{"anthropic":{"models":[{"id":"synthetic-model","api":"anthropic-messages","baseUrl":%q,"contextWindow":65536,"maxTokens":4096,"reasoning":false,"input":["text"]}]}}}`, strings.TrimRight(server.URL, "/")))
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-soft-wrap-key"}}`)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools")
	return q, paragraph
}

func runSoftWrapPTYTurn(t *testing.T, softWrap bool) (*reviewPTY, string) {
	t.Helper()
	q, paragraph := startSoftWrapPTY(t, softWrap)
	waitSoftWrapPTYReady(t, q, "")
	q.write("explain the probe\r")
	waitSoftWrapPTYReady(t, q, "SOFTWRAP_END")
	t.Cleanup(func() {
		q.write("/quit\r")
		q.finish()
	})
	return q, paragraph
}

// Synchronize on completed terminal state, not a quiet gap in provider output.
func waitSoftWrapPTYReady(t *testing.T, q *reviewPTY, marker string) {
	t.Helper()
	timeout := time.NewTimer(8 * time.Second)
	defer timeout.Stop()
	for !semanticEditorReady(q.screen) || !strings.Contains(q.raw.String(), marker) {
		select {
		case chunk, ok := <-q.chunks:
			if !ok {
				t.Fatalf("process exited before editor and %q were visible: %q", marker, q.raw.String())
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-timeout.C:
			t.Fatalf("editor and %q never became visible: %q", marker, q.raw.String())
		}
	}
}

func TestSoftWrapPTYCommittedParagraphKeepsOneLine(t *testing.T) {
	q, paragraph := runSoftWrapPTYTurn(t, true)

	raw := q.raw.String()
	if !strings.Contains(raw, paragraph) {
		t.Fatalf("committed paragraph was split across rows in soft mode:\n%q", raw)
	}
	screen := q.screen.Text()
	if strings.Contains(screen, paragraph) {
		t.Fatalf("paragraph did not wrap on screen at 80 columns:\n%s", screen)
	}
	if got, want := normalizedScreenParagraph(screen, "SOFTWRAP_PROBE"), normalizedWords(paragraph); got != want {
		t.Fatalf("screen paragraph = %q, want %q", got, want)
	}
	if !semanticEditorReady(q.screen) {
		t.Fatalf("editor missing below the transcript:\n%s", screen)
	}
	if !strings.Contains(screen, "(anthropic) synthetic-model") {
		t.Fatalf("footer missing below the editor:\n%s", screen)
	}
}

func normalizedWords(text string) string { return strings.Join(strings.Fields(text), " ") }

// normalizedScreenParagraph joins the visual rows of one wrapped paragraph.
// A wrap can split a word, so joining recovers the logical line. Fields
// normalization removes the hard-mode continuation indentation.
func normalizedScreenParagraph(screen, start string) string {
	lines := strings.Split(screen, "\n")
	var joined strings.Builder
	started := false
	for _, line := range lines {
		if !started {
			index := strings.Index(line, start)
			if index < 0 {
				continue
			}
			started = true
			joined.WriteString(line[index:])
			continue
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		joined.WriteString(line)
	}
	return normalizedWords(joined.String())
}

func TestSoftWrapPTYHardSettingKeepsWrappedRows(t *testing.T) {
	q, paragraph := runSoftWrapPTYTurn(t, false)

	if strings.Contains(q.raw.String(), paragraph) {
		t.Fatal("hard mode emitted the paragraph as one contiguous line")
	}
	screen := q.screen.Text()
	if got, want := normalizedScreenParagraph(screen, "SOFTWRAP_PROBE"), normalizedWords(paragraph); got != want {
		t.Fatalf("screen paragraph = %q, want %q", got, want)
	}
}
