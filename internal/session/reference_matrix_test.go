package session

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const referenceJourney = `
import {copyFileSync} from 'node:fs';
import {dirname,join} from 'node:path';
const [module,mode,path,family]=process.argv.slice(1);
const {SessionManager}=await import(module);
const api=family==='anthropic'?'anthropic-messages':family==='openai-codex'?'openai-codex-responses':'openai-responses';
const model=family==='anthropic'?'claude-opus-4-6':family==='openai-codex'?'gpt-5.6-luna':'gpt-5.6-luna';
const usage={input:11,output:7,cacheRead:3,cacheWrite:2,totalTokens:23,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}};
const assistant=(text,tools=false)=>({role:'assistant',provider:family,api,model,responseModel:model+'-returned',responseId:'synthetic-response',thinkingLevel:'high',providerThinkingLevel:'high',content:[
 {type:'thinking',thinking:'synthetic reasoning',thinkingSignature:family==='anthropic'?'synthetic-signed-thinking':JSON.stringify({id:'rs_synthetic',type:'reasoning',summary:[],encrypted_content:'synthetic-encrypted-reasoning'})},
 {type:'text',text,textSignature:family==='anthropic'?'synthetic-text-signature':JSON.stringify({v:1,id:'msg_synthetic',phase:'commentary'})},
 ...(tools?[{type:'toolCall',id:family==='anthropic'?'call_synthetic':'call_synthetic|fc_synthetic',name:'read',namespace:'synthetic_namespace',arguments:{path:'synthetic.txt'},thoughtSignature:'synthetic-call-signature'}]:[])],usage,stopReason:tools?'toolUse':'stop',timestamp:101});
if(mode==='create'){
 const s=SessionManager.create('/synthetic/work',join(dirname(path),'pi-generated'));
 s.appendMessage({role:'system',content:'synthetic base',sections:{one:'section one',two:'section two'},toolsAdded:[{name:'read',description:'read a file',parameters:{type:'object',properties:{path:{type:'string'}}}}],timestamp:1});
 s.appendModelChange(family,model);s.appendThinkingLevelChange('high');
 const user=s.appendMessage({role:'user',content:[{type:'text',text:'synthetic original'},{type:'image',mimeType:'image/png',data:'c3ludGhldGlj'}],timestamp:2});
 s.appendMessage(assistant('abandoned sibling'));s.branch(user);
 s.appendMessage(assistant('selected branch',true));
 s.appendMessage({role:'toolResult',toolCallId:family==='anthropic'?'call_synthetic':'call_synthetic|fc_synthetic',toolName:'read',content:[{type:'text',text:'HIDDEN_SYNTHETIC_TOOL_BODY'}],details:{future:{preserve:true}},isError:false,timestamp:3});
 s.appendCustomEntry('synthetic.opaque',{future:[1,{keep:true}]});
 s.appendCustomMessageEntry('synthetic.visible','visible custom',true);
 s.appendCustomMessageEntry('synthetic.hidden','hidden custom context',false);
 s.appendContextEdit(user,{content:'edited synthetic prompt'});
 s.appendMessage({role:'system',content:'patch',sections:{one:null,three:'section three'},toolsRemoved:[{name:'read'}],toolsAdded:[{name:'bash',description:'shell'}],timestamp:4});
 s.appendUsage('synthetic',family,model,usage,'synthetic usage');s.appendSessionInfo('Synthetic reference title');
 const kept=s.appendMessage({role:'user',content:'kept synthetic prompt',timestamp:5});
 s.appendCompaction('Pi-generated summary',kept,400,{synthetic:true},false,usage);
 s.appendMessage(assistant('after Pi compaction'));
 copyFileSync(s.getSessionFile(),path);
} else {
 const s=SessionManager.open(path);
 s.appendMessage({role:'user',content:'Pi resumed after psi',timestamp:201});
 s.appendMessage(assistant('Pi continuation complete'));
}
console.log('reference continuation complete');
`

func runReferenceJourney(t *testing.T, module, mode, path, family string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--no-warnings", "--input-type=module", "-e", referenceJourney, module, mode, path, family)
	home := t.TempDir()
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin", "PI_CODING_AGENT_DIR=" + filepath.Join(home, "agent"), "PI_CODING_AGENT_SESSION_DIR=" + filepath.Join(home, "sessions")}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pinned Pi %s: %v: %s", mode, err, out)
	}
}

