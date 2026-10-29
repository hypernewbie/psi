package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"psi/internal/modelmeta"
)

func TestResponsesRequestMatchesOptionalPiReference(t *testing.T) {
	module := os.Getenv("PSI_PI_RESPONSES_MODULE")
	if module == "" {
		t.Skip("set PSI_PI_RESPONSES_MODULE to an optional Pi openai-responses-shared module")
	}
	model := ModelMeta{
		ID: "synthetic-responses", API: "openai-responses", Provider: "opencode-go", BaseURL: "https://opencode.ai/inference/go/v1",
		Reasoning: true, InputImage: true,
		ThinkingLevelMap: map[string]*string{"off": nil, "low": responsesString("low"), "high": responsesString("high")},
		Compat:           modelmeta.Compat{"supportsMidConvoSystemMessages": json.RawMessage(`true`), "supportsAdditionalTools": json.RawMessage(`true`), "sessionAffinityFormat": json.RawMessage(`"openai-nosession"`)},
	}
	readTool := ToolDef{Name: "read", Description: "Read a local file", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	writeTool := ToolDef{Name: "write", Description: "Write a local file", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}
	messages := []Message{
		{Role: "system", Content: []Block{{Type: "text", Text: "base instructions"}}, Sections: []SystemSection{{Name: "psi", Value: responsesString("workspace instructions")}}, ToolsAdded: []ToolDef{readTool}},
		{Role: "user", Content: []Block{{Type: "text", Text: "read fixture.txt"}}},
		{Role: "assistant", Provider: model.Provider, API: model.API, Model: model.ID, Content: []Block{
			{Type: "thinking", Thinking: "plan", ThinkingSignature: `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`},
			{Type: "text", Text: "reading", TextSignature: `{"v":1,"id":"msg_1","phase":"commentary"}`},
			{Type: "toolCall", ID: "call_1|fc_1", Name: "read", Arguments: map[string]any{"path": "fixture.txt"}},
		}},
		{Role: "toolResult", ToolCallID: "call_1|fc_1", ToolName: "read", Content: []Block{{Type: "text", Text: "fixture contents"}}},
		{Role: "system", Sections: []SystemSection{{Name: "psi", Value: responsesString("updated workspace instructions")}}, ToolsAdded: []ToolDef{writeTool}, ToolsRemoved: []string{"read"}},
	}
	request := Request{Model: model, Thinking: "off", Tools: []ToolDef{writeTool}, Messages: messages}
	body, err := BuildResponsesRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	piMessages := []any{
		map[string]any{"role": "system", "content": "base instructions", "sections": map[string]any{"psi": "workspace instructions"}, "toolsAdded": []any{map[string]any{"name": "read", "description": "Read a local file", "parameters": readTool.Schema}}},
		map[string]any{"role": "user", "content": "read fixture.txt"},
		map[string]any{"role": "assistant", "provider": model.Provider, "api": model.API, "model": model.ID, "stopReason": "toolUse", "content": []any{
			map[string]any{"type": "thinking", "thinking": "plan", "thinkingSignature": `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`},
			map[string]any{"type": "text", "text": "reading", "textSignature": `{"v":1,"id":"msg_1","phase":"commentary"}`},
			map[string]any{"type": "toolCall", "id": "call_1|fc_1", "name": "read", "arguments": map[string]any{"path": "fixture.txt"}},
		}},
		map[string]any{"role": "toolResult", "toolCallId": "call_1|fc_1", "toolName": "read", "content": []any{map[string]any{"type": "text", "text": "fixture contents"}}},
		map[string]any{"role": "system", "sections": map[string]any{"psi": "updated workspace instructions"}, "toolsAdded": []any{map[string]any{"name": "write", "description": "Write a local file", "parameters": writeTool.Schema}}, "toolsRemoved": []any{map[string]any{"name": "read"}}},
	}
	input, err := json.Marshal(map[string]any{
		"model":    map[string]any{"id": model.ID, "provider": model.Provider, "api": model.API, "reasoning": true, "input": []string{"text", "image"}, "thinkingLevelMap": map[string]*string{"off": nil, "low": responsesString("low"), "high": responsesString("high")}, "compat": map[string]any{"supportsMidConvoSystemMessages": true, "supportsAdditionalTools": true, "sessionAffinityFormat": "openai-nosession"}},
		"messages": piMessages,
	})
	if err != nil {
		t.Fatal(err)
	}
	piInput, err := responsesPiInput(t, module, input)
	if err != nil {
		t.Fatal(err)
	}
	goInput, err := json.Marshal(body["input"])
	if err != nil {
		t.Fatal(err)
	}
	var piValue, goValue any
	if err := json.Unmarshal(piInput, &piValue); err != nil {
		t.Fatalf("Pi normalized input: %s: %v", piInput, err)
	}
	if err := json.Unmarshal(goInput, &goValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(piValue, goValue) {
		t.Fatalf("Pi Responses request input differs:\nPi: %s\nGo: %s", piInput, goInput)
	}
}

func responsesPiInput(t *testing.T, module string, input []byte) ([]byte, error) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("find Node for optional Pi reference: %w", err)
	}
	dir := t.TempDir()
	script := `import fs from "node:fs"; const p=JSON.parse(fs.readFileSync(0,"utf8")); const m=await import(process.argv[1]); const x=m.convertResponsesMessages(p.model,{messages:p.messages},new Set(["openai","openai-codex","opencode"]),{supportsMidConvoSystemMessages:true,supportsAdditionalTools:true,toolOptions:{supportsStrictMode:false}}); process.stdout.write(JSON.stringify(x));`
	cmd := exec.Command(node, "--no-warnings", "--input-type=module", "-e", script, module)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/local/bin:/usr/bin:/bin", "TZ=UTC"}
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("Pi Responses request reference: %w: %s", err, output)
	}
	return output, nil
}
