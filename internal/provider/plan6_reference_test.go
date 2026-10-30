package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/url"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"psi/internal/testutil"
)

var plan6PiAPIDir = flag.String("psi-test-pi-ai-api-dir", "", "public pi-ai dist/api directory for optional PLAN6 payload comparisons")

func TestReviewPlan6OptionalPiPayloads(t *testing.T) {
	if *plan6PiAPIDir == "" {
		t.Skip("pass -psi-test-pi-ai-api-dir to request Pi payload comparisons")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the requested Pi reference")
	}
	cases := []struct {
		api, provider, id, level string
		mapping                  map[string]*string
	}{
		{"google-generative-ai", "google", "gemini-4-synthetic", "off", nil},
		{"google-generative-ai", "google", "gemini-2.5-pro", "high", map[string]*string{"high": completionString("low")}},
		{"openai-completions", "deepseek", "synthetic", "off", map[string]*string{"off": nil, "minimal": nil, "low": completionString("low")}},
		{"openai-completions", "zai", "synthetic", "off", map[string]*string{"off": nil, "minimal": nil, "low": completionString("low")}},
		{"openai-completions", "opencode-go", "synthetic", "max", nil},
		{"openai-responses", "opencode-go", "synthetic", "off", nil},
	}
	for _, tc := range cases {
		t.Run(tc.api+"/"+tc.provider+"/"+tc.id, func(t *testing.T) {
			m := ModelMeta{API: tc.api, Provider: tc.provider, ID: tc.id, Reasoning: true, MaxTokens: 8192, ContextWindow: 100000, ThinkingLevelMap: tc.mapping, BaseURL: "https://opencode.ai/inference/go/v1"}
			body, err := BuildRequest(Request{Model: m, Thinking: tc.level, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			// Only compare fields at issue. The hook stops before SDK serialization
			// or HTTP, so this is an adapter comparison, not a live wire claim.
			selected := map[string]any{}
			keys := []string{"thinking", "reasoning_effort", "max_tokens", "max_completion_tokens", "max_output_tokens"}
			if tc.api == "google-generative-ai" {
				selected["thinkingConfig"] = body["generationConfig"].(map[string]any)["thinkingConfig"]
			} else {
				for _, key := range keys {
					if value, ok := body[key]; ok {
						selected[key] = value
					}
				}
			}
			module, err := filepath.Abs(filepath.Join(*plan6PiAPIDir, tc.api+".js"))
			if err != nil {
				t.Fatal(err)
			}
			moduleURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(module)}).String()
			input, _ := json.Marshal(map[string]any{"module": moduleURL, "api": tc.api, "level": tc.level, "keys": keys, "model": map[string]any{
				"api": tc.api, "provider": tc.provider, "id": tc.id, "baseUrl": m.BaseURL, "maxTokens": 8192, "contextWindow": 100000, "reasoning": true, "input": []string{"text"}, "thinkingLevelMap": tc.mapping,
			}})
			script := `import fs from 'node:fs'; const p=JSON.parse(fs.readFileSync(0,'utf8')); globalThis.fetch=async()=>{throw new Error('network disabled')}; const m=await import(p.module); let captured; const stream=m.streamSimple(p.model,{messages:[{role:'user',content:'hello'}]},{apiKey:'dummy-key',reasoning:p.level,onPayload:(body)=>{captured=body;throw new Error('capture complete')}}); const result=await stream.result(); if(!captured)throw new Error(result.errorMessage||'payload hook not called'); const selected={}; if(p.api==='google-generative-ai')selected.thinkingConfig=captured.config.thinkingConfig;else for(const k of p.keys)if(captured[k]!==undefined)selected[k]=captured[k]; process.stdout.write(JSON.stringify(selected));`
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			cmd := exec.CommandContext(ctx, node, "--no-warnings", "--input-type=module", "-e", script)
			cmd.Dir = dir
			cmd.Env = testutil.ChildEnv(dir, nil)
			cmd.Stdin = bytes.NewReader(input)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("requested Pi reference failed: %v: %s", err, out)
			}
			var got, want any
			goJSON, _ := json.Marshal(selected)
			if json.Unmarshal(out, &want) != nil || json.Unmarshal(goJSON, &got) != nil {
				t.Fatalf("invalid reference output: %s", out)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("payload differs: Go=%s Pi=%s", goJSON, out)
			}
		})
	}
}
