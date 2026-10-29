package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

const googleValidSignature = "AAAAAAAAAAAAAAAAAAAAAA=="

func googleTestModel(id, providerID string) ModelMeta {
	return ModelMeta{ID: id, API: "google-generative-ai", Provider: providerID, BaseURL: "https://example.invalid/v1beta",
		MaxTokens: 8192, Reasoning: true, InputImage: true}
}

func googleGenerationConfig(body map[string]any) map[string]any {
	return body["generationConfig"].(map[string]any)
}

func TestGoogleRequestMatchesPiMessageAndToolTranslation(t *testing.T) {
	model := googleTestModel("gemini-3.1-pro-preview", "google")
	model.ThinkingLevelMap = map[string]*string{
		"off": nil, "minimal": nil, "low": completionString("low"), "medium": completionString("medium"),
		"high": completionString("high"), "xhigh": nil, "max": nil,
	}
	req := Request{Model: model, System: "system rules", Thinking: "medium", MaxTokens: 4096,
		Tools: []ToolDef{{Name: "read", Description: "read a file", Schema: map[string]any{
			"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object",
			"properties": map[string]any{"mode": map[string]any{"type": "string", "const": "safe"}, "path": map[string]any{"type": "string"}},
			"required":   []any{"path"},
		}}},
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: "inspect"}, {Type: "image", MimeType: "image/png", Data: "AQID"}}},
			{Role: "assistant", Provider: "google", API: "google-generative-ai", Model: model.ID, Content: []Block{
				{Type: "text", Text: "I will inspect.", TextSignature: googleValidSignature},
				{Type: "thinking", Thinking: "", ThinkingSignature: googleValidSignature},
				{Type: "toolCall", ID: "call|one", Name: "read", Arguments: map[string]any{"path": "a.txt"}, ThoughtSignature: googleValidSignature},
				{Type: "toolCall", ID: "call-2", Name: "read", Arguments: map[string]any{"path": "b.txt"}},
			}},
			{Role: "toolResult", ToolCallID: "call|one", ToolName: "read", Content: []Block{{Type: "text", Text: "alpha"}, {Type: "image", MimeType: "image/png", Data: "BAUG"}}},
			{Role: "toolResult", ToolCallID: "call-2", ToolName: "read", IsError: true, Content: []Block{{Type: "text", Text: "not found"}}},
		},
	}
	body, err := BuildGoogleRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	config := googleGenerationConfig(body)
	if body["config"] != nil || body["tools"] == nil || body["systemInstruction"] == nil || body["model"] != nil {
		t.Fatalf("Google REST body did not match SDK serialization boundaries: %#v", body)
	}
	if config["maxOutputTokens"] != 4096 || config["thinkingConfig"].(map[string]any)["thinkingLevel"] != "MEDIUM" ||
		config["thinkingConfig"].(map[string]any)["includeThoughts"] != true {
		t.Fatalf("Google generation config = %#v", config)
	}
	if body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] != "system rules" {
		t.Fatalf("system instruction = %#v", body["systemInstruction"])
	}
	contents := body["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents grouping = %#v", contents)
	}
	userParts := contents[0].(map[string]any)["parts"].([]any)
	if userParts[0].(map[string]any)["text"] != "inspect" || userParts[1].(map[string]any)["inlineData"].(map[string]any)["mimeType"] != "image/png" {
		t.Fatalf("user text/image translation = %#v", userParts)
	}
	modelParts := contents[1].(map[string]any)["parts"].([]any)
	if len(modelParts) != 4 || modelParts[0].(map[string]any)["thoughtSignature"] != googleValidSignature ||
		modelParts[1].(map[string]any)["thought"] != true || modelParts[1].(map[string]any)["text"] != "" {
		t.Fatalf("assistant signed parts = %#v", modelParts)
	}
	firstCall := modelParts[2].(map[string]any)["functionCall"].(map[string]any)
	if firstCall["id"] != "call_one" || modelParts[2].(map[string]any)["thoughtSignature"] != googleValidSignature {
		t.Fatalf("function call ID/signature = %#v", modelParts[2])
	}
	results := contents[2].(map[string]any)["parts"].([]any)
	if len(results) != 2 {
		t.Fatalf("consecutive function responses were not grouped: %#v", results)
	}
	firstResponse := results[0].(map[string]any)["functionResponse"].(map[string]any)
	if firstResponse["id"] != "call_one" || firstResponse["response"].(map[string]any)["output"] != "alpha" ||
		firstResponse["parts"].([]any)[0].(map[string]any)["inlineData"].(map[string]any)["data"] != "BAUG" {
		t.Fatalf("first function response = %#v", firstResponse)
	}
	secondResponse := results[1].(map[string]any)["functionResponse"].(map[string]any)
	if secondResponse["id"] != "call-2" || secondResponse["response"].(map[string]any)["error"] != "not found" {
		t.Fatalf("error function response = %#v", secondResponse)
	}
	declarations := body["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	parameters := declarations[0].(map[string]any)["parametersJsonSchema"].(map[string]any)
	mode := parameters["properties"].(map[string]any)["mode"].(map[string]any)
	if mode["enum"].([]any)[0] != "safe" || mode["const"] != nil || parameters["$schema"] == nil {
		t.Fatalf("Google parametersJsonSchema const conversion = %#v", parameters)
	}
	if !strings.Contains(string(mustJSON(t, body)), "thoughtSignature") {
		t.Fatal("Google signature fields were not serialized")
	}
}

