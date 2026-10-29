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

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/testutil"
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

const piCompactionSummaryReferenceScript = `
const m=await import(process.argv[1]);
const entries=m.loadEntriesFromFile(process.argv[2]).filter(e=>e.type!=='session');
const preparation=m.prepareCompaction(entries,{enabled:true,reserveTokens:16384,keepRecentTokens:20000});
const requests=[];
const usage={input:20,output:5,cacheRead:0,cacheWrite:0,totalTokens:25,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}};
const stream=async(_model,context)=>{requests.push(context);const n=requests.length;return {result:async()=>({role:'assistant',content:[{type:'text',text:n===1?'SYNTHETIC_HISTORY':'SYNTHETIC_TURN'}],stopReason:'stop',usage})}};
const result=await m.compact(preparation,{id:'synthetic-model',api:'anthropic-messages',reasoning:false},undefined,undefined,process.argv[3],undefined,undefined,stream);
const prompts=requests.map(context=>{const message=context.messages.filter(m=>m.role==='user').at(-1);const content=message.content;const text=typeof content==='string'?content:content.filter(b=>b.type==='text').map(b=>b.text).join('\\n');return {hasPreviousSummary:text.includes('previous checkpoint'),hasRetainedBoundary:text.includes('retained boundary request'),hasAncient:text.includes('ancient discarded request'),hasCurrentTask:text.includes('ongoing current task'),hasFocus:text.includes('Additional focus: '+process.argv[3])}});
console.log(JSON.stringify({firstKeptEntryId:preparation.firstKeptEntryId,isSplitTurn:preparation.isSplitTurn,prompts,summary:result.summary,totalTokens:result.usage.totalTokens}));
`

func TestPiCompactionSummaryUsesInjectedStream(t *testing.T) {
	module := piCompactionReferenceModule
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to the pinned Pi reference adapter")
	}
	if piReferenceNodePath == "" {
		t.Fatal("Node is required for the requested pinned Pi reference")
	}
	const focus = "Preserve the deployment constraints."
	store, _, fake, loop, _ := testSetup(t)
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
	for i := 0; i < 4; i++ {
		appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 80_000))
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	path, err = store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareCompaction(path, projection, 20_000)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	home := t.TempDir()
	cmd := exec.CommandContext(ctx, piReferenceNodePath, "--no-warnings", "--input-type=module", "-e", piCompactionSummaryReferenceScript, module, store.File(), focus)
	cmd.Dir = home
	cmd.Env = testutil.ChildEnv(home, map[string]string{"PI_CODING_AGENT_DIR": filepath.Join(home, "agent"), "PI_CODING_AGENT_SESSION_DIR": filepath.Join(home, "sessions")})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pinned Pi synthetic compaction: %v: %s", err, out)
	}
	var pi struct {
		FirstKeptEntryID string `json:"firstKeptEntryId"`
		IsSplitTurn      bool   `json:"isSplitTurn"`
		Prompts          []struct {
			HasPreviousSummary  bool `json:"hasPreviousSummary"`
			HasRetainedBoundary bool `json:"hasRetainedBoundary"`
			HasAncient          bool `json:"hasAncient"`
			HasCurrentTask      bool `json:"hasCurrentTask"`
			HasFocus            bool `json:"hasFocus"`
		} `json:"prompts"`
		Summary     string `json:"summary"`
		TotalTokens int    `json:"totalTokens"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &pi); err != nil {
		t.Fatalf("decode Pi compaction result %q: %v", out, err)
	}
	if pi.FirstKeptEntryID != prepared.FirstKeptEntryID || pi.IsSplitTurn != prepared.SplitTurn {
		t.Fatalf("Pi compaction preparation differs: psi=%+v Pi=%+v", prepared, pi)
	}
	if len(pi.Prompts) != 2 || !pi.Prompts[0].HasPreviousSummary || !pi.Prompts[0].HasRetainedBoundary || pi.Prompts[0].HasAncient || !pi.Prompts[1].HasCurrentTask {
		t.Fatalf("Pi split-summary requests lost or invented context: %+v", pi.Prompts)
	}
	fake.Responses = []*provider.Response{
		{Text: "SYNTHETIC_HISTORY", StopReason: "stop", Usage: provider.Usage{Input: 20, Output: 5, TotalTokens: 25}},
		{Text: "SYNTHETIC_TURN", StopReason: "stop", Usage: provider.Usage{Input: 20, Output: 5, TotalTokens: 25}},
	}
	if err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, focus); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != len(pi.Prompts) {
		t.Fatalf("summary requests differ: psi=%d Pi=%d", len(fake.Calls), len(pi.Prompts))
	}
	for i, call := range fake.Calls {
		if len(call.Messages) != 1 || call.Messages[0].Role != "user" {
			t.Fatalf("psi summary request has unexpected transcript: %+v", call)
		}
		if !pi.Prompts[i].HasFocus || !strings.Contains(call.Messages[0].Content[0].Text, "Additional focus: "+focus) {
			t.Fatalf("summary request %d lost operator focus (Pi=%+v)", i, pi.Prompts[i])
		}
	}
	compacted := store.Entries()[len(store.Entries())-1].Compaction
	if compacted == nil || compacted.FirstKeptEntryID != pi.FirstKeptEntryID || !strings.Contains(compacted.Summary, pi.Summary) || compacted.Usage == nil || compacted.Usage.TotalTokens != float64(pi.TotalTokens) {
		t.Fatalf("psi result differs from pinned Pi's injected-summary flow: psi=%+v Pi=%+v", compacted, pi)
	}
}

func TestPiCompactionCutPointDifferential(t *testing.T) {
	module := piCompactionReferenceModule
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to the pinned Pi reference adapter")
	}
	if piReferenceNodePath == "" {
		t.Fatal("Node is required for the requested pinned Pi reference")
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
	cmd.Env = testutil.ChildEnv(home, map[string]string{"PI_CODING_AGENT_DIR": filepath.Join(home, "agent"), "PI_CODING_AGENT_SESSION_DIR": filepath.Join(home, "sessions")})
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