func appendReferencePsiContinuation(t *testing.T, s *Store, family string) {
	t.Helper()
	id, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "expanded synthetic skill input", Timestamp: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendProvenance(id, "/skill:synthetic fix", "skill"); err != nil {
		t.Fatal(err)
	}
	api, model, signature := "anthropic-messages", "claude-opus-4-6", "synthetic-signed-thinking"
	if family == "openai-codex" {
		api, model = "openai-codex-responses", "gpt-5.6-luna"
		signature = `{"id":"rs_psi","type":"reasoning","encrypted_content":"synthetic-encrypted-reasoning","summary":[]}`
	} else if family == "opencode-go" {
		api, model = "openai-responses", "gpt-5.6-luna"
		signature = `{"id":"rs_psi","type":"reasoning","encrypted_content":"synthetic-encrypted-reasoning","summary":[]}`
	}
	raw, _ := json.Marshal(map[string]any{"role": "assistant", "provider": family, "api": api, "model": model, "responseModel": model + "-returned", "responseId": "psi-synthetic-response", "thinkingLevel": "high", "providerThinkingLevel": "high", "futureOpaque": map[string]any{"preserved": true}, "content": []any{map[string]any{"type": "thinking", "thinking": "synthetic reasoning", "thinkingSignature": signature}, map[string]any{"type": "text", "text": "psi continuation", "textSignature": `{"v":1,"id":"msg_psi","phase":"final"}`}}, "usage": Usage{Input: 11, Output: 7, CacheRead: 3, CacheWrite: 2, TotalTokens: 23}, "stopReason": "stop", "timestamp": 101})
	var message AgentMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	proj, err := s.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCompaction("psi-generated summary", id, 600, ReplaySystem(proj.Flat).Checkpoint); err != nil {
		t.Fatal(err)
	}
}

// Both directions use the actual, unmodified pinned SessionManager writer and
// reader. The default suite skips this explicit external-reference dependency.
func TestReferencePiContinuationMatrix(t *testing.T) {
	module := os.Getenv("PSI_PI_SESSION_MODULE")
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to the pinned reference adapter")
	}
	for _, family := range []string{"anthropic", "openai-codex", "opencode-go"} {
		for _, origin := range []string{"Pi", "psi"} {
			t.Run(origin+"/"+family, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "session.jsonl")
				if origin == "Pi" {
					runReferenceJourney(t, module, "create", path, family)
				} else {
					fresh, err := New(OpenOptions{Cwd: "/synthetic/work", SessionDir: root, Persist: true})
					if err != nil {
						t.Fatal(err)
					}
					appendReferencePsiContinuation(t, fresh, family)
					path = fresh.File()
				}
				prefix, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				store, err := OpenFile(path, "/synthetic/work", true)
				if err != nil {
					t.Fatal(err)
				}
				assertReferenceProjection(t, module, path, store)
				appendReferencePsiContinuation(t, store, family)
				assertReferenceProjection(t, module, path, store)
				beforePi, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasPrefix(beforePi, prefix) {
					t.Fatal("psi changed the original prefix")
				}
				runReferenceJourney(t, module, "continue", path, family)
				afterPi, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasPrefix(afterPi, beforePi) {
					t.Fatal("Pi changed the psi-written prefix")
				}
				reopened, err := OpenFile(path, "/synthetic/work", true)
				if err != nil {
					t.Fatal(err)
				}
				assertReferenceProjection(t, module, path, reopened)
				recall, err := reopened.Recall()
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(strings.Join(recall, "\n"), "/skill:synthetic fix") {
					t.Fatal("Pi continuation lost psi input provenance")
				}
				proj, err := reopened.Projection()
				if err != nil {
					t.Fatal(err)
				}
				last := proj.Flat[len(proj.Flat)-1].Assistant
				if last == nil || last.Provider != family || last.ResponseID == nil || *last.ResponseID != "synthetic-response" {
					t.Fatal("returned provider metadata lost on continuation")
				}
			})
		}
	}
}
