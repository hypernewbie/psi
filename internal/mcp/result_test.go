package mcp

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"psi/internal/tools"
)

func sdkTool(name, description string, schema any) *sdk.Tool {
	return &sdk.Tool{Name: name, Description: description, InputSchema: schema}
}

func TestCatalogNamingSchemaAndExposure(t *testing.T) {
	server := Server{Name: "a-b", Exposure: ExposureCodemode, ToolRules: []ExposureRule{{Pattern: "hidden_*", Exposure: ExposureHidden}}}
	tools, err := buildCatalog(server, 7, []*sdk.Tool{
		sdkTool("dev.tools", "", map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string", "default": "y"}}, "required": []any{"x"}}),
		sdkTool("dev_tools", "explicit", map[string]any{"$ref": "#/$defs/x"}),
		sdkTool("hidden_one", "hidden", map[string]any{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("tools = %d", len(tools))
	}
	if tools[0].Name == "mcp__a_b__dev_tools" {
		t.Errorf("collision member missing its suffix: %q", tools[0].Name)
	}
	if tools[1].Name == "mcp__a_b__dev_tools" {
		t.Errorf("collision member missing its suffix: %q", tools[1].Name)
	}
	if tools[2].Exposure != ExposureHidden {
		t.Errorf("hidden exposure = %q", tools[2].Exposure)
	}
	if tools[0].Description == "" {
		t.Error("description fallback missing")
	}
	schema := tools[0].Schema
	if schema["type"] != "object" || schema["properties"] == nil {
		t.Errorf("schema defaults = %#v", schema)
	}
	if _, ok := schema["required"]; !ok {
		t.Errorf("schema keyword lost: %#v", schema)
	}
	ref := tools[1].Schema
	if ref["type"] != "object" || ref["properties"] == nil || ref["$ref"] != "#/$defs/x" {
		t.Errorf("ref schema changed: %#v", ref)
	}
}

func TestCatalogLongNameAndOrderStability(t *testing.T) {
	server := Server{Name: "filesystem", Exposure: ExposureDirect}
	long := strings.Repeat("x", 80)
	first, err := buildCatalog(server, 1, []*sdk.Tool{
		sdkTool(long, "", map[string]any{}),
		sdkTool("collide.tool", "", map[string]any{}),
		sdkTool("collide_tool", "", map[string]any{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildCatalog(server, 1, []*sdk.Tool{
		sdkTool("collide_tool", "", map[string]any{}),
		sdkTool("collide.tool", "", map[string]any{}),
		sdkTool(long, "", map[string]any{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, tool := range first {
		if len(tool.Name) > 64 {
			t.Errorf("name too long: %d", len(tool.Name))
		}
		names[tool.RawName] = tool.Name
	}
	for _, tool := range second {
		if names[tool.RawName] != tool.Name {
			t.Errorf("list reorder changed %q: %q -> %q", tool.RawName, names[tool.RawName], tool.Name)
		}
	}
}

func TestCatalogRejectsBadSchema(t *testing.T) {
	server := Server{Name: "s", Exposure: ExposureDirect}
	_, err := buildCatalog(server, 1, []*sdk.Tool{sdkTool("bad", "", "not an object")})
	if err == nil {
		t.Fatal("non-object schema accepted")
	}
}

func TestResultTextImageAndError(t *testing.T) {
	image := []byte("image-bytes")
	result := &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.TextContent{Text: "hello"},
		&sdk.ImageContent{Data: image, MIMEType: "image/png"},
	}, IsError: true}
	converted := Convert(result, ConvertOptions{Server: "s", Tool: "t", ReadAvailable: true})
	if !converted.IsError {
		t.Error("isError lost")
	}
	if len(converted.Content) != 2 {
		t.Fatalf("blocks = %d", len(converted.Content))
	}
	if converted.Content[0].Text != "hello" || converted.Content[1].MimeType != "image/png" {
		t.Errorf("content = %+v", converted.Content)
	}
	if converted.Content[1].Data != base64.StdEncoding.EncodeToString(image) {
		t.Errorf("image data not base64 encoded")
	}
}

func TestResultRejectsUnsupportedImages(t *testing.T) {
	result := &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.ImageContent{Data: []byte("x"), MIMEType: "image/bmp"},
		&sdk.TextContent{Text: "sibling"},
		&sdk.ImageContent{Data: make([]byte, maxResultImage+1), MIMEType: "image/png"},
	}}
	converted := Convert(result, ConvertOptions{Server: "s", Tool: "t", ReadAvailable: true})
	for _, block := range converted.Content {
		if block.Type == "image" {
			t.Errorf("unsupported image persisted: %+v", block)
		}
	}
	if len(converted.Content) < 3 {
		t.Errorf("sibling text or notices missing: %+v", converted.Content)
	}
}

func TestResultTruncatesAndSavesFullOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	full := strings.Repeat("start-", 5000) + "middle" + strings.Repeat("-end", 5000)
	result := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: full}, &sdk.ImageContent{Data: []byte("img"), MIMEType: "image/webp"}}}
	converted := Convert(result, ConvertOptions{Server: "s", Tool: "t", ReadAvailable: true})
	if len(converted.Content) < 2 {
		t.Fatalf("blocks = %d", len(converted.Content))
	}
	text := converted.Content[0].Text
	if len(text) > maxResultText {
		t.Errorf("truncated text length = %d", len(text))
	}
	if !strings.HasPrefix(text, "start-") || !strings.HasSuffix(text, "-end") {
		t.Errorf("truncation did not preserve both ends")
	}
	if converted.Content[1].Type != "image" {
		t.Errorf("image was not retained after text: %+v", converted.Content)
	}
	details, ok := converted.Details.(toolDetails)
	if !ok || details.FullOutputPath == "" {
		t.Fatalf("missing full output path: %+v", converted.Details)
	}
	if !strings.HasPrefix(details.FullOutputPath, dir) {
		t.Errorf("full output escaped the owned temporary parent: %s", details.FullOutputPath)
	}
	data, err := os.ReadFile(details.FullOutputPath)
	if err != nil || string(data) != full {
		t.Errorf("full output mismatch: err=%v, len=%d", err, len(data))
	}
}

func TestResultStructuredAndNeedsInput(t *testing.T) {
	result := &sdk.CallToolResult{StructuredContent: map[string]any{"ok": true}}
	converted := Convert(result, ConvertOptions{Server: "s", Tool: "t"})
	if len(converted.Content) != 1 || !strings.Contains(converted.Content[0].Text, `"ok":true`) {
		t.Errorf("structured content = %+v", converted.Content)
	}
	if converted.Details != (toolDetails{Server: "s", Tool: "t"}) {
		t.Errorf("details = %+v", converted.Details)
	}
}

func TestResultResourceLinkDoesNotFetch(t *testing.T) {
	result := &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.ResourceLink{URI: "http://127.0.0.1:1/never", Name: "example"},
	}}
	converted := Convert(result, ConvertOptions{Server: "s", Tool: "t"})
	if len(converted.Content) != 1 || !strings.Contains(converted.Content[0].Text, "example") {
		t.Errorf("resource link = %+v", converted.Content)
	}
}

func TestResultFullOutputIsReadableByReadTool(t *testing.T) {
	full := strings.Repeat("alpha ", 6000) + "omega"
	converted := Convert(&sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: full}}}, ConvertOptions{Server: "s", Tool: "t", ReadAvailable: true})
	details := converted.Details.(toolDetails)
	registry := tools.NewRegistry(t.TempDir())
	result := registry.Dispatch(tools.Invocation{Name: tools.Read, Arguments: map[string]any{"path": details.FullOutputPath, "limit": 2000}})
	if result.IsError {
		t.Fatalf("read full output: %+v", result)
	}
	text := result.Content[0].Text
	if !strings.HasPrefix(text, "alpha") || !strings.Contains(text, "omega") {
		t.Fatalf("read tool could not reach the saved output: %q", text)
	}
}
