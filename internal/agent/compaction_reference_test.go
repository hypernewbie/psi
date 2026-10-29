package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"psi/internal/session"
)

// Capture the opt-in module before TestMain installs the isolated environment.
var piCompactionReferenceModule = os.Getenv("PSI_PI_SESSION_MODULE")
var piReferenceNodePath, _ = exec.LookPath("node")

const piCompactionReferenceScript = `
const m=await import(process.argv[1]);
const entries=m.loadEntriesFromFile(process.argv[2]).filter(e=>e.type!=='session');
const settings={enabled:true,reserveTokens:16384,keepRecentTokens:Number(process.argv[3])};
const p=m.prepareCompaction(entries,settings);
console.log(JSON.stringify(p&&{firstKeptEntryId:p.firstKeptEntryId,isSplitTurn:p.isSplitTurn}));
`

func TestPiCompactionCutPointDifferential(t *testing.T) {
	module := piCompactionReferenceModule
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to the pinned Pi reference adapter")
	}
	if piReferenceNodePath == "" {
		t.Skip("Node is required for the opt-in pinned Pi reference")
	}
	t.Run("ordinary-budget-long-assistant-span", func(t *testing.T) {
		store, _, _, _, _ := testSetup(t)
		if _, err := store.AppendMessage(userMsg("original request")); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 80_000))
		}
		assertPiCompactionCutPoint(t, module, store, 20_000)
	})
	t.Run("previous-summary-and-adjacent-metadata", func(t *testing.T) {
		store, _, _, _, _ := testSetup(t)
		if _, err := store.AppendMessage(userMsg("ancient discarded request")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(userMsg("retained boundary request")); err != nil {
			t.Fatal(err)
		}
		path, err := store.ActivePath()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendCompaction("previous checkpoint", path[len(path)-1].ID(), 100, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(userMsg("ongoing current task")); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 80_000))
		}
		if _, err := store.AppendModelChange("anthropic", "m"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendThinkingLevelChange("high"); err != nil {
			t.Fatal(err)
		}
		appendAssistantText(t, store, strings.Repeat("d", 80_000))
		assertPiCompactionCutPoint(t, module, store, 20_000)
	})
	t.Run("tool-call-result-boundary", func(t *testing.T) {
		store, _, _, _, _ := testSetup(t)
		if _, err := store.AppendMessage(userMsg("older request")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", Content: []session.ContentBlock{{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: "read-one", Name: "read", Arguments: map[string]any{"path": "large.txt"}}}}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{Role: "toolResult", ToolCallID: "read-one", ToolName: "read", Content: []session.UserContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("result ", 2_000)}}}}}); err != nil {
			t.Fatal(err)
		}
		assertPiCompactionCutPoint(t, module, store, 1)
	})
}

func assertPiCompactionCutPoint(t *testing.T, module string, store *session.Store, keepRecentTokens int) {
	t.Helper()
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	got, err := PrepareCompaction(path, projection, keepRecentTokens)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	home := t.TempDir()
	cmd := exec.CommandContext(ctx, piReferenceNodePath, "--no-warnings", "--input-type=module", "-e", piCompactionReferenceScript, module, store.File(), strconv.Itoa(keepRecentTokens))
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin", "PI_CODING_AGENT_DIR=" + filepath.Join(home, "agent"), "PI_CODING_AGENT_SESSION_DIR=" + filepath.Join(home, "sessions")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pinned Pi compaction preparation: %v: %s", err, out)
	}
	var want struct {
		FirstKeptEntryID string `json:"firstKeptEntryId"`
		IsSplitTurn      bool   `json:"isSplitTurn"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &want); err != nil {
		t.Fatalf("decode pinned Pi preparation %q: %v", out, err)
	}
	if got.FirstKeptEntryID != want.FirstKeptEntryID || got.SplitTurn != want.IsSplitTurn {
		t.Fatalf("cut point differs from pinned Pi: psi=%+v Pi=%+v output=%s", got, want, out)
	}
}
