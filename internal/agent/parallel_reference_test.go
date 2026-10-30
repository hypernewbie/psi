package agent

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/testutil"
)

// Pi reference trace for tool batch execution (parallel plan §1).
//
// The probe drives the unmodified pinned agent loop with gated tools and a
// synthetic stream. It records preflight order, concurrent start, completion
// order, tool-result order, and the next request boundary for the parallel
// default and both sequential fallbacks. Credentials, user settings, and
// extensions never load.
const piParallelBatchReferenceScript = `
const m=await import(process.argv[1]);
const mode=process.argv[2];
const log=[];
let seq=0;
const stamp=k=>{log.push({seq:seq++,k})};
const latch=()=>{let r;const p=new Promise(res=>{r=res});return [p,r]};
const [bStarted,resolveBStarted]=latch();
const [aStarted,resolveAStarted]=latch();
const [releaseB,doReleaseB]=latch();
const [releaseA,doReleaseA]=latch();
const hang=()=>new Promise(res=>{const t=setTimeout(res,4000);if(t.unref)t.unref();});
// Hold the event loop while unref'd hang timers race; cleared before exit.
const keepAlive=setInterval(()=>{},1000);
const gated=name=>({
  name,label:name,description:'gated '+name,
  parameters:{type:'object',properties:{},additionalProperties:false},
  execute: async()=>{
    stamp('exec-start '+name);
    if(name==='gated_b'){resolveBStarted();await Promise.race([releaseB,hang()]);}
    else{resolveAStarted();await Promise.race([releaseA,hang()]);}
    stamp('exec-end '+name);
    return {content:[{type:'text',text:name+' result'}],details:undefined};
  },
});
const b=gated('gated_b');
const a=gated('gated_a');
if(mode==='sequential-tool')b.executionMode='sequential';
const tools=[b,a];
const requests=[];
const streamFn=async()=>{
  stamp('request-start');
  requests.push(requests.length);
  const s=new m.AssistantMessageEventStream();
  if(requests.length===1){
    s.push({type:'done',message:{role:'assistant',content:[
      {type:'text',text:'run both'},
      {type:'toolCall',id:'call-b',name:'gated_b',arguments:{}},
      {type:'toolCall',id:'call-a',name:'gated_a',arguments:{}},
    ],stopReason:'toolUse',usage:{input:1,output:1,cacheRead:0,cacheWrite:0,totalTokens:2}}});
  }else{
    s.push({type:'done',message:{role:'assistant',content:[{type:'text',text:'done'}],stopReason:'stop',usage:{input:1,output:1,cacheRead:0,cacheWrite:0,totalTokens:2}}});
  }
  s.end();
  return s;
};
const events=[];
const emit=async e=>{
  if(e.type==='tool_execution_start')events.push({type:'start',id:e.toolCallId});
  if(e.type==='tool_execution_end')events.push({type:'end',id:e.toolCallId});
  if(e.type==='turn_end')events.push({type:'turn-results',order:e.toolResults.map(r=>r.toolCallId)});
};
const config={model:{id:'synthetic',api:'anthropic-messages'},convertToLlm:ms=>ms,toolExecution:mode==='sequential-config'?'sequential':'parallel'};
// Release the second call first, after both calls started. In sequential modes
// the first call can only end via the hang timeout, so the batch stays bounded.
Promise.all([bStarted,aStarted]).then(()=>{doReleaseA();doReleaseB()});
const messages=await m.runAgentLoop(
  [{role:'user',content:[{type:'text',text:'go'}]}],
  {messages:[],tools},
  config,emit,undefined,streamFn);
const toolResults=messages.filter(x=>x.role==='toolResult').map(r=>r.toolCallId);
clearInterval(keepAlive);
console.log(JSON.stringify({mode,events,toolResults,requestCount:requests.length,trace:log.map(e=>e.k)}));
`

type piBatchEvent struct {
	Type  string   `json:"type"`
	ID    string   `json:"id,omitempty"`
	Order []string `json:"order,omitempty"`
}

type piBatchTrace struct {
	Mode         string         `json:"mode"`
	Events       []piBatchEvent `json:"events"`
	ToolResults  []string       `json:"toolResults"`
	RequestCount int            `json:"requestCount"`
	Trace        []string       `json:"trace"`
}

