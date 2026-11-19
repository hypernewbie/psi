package mcp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestCatalogSanitizesNonASCIIToolNames(t *testing.T) {
	raw := "工具.read"
	catalog, err := buildCatalog(Server{Name: "srv", Exposure: ExposureDirect}, 1, []*sdk.Tool{sdkTool(raw, "", map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	// 工 and 具 each collapse to one underscore and the dot becomes another, so
	// the name keeps no non-ASCII rune while the route keeps the raw tool name.
	if got, want := catalog[0].Name, "mcp__srv_____read"; got != want {
		t.Fatalf("sanitized name = %q, want %q", got, want)
	}
	if catalog[0].RawName != raw {
		t.Fatalf("raw name lost: %q", catalog[0].RawName)
	}
	for _, r := range catalog[0].Name {
		if !isToolNameRune(r) {
			t.Fatalf("name kept rejected rune %q in %q", r, catalog[0].Name)
		}
	}
}

func TestToolDefSchemaIsSharedNotCopied(t *testing.T) {
	catalog, err := buildCatalog(Server{Name: "s", Exposure: ExposureDirect}, 1, []*sdk.Tool{
		sdkTool("t", "", map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every declaration snapshot calls this once per tool, so a per-call copy
	// would allocate on each snapshot. Identity pins the shared read-only
	// contract; the provider request builders never write to the map.
	first := catalog[0].ToolDefSchema()
	second := catalog[0].ToolDefSchema()
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatal("ToolDefSchema copied the catalog schema")
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

func TestResultPreservesEmbeddedResourceOmissionNotices(t *testing.T) {
	for _, test := range []struct {
		name     string
		resource *sdk.ResourceContents
		want     string
	}{
		{name: "unsupported-image", resource: &sdk.ResourceContents{MIMEType: "image/bmp", Blob: []byte("image")}, want: "embedded image omitted"},
		{name: "oversize-image", resource: &sdk.ResourceContents{MIMEType: "image/png", Blob: make([]byte, maxResultImage+1)}, want: "embedded image omitted"},
		{name: "invalid-text", resource: &sdk.ResourceContents{MIMEType: "text/plain", Blob: []byte{0xff}}, want: "not valid UTF-8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, withSibling := range []bool{false, true} {
				content := []sdk.Content{&sdk.EmbeddedResource{Resource: test.resource}}
				if withSibling {
					content = append(content, &sdk.TextContent{Text: "sibling"})
				}
				result := Convert(&sdk.CallToolResult{Content: content, IsError: true}, ConvertOptions{Server: "fixture", Tool: "resource"})
				if !result.IsError || len(result.Content) != len(content) || !strings.Contains(result.Content[0].Text, test.want) {
					t.Fatalf("sibling=%t: omission notice missing or error flag changed: %+v", withSibling, result)
				}
				if withSibling && result.Content[1].Text != "sibling" {
					t.Fatalf("sibling content changed: %+v", result)
				}
			}
		})
	}
}

func TestResultTruncatesAndSavesReadableFullOutput(t *testing.T) {
	// The product writes to os.TempDir(), which ignores TMPDIR outside Unix-like
	// systems. Assert the parent os.TempDir() reports instead of redirecting the
	// file into t.TempDir(), and delete the shared-root file explicitly.
	dir := t.TempDir()
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
	if parent, want := filepath.Dir(details.FullOutputPath), filepath.Clean(os.TempDir()); parent != want {
		t.Errorf("full output parent = %s, want the OS temporary directory %s", parent, want)
	}
	t.Cleanup(func() { _ = os.Remove(details.FullOutputPath) })
	data, err := os.ReadFile(details.FullOutputPath)
	if err != nil || string(data) != full {
		t.Errorf("full output mismatch: err=%v, len=%d", err, len(data))
	}
	registry := tools.NewRegistry(dir)
	read := registry.Dispatch(tools.Invocation{Name: tools.Read, Arguments: map[string]any{"path": details.FullOutputPath, "limit": 2000}})
	if read.IsError || len(read.Content) != 1 || read.Content[0].Text != full {
		t.Fatalf("read tool could not reach the complete saved output: %+v", read)
	}
}

func TestResultStructuredContent(t *testing.T) {
	result := &sdk.CallToolResult{StructuredContent: map[string]any{"ok": true}}
	converted := Convert(result, ConvertOptions{Server: "s", Tool: "t"})
	if len(converted.Content) != 1 || !strings.Contains(converted.Content[0].Text, `"ok":true`) {
		t.Errorf("structured content = %+v", converted.Content)
	}
	if converted.Details != (toolDetails{Server: "s", Tool: "t"}) {
		t.Errorf("details = %+v", converted.Details)
	}
}

func TestResultFormatsResourceLinkAsText(t *testing.T) {
	result := &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.ResourceLink{URI: "http://127.0.0.1:1/never", Name: "example"},
	}}
	converted := Convert(result, ConvertOptions{Server: "s", Tool: "t"})
	if len(converted.Content) != 1 || !strings.Contains(converted.Content[0].Text, "example") {
		t.Errorf("resource link = %+v", converted.Content)
	}
}

func TestEmbeddedBinaryResourceFileIsPrivate(t *testing.T) {
	block, notice, err := convertResource(&sdk.ResourceContents{
		URI:      "file:///tmp/example.bin",
		MIMEType: "application/octet-stream",
		Blob:     []byte("resource-bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if block == nil || block.Text != notice {
		t.Fatalf("resource block = %+v notice=%q", block, notice)
	}
	path := strings.TrimPrefix(notice, "MCP binary resource saved to ")
	if path == notice || path == "" {
		t.Fatalf("resource path notice = %q", notice)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for every regular file and maps chmod to the
	// read-only attribute, so POSIX permission bits carry no meaning there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("resource mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "resource-bytes" {
		t.Fatalf("resource bytes = %q", data)
	}
}