func TestGoogleRequestUsesLegacyImageToolResponseRoutingAndNoIDs(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash", "google")
	req := Request{Model: model, Thinking: "off", Messages: []Message{
		{Role: "assistant", Provider: "google", Model: model.ID, Content: []Block{{Type: "toolCall", ID: "call-1", Name: "read", Arguments: map[string]any{}}}},
		{Role: "toolResult", ToolCallID: "call-1", ToolName: "read", Content: []Block{{Type: "image", MimeType: "image/jpeg", Data: "AQID"}}},
	}}
	body, err := BuildGoogleRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	contents := body["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("legacy image routing = %#v", contents)
	}
	call := contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if _, exists := call["id"]; exists {
		t.Fatalf("Gemini 2.5 function call should omit ID: %#v", call)
	}
	response := contents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if response["response"].(map[string]any)["output"] != "(see attached image)" || response["parts"] != nil || response["id"] != nil {
		t.Fatalf("Gemini 2.5 function response = %#v", response)
	}
	imageTurn := contents[2].(map[string]any)["parts"].([]any)
	if imageTurn[0].(map[string]any)["text"] != "Tool result image:" || imageTurn[1].(map[string]any)["inlineData"] == nil {
		t.Fatalf("legacy separate image turn = %#v", imageTurn)
	}
	config := googleGenerationConfig(body)
	if config["thinkingConfig"].(map[string]any)["thinkingBudget"] != 0 {
		t.Fatalf("disabled Gemini 2.5 thinking = %#v", config["thinkingConfig"])
	}
}

