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
const [module,mode,path,provider,api,model]=process.argv.slice(1);
const {SessionManager}=await import(module);
const usage={input:11,output:7,cacheRead:3,cacheWrite:2,totalTokens:23,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}};
const responseAPI=api==='openai-responses'||api==='openai-codex-responses';
const callId=responseAPI?'call_synthetic|fc_synthetic':'call_synthetic';
const assistant=(text,tools=false)=>({role:'assistant',provider,api,model,responseModel:model+'-returned',responseId:'synthetic-response',thinkingLevel:'high',providerThinkingLevel:'high',content:[
 {type:'thinking',thinking:'synthetic reasoning',thinkingSignature:responseAPI?JSON.stringify({id:'rs_synthetic',type:'reasoning',summary:[],encrypted_content:'synthetic-encrypted-reasoning'}):'synthetic-signed-thinking'},
 {type:'text',text,textSignature:JSON.stringify({v:1,id:'msg_synthetic',phase:'commentary'})},
 ...(tools?[{type:'toolCall',id:callId,name:'read',namespace:'synthetic_namespace',arguments:{path:'synthetic.txt'},thoughtSignature:'synthetic-call-signature'}]:[])],usage,stopReason:tools?'toolUse':'stop',timestamp:101});
if(mode==='create'){
 const s=SessionManager.create('/synthetic/work',join(dirname(path),'pi-generated'));
 s.appendMessage({role:'system',content:'synthetic base',sections:{one:'section one',two:'section two'},toolsAdded:[{name:'read',description:'read a file',parameters:{type:'object',properties:{path:{type:'string'}}}}],timestamp:1});
 s.appendModelChange(provider,model);s.appendThinkingLevelChange('high');
 const user=s.appendMessage({role:'user',content:[{type:'text',text:'synthetic original'},{type:'image',mimeType:'image/png',data:'c3ludGhldGlj'}],timestamp:2});
 s.appendMessage(assistant('abandoned sibling'));s.branch(user);
 s.appendMessage(assistant('selected branch',true));
 s.appendMessage({role:'toolResult',toolCallId:callId,toolName:'read',content:[{type:'text',text:'HIDDEN_SYNTHETIC_TOOL_BODY'}],details:{future:{preserve:true}},isError:false,timestamp:3});
 s.appendCustomEntry('synthetic.opaque',{future:[1,{keep:true}]});
 s.appendCustomMessageEntry('synthetic.visible','visible custom',true);
 s.appendCustomMessageEntry('synthetic.hidden','hidden custom context',false);
 s.appendContextEdit(user,{content:'edited synthetic prompt'});
 s.appendMessage({role:'system',content:'patch',sections:{one:null,three:'section three'},toolsRemoved:[{name:'read'}],toolsAdded:[{name:'bash',description:'shell'}],timestamp:4});
 s.appendUsage('synthetic',provider,model,usage,'synthetic usage');s.appendSessionInfo('Synthetic reference title');
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

type referenceCase struct {
	name, provider, api, model string
}

func runReferenceJourney(t *testing.T, module, mode, path string, reference referenceCase) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--no-warnings", "--input-type=module", "-e", referenceJourney, module, mode, path, reference.provider, reference.api, reference.model)
	home := t.TempDir()
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin", "PI_CODING_AGENT_DIR=" + filepath.Join(home, "agent"), "PI_CODING_AGENT_SESSION_DIR=" + filepath.Join(home, "sessions")}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Pi reference %s: %v: %s", mode, err, out)
	}
}

func appendReferencePsiContinuation(t *testing.T, s *Store, reference referenceCase) {
	t.Helper()
	id, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "expanded synthetic skill input", Timestamp: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendProvenance(id, "/skill:synthetic fix", "skill"); err != nil {
		t.Fatal(err)
	}
	signature := "synthetic-signed-thinking"
	if reference.api == "openai-responses" || reference.api == "openai-codex-responses" {
		signature = `{"id":"rs_psi","type":"reasoning","encrypted_content":"synthetic-encrypted-reasoning","summary":[]}`
	}
	raw, _ := json.Marshal(map[string]any{"role": "assistant", "provider": reference.provider, "api": reference.api, "model": reference.model, "responseModel": reference.model + "-returned", "responseId": "psi-synthetic-response", "thinkingLevel": "high", "providerThinkingLevel": "high", "futureOpaque": map[string]any{"preserved": true}, "content": []any{map[string]any{"type": "thinking", "thinking": "synthetic reasoning", "thinkingSignature": signature}, map[string]any{"type": "text", "text": "psi continuation", "textSignature": `{"v":1,"id":"msg_psi","phase":"final"}`}}, "usage": Usage{Input: 11, Output: 7, CacheRead: 3, CacheWrite: 2, TotalTokens: 23}, "stopReason": "stop", "timestamp": 101})
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

// Both directions use the configured Pi SessionManager writer and reader.
// The default suite skips this explicit external-reference dependency.
func TestReferencePiContinuationMatrix(t *testing.T) {
	module := os.Getenv("PSI_PI_SESSION_MODULE")
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to an optional Pi session-manager adapter")
	}
	cases := []referenceCase{
		{"anthropic", "anthropic", "anthropic-messages", "claude-opus-4-6"},
		{"openai-codex", "openai-codex", "openai-codex-responses", "gpt-5.6-luna"},
		{"minimax", "minimax", "anthropic-messages", "synthetic-minimax"},
		{"opencode-go-anthropic", "opencode-go", "anthropic-messages", "synthetic-opencode-anthropic"},
		{"deepseek", "deepseek", "openai-completions", "synthetic-deepseek"},
		{"zai", "zai", "openai-completions", "synthetic-zai"},
		{"opencode-go-completions", "opencode-go", "openai-completions", "synthetic-opencode-completions"},
		{"google", "google", "google-generative-ai", "synthetic-google"},
		{"opencode-go-google", "opencode-go", "google-generative-ai", "synthetic-opencode-google"},
		{"opencode-go-responses", "opencode-go", "openai-responses", "synthetic-opencode-responses"},
	}
	for _, reference := range cases {
		for _, origin := range []string{"Pi", "psi"} {
			t.Run(origin+"/"+reference.name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "session.jsonl")
				if origin == "Pi" {
					runReferenceJourney(t, module, "create", path, reference)
				} else {
					fresh, err := New(OpenOptions{Cwd: "/synthetic/work", SessionDir: root, Persist: true})
					if err != nil {
						t.Fatal(err)
					}
					appendReferencePsiContinuation(t, fresh, reference)
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
				appendReferencePsiContinuation(t, store, reference)
				assertReferenceProjection(t, module, path, store)
				beforePi, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasPrefix(beforePi, prefix) {
					t.Fatal("psi changed the original prefix")
				}
				runReferenceJourney(t, module, "continue", path, reference)
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
				if last == nil || last.Provider != reference.provider || last.ResponseID == nil || *last.ResponseID != "synthetic-response" {
					t.Fatal("returned provider metadata lost on continuation")
				}
			})
		}
	}
}