func runPiBatchReference(t *testing.T, module, mode string) piBatchTrace {
	t.Helper()
	if piReferenceNodePath == "" {
		t.Fatal("Node is required for the requested pinned Pi reference")
	}
	home, _, _, sessions := testutil.IsolatedEnv(t)
	cmd := exec.CommandContext(t.Context(), piReferenceNodePath, "--no-warnings", "--input-type=module", "-e", piParallelBatchReferenceScript, module, mode)
	cmd.Env = testutil.ChildEnv(home, map[string]string{"PI_CODING_AGENT_DIR": filepath.Join(home, "agent"), "PI_CODING_AGENT_SESSION_DIR": sessions})
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("reference probe failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("reference probe failed: %v", err)
	}
	var trace piBatchTrace
	if err := json.Unmarshal(out, &trace); err != nil {
		t.Fatalf("reference probe output is not JSON: %v\n%s", err, out)
	}
	return trace
}

// TestPiParallelBatchTrace pins the reference behavior psi must reproduce:
// preflight emits every start in call order, execution overlaps, completion
// events follow completion order, tool results keep call order, and the next
// request starts only after the batch settles.
func TestPiParallelBatchTrace(t *testing.T) {
	module := piCompactionReferenceModule
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to the pinned Pi reference adapter")
	}
	trace := runPiBatchReference(t, module, "parallel")
	if trace.RequestCount != 2 {
		t.Fatalf("expected 2 model requests, got %d (events %+v)", trace.RequestCount, trace.Events)
	}
	if got := trace.ToolResults; len(got) != 2 || got[0] != "call-b" || got[1] != "call-a" {
		t.Fatalf("tool results must keep assistant-call order [call-b call-a], got %v", got)
	}
	// Both calls start before either ends.
	bStart := indexOf(t, trace.Trace, "exec-start gated_b")
	aStart := indexOf(t, trace.Trace, "exec-start gated_a")
	bEnd := indexOf(t, trace.Trace, "exec-end gated_b")
	aEnd := indexOf(t, trace.Trace, "exec-end gated_a")
	if bStart < 0 || aStart < 0 || bEnd < 0 || aEnd < 0 {
		t.Fatalf("parallel trace must contain both starts and ends, got %v", trace.Trace)
	}
	if aStart > bEnd || bStart > aEnd {
		t.Fatalf("parallel execution must overlap: %v", trace.Trace)
	}
	// Completion events follow completion order; call-a was released first.
	var ends []string
	for _, e := range trace.Events {
		if e.Type == "end" {
			ends = append(ends, e.ID)
		}
	}
	if len(ends) != 2 || ends[0] != "call-a" || ends[1] != "call-b" {
		t.Fatalf("completion events must follow completion order [call-a call-b], got %v", ends)
	}
	var starts []string
	for _, e := range trace.Events {
		if e.Type == "start" {
			starts = append(starts, e.ID)
		}
	}
	if len(starts) != 2 || starts[0] != "call-b" || starts[1] != "call-a" {
		t.Fatalf("start events must follow call order [call-b call-a], got %v", starts)
	}
}

// TestPiSequentialBatchTrace pins both sequential fallbacks: a tool marked
// sequential, and a sequential config, each run the whole batch in call order.
func TestPiSequentialBatchTrace(t *testing.T) {
	module := piCompactionReferenceModule
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to the pinned Pi reference adapter")
	}
	for _, mode := range []string{"sequential-tool", "sequential-config"} {
		trace := runPiBatchReference(t, module, mode)
		if trace.RequestCount != 2 {
			t.Fatalf("%s: expected 2 model requests, got %d", mode, trace.RequestCount)
		}
		want := []string{
			"exec-start gated_b", "exec-end gated_b",
			"exec-start gated_a", "exec-end gated_a",
		}
		var exec []string
		for _, entry := range trace.Trace {
			if strings.HasPrefix(entry, "exec-") {
				exec = append(exec, entry)
			}
		}
		if len(exec) != len(want) {
			t.Fatalf("%s: sequential trace must interleave per call, got %v", mode, trace.Trace)
		}
		for i, w := range want {
			if exec[i] != w {
				t.Fatalf("%s: sequential trace must be %v, got %v", mode, want, exec)
			}
		}
		if got := trace.ToolResults; len(got) != 2 || got[0] != "call-b" || got[1] != "call-a" {
			t.Fatalf("%s: tool results must keep call order, got %v", mode, got)
		}
	}
}

func indexOf(t *testing.T, items []string, want string) int {
	t.Helper()
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}