func TestGoogleRequestSignatureRulesAndCrossModelHistory(t *testing.T) {
	model := googleTestModel("gemini-3-flash-preview", "google")
	model.ThinkingLevelMap = map[string]*string{"off": nil, "minimal": completionString("minimal")}
	messages := []Message{{Role: "assistant", Provider: "google", API: "google-generative-ai", Model: model.ID, Content: []Block{
		{Type: "text", Text: "", TextSignature: googleValidSignature},
		{Type: "text", Text: "bad signature", TextSignature: "not-base64!"},
		{Type: "thinking", Thinking: "thought", ThinkingSignature: googleValidSignature},
		{Type: "toolCall", ID: "call/1", Name: "read", Arguments: map[string]any{}, ThoughtSignature: googleValidSignature},
	}}, {Role: "toolResult", ToolCallID: "call/1", ToolName: "read", Content: []Block{{Type: "text", Text: "done"}}}}
	body, err := BuildGoogleRequest(Request{Model: model, Thinking: "off", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	modelParts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if len(modelParts) != 4 || modelParts[0].(map[string]any)["text"] != "" || modelParts[0].(map[string]any)["thoughtSignature"] != googleValidSignature {
		t.Fatalf("same-model signed empty/text blocks = %#v", modelParts)
	}
	if modelParts[1].(map[string]any)["thoughtSignature"] != nil || modelParts[2].(map[string]any)["thought"] != true ||
		modelParts[3].(map[string]any)["thoughtSignature"] != googleValidSignature {
		t.Fatalf("signature validation/replay = %#v", modelParts)

	}
	messages[0].Provider = "deepseek"
	body, err = BuildGoogleRequest(Request{Model: model, Thinking: "off", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	crossParts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if len(crossParts) != 2 || crossParts[0].(map[string]any)["thoughtSignature"] != nil || crossParts[0].(map[string]any)["text"] != "bad signature" ||
		crossParts[1].(map[string]any)["thoughtSignature"] != nil || crossParts[1].(map[string]any)["functionCall"] == nil {
		t.Fatalf("cross-provider signature handling = %#v", crossParts)
	}
	messages[0].Provider = "google"
	messages[0].API = "openai-completions"
	body, err = BuildGoogleRequest(Request{Model: model, Thinking: "off", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	crossAPIParts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if len(crossAPIParts) != 2 || crossAPIParts[1].(map[string]any)["thoughtSignature"] != nil {
		t.Fatalf("cross-API signature handling = %#v", crossAPIParts)
	}
}

func TestGoogleRequestRejectsUnsafeHistoryAndUnknownCompat(t *testing.T) {
	model := googleTestModel("gemini-3-flash-preview", "google")
	model.ThinkingLevelMap = map[string]*string{"off": nil, "minimal": completionString("minimal")}
	for name, messages := range map[string][]Message{
		"orphan result":              {{Role: "toolResult", ToolCallID: "unknown", ToolName: "read"}},
		"wrong result name":          {{Role: "assistant", Provider: "google", Model: model.ID, Content: []Block{{Type: "toolCall", ID: "id-1", Name: "read", Arguments: map[string]any{}}}}, {Role: "toolResult", ToolCallID: "id-1", ToolName: "write"}},
		"normalized ID collision":    {{Role: "assistant", Provider: "google", Model: model.ID, Content: []Block{{Type: "toolCall", ID: "call|id", Name: "read", Arguments: map[string]any{}}, {Type: "toolCall", ID: "call_id", Name: "read", Arguments: map[string]any{}}}}},
		"unsupported result content": {{Role: "assistant", Provider: "google", Model: model.ID, Content: []Block{{Type: "toolCall", ID: "id-1", Name: "read", Arguments: map[string]any{}}}}, {Role: "toolResult", ToolCallID: "id-1", ToolName: "read", Content: []Block{{Type: "thinking", Thinking: "not a result"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildGoogleRequest(Request{Model: model, Thinking: "off", Messages: messages}); err == nil {
				t.Fatal("unsafe Google history was accepted")
			}
		})
	}
	model.Compat = completionCompat(t, map[string]any{"unknownGoogleFlag": true})
	if _, err := BuildGoogleRequest(Request{Model: model, Thinking: "off"}); err == nil || !strings.Contains(err.Error(), "unknownGoogleFlag") {
		t.Fatalf("unknown Google compatibility metadata accepted: %v", err)
	}
}

func TestGoogleThinkingMapsBudgetsAndDisabledBehavior(t *testing.T) {
	cases := []struct {
		id, level string
		want      int
	}{
		{"gemini-2.5-pro", "high", 32768},
		{"gemini-2.5-flash-lite", "minimal", 512},
		{"gemini-2.5-flash", "medium", 8192},
		{"deep-research-preview", "high", -1},
	}
	for _, tc := range cases {
		t.Run(tc.id+"/"+tc.level, func(t *testing.T) {
			model := googleTestModel(tc.id, "google")
			body, err := BuildGoogleRequest(Request{Model: model, Thinking: tc.level})
			if err != nil {
				t.Fatal(err)
			}
			thinking := googleGenerationConfig(body)["thinkingConfig"].(map[string]any)
			if thinking["thinkingBudget"] != tc.want || thinking["includeThoughts"] != true {
				t.Fatalf("thinking budget = %#v, want %d", thinking, tc.want)
			}
		})
	}
	model := googleTestModel("gemini-3.1-pro-preview", "google")
	model.ThinkingLevelMap = map[string]*string{"off": nil, "minimal": nil, "low": completionString("LOW"), "medium": completionString("medium"), "high": completionString("high"), "xhigh": nil, "max": nil}
	body, err := BuildGoogleRequest(Request{Model: model, Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if got := googleGenerationConfig(body)["thinkingConfig"].(map[string]any)["thinkingLevel"]; got != "LOW" {
		t.Fatalf("Pi disabled-thinking fallback = %#v", got)
	}
	body, err = BuildGoogleRequest(Request{Model: model, Thinking: "low"})
	if err != nil || googleGenerationConfig(body)["thinkingConfig"].(map[string]any)["thinkingLevel"] != "LOW" {
		t.Fatalf("case-insensitive thinking map = %#v, %v", body, err)
	}
	body, err = BuildGoogleRequest(Request{Model: model, Thinking: "minimal"})
	if err != nil || googleGenerationConfig(body)["thinkingConfig"].(map[string]any)["thinkingLevel"] != "LOW" {
		t.Fatalf("unsupported Google level did not clamp to the next available level: %#v %v", body, err)
	}
	body, err = BuildGoogleRequest(Request{Model: model, Thinking: "xhigh"})
	if err != nil || googleGenerationConfig(body)["thinkingConfig"].(map[string]any)["thinkingLevel"] != "HIGH" {
		t.Fatalf("unmapped extended Google level did not clamp to high: %#v %v", body, err)
	}
	if got := SupportedThinking(model); strings.Contains(strings.Join(got, ","), "off") || strings.Contains(strings.Join(got, ","), "minimal") || strings.Contains(strings.Join(got, ","), "xhigh") {
		t.Fatalf("unsupported Google thinking levels advertised: %v", got)
	}
	if got := ThinkingDescription(model, "off"); !strings.Contains(got, "fallback level LOW") {
		t.Fatalf("Google off description = %q", got)
	}
	model.ThinkingLevelMap["low"] = completionString("extreme")
	if _, err := BuildGoogleRequest(Request{Model: model, Thinking: "low"}); err == nil || !strings.Contains(err.Error(), "unsupported Google thinking level mapping") {
		t.Fatalf("invalid Google effort mapping accepted: %v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
