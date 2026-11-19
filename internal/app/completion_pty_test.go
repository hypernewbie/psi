//go:build unix

package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"psi/internal/testutil"
)

const exactBumpCompletionRow = "→ /bump  Request another response without a new prompt"

// exactCompletionState reports whether the screen shows draft on the cursor
// row and the exact /bump suggestion on a separate row below it. Terminal
// padding is ignored; other text on those rows is a mismatch.
func exactCompletionState(screen *testutil.VTModel, draft string) (hasDraft, hasRow bool) {
	rows := strings.Split(screen.Text(), "\n")
	for i, row := range rows {
		trimmed := strings.TrimRight(row, " ")
		if i == screen.CursorRow && trimmed == draft {
			hasDraft = true
		}
		if i > screen.CursorRow && trimmed == exactBumpCompletionRow {
			hasRow = true
		}
	}
	return hasDraft, hasRow
}

func TestActualAppExactSlashCompletion(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("set -psi-test-binary to run the actual-app exact completion check")
	}
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	// startReviewPTY registers its cleanup after this one, so the child stops
	// before the server closes and the request count becomes final.
	t.Cleanup(func() {
		server.Close()
		if got := requests.Load(); got != 0 {
			t.Errorf("provider requests = %d, want 0", got)
		} else {
			t.Log("provider requests: 0")
		}
	})
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"editorPaddingX":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"m","maxTokens":16384,"contextWindow":200000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-exact-completion-key"}}`)
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-session", "--no-tools", "--no-context-files", "--no-skills")
	waitScreenState(q, "ready editor", semanticEditorReady)

	// Writer.Emit restores the cursor after it writes the changed rows. These
	// sequences therefore mark the end of the frame for an ASCII draft at the
	// start of the editor body.
	q.pending = ""
	q.write("/bum")
	q.wait("\r\x1b[4C")
	if !semanticEditorReady(q.screen) {
		t.Errorf("after /bum: editor not ready; screen=%q", q.screen.Text())
	}
	if hasDraft, hasRow := exactCompletionState(q.screen, "/bum"); !hasDraft || !hasRow {
		t.Errorf("after /bum: draft=%v row=%v screen=%q", hasDraft, hasRow, q.screen.Text())
	}

	q.pending = ""
	q.write("p")
	q.wait("\r\x1b[5C")
	if !semanticEditorReady(q.screen) {
		t.Errorf("after /bump: editor not ready; screen=%q", q.screen.Text())
	}
	if hasDraft, hasRow := exactCompletionState(q.screen, "/bump"); !hasDraft || !hasRow {
		t.Errorf("after /bump: draft=%v row=%v screen=%q", hasDraft, hasRow, q.screen.Text())
	}

	q.write("\x03")
	waitScreenState(q, "cleared draft", func(screen *testutil.VTModel) bool {
		hasDraft, _ := exactCompletionState(screen, "")
		return semanticEditorReady(screen) && hasDraft && !strings.Contains(screen.Text(), "/bump")
	})
	q.write("\x03")
	q.finish()
}
