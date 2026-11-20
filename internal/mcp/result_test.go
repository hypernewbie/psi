package mcp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hypernewbie/psi/internal/tools"
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
		&sdk.ImageContent{Data: image, MIMEType: "image/png; profile=example"},
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

func TestResultExplainsImageOnlyError(t *testing.T) {
	result := Convert(&sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.ImageContent{Data: []byte("image"), MIMEType: "image/png"}},
	}, ConvertOptions{Server: "s", Tool: "t"})
	if !result.IsError || len(result.Content) != 2 || result.Content[0].Type != "image" ||
		result.Content[1].Type != "text" || result.Content[1].Text != "MCP tool s/t returned an error" {
		t.Fatalf("image-only error = %+v", result)
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
	registry := tools.NewRegistry(dir)
	read := registry.Dispatch(tools.Invocation{Name: tools.Read, Arguments: map[string]any{"path": details.FullOutputPath, "limit": 2000}})
	if read.IsError || len(read.Content) != 1 || read.Content[0].Text != full {
		t.Fatalf("read tool could not reach the complete saved output: %+v", read)
	}
}

func TestResultReportsUnsavedTruncatedOutput(t *testing.T) {
	// Use a nonexistent temp directory so creation fails even with elevated
	// permissions. Go uses TMPDIR on Unix and TMP or TEMP on Windows.
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)
	full := "start:" + strings.Repeat("界", maxResultText) + ":end"
	result := Convert(&sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: full}}}, ConvertOptions{ReadAvailable: true})
	if result.IsError || len(result.Content) != 2 || result.Content[0].Type != "text" || result.Content[1].Type != "text" {
		t.Fatalf("expected truncated text and a notice, got %d blocks (tool error: %t)", len(result.Content), result.IsError)
	}
	preview := result.Content[0].Text
	if len(preview) > maxResultText || !utf8.ValidString(preview) ||
		!strings.HasPrefix(preview, "start:") || !strings.HasSuffix(preview, ":end") ||
		!strings.Contains(preview, "[... output truncated ...]") {
		t.Fatal("preview must preserve both ends and valid UTF-8 within the text limit, with a truncation marker")
	}
	if details, ok := result.Details.(toolDetails); !ok || details.FullOutputPath != "" {
		t.Fatalf("failed save published unexpected details: %+v", result.Details)
	}
	if notice := result.Content[1].Text; notice != "MCP output was truncated and the full output could not be saved." {
		t.Fatalf("failed save notice = %q", notice)
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
	const uriLimit = 512
	const linkPrefix = "MCP resource link: "
	// Put a multi-byte rune across each byte limit.
	longURI := "https://example.test/" + strings.Repeat("x", uriLimit-1-len("https://example.test/")) + "界"
	longName := strings.Repeat("x", maxNoticeBytes-len("...")-1-len(linkPrefix)) + "界"
	result := Convert(&sdk.CallToolResult{Content: []sdk.Content{
		&sdk.ResourceLink{URI: longURI, Name: "example"},
		&sdk.ResourceLink{URI: "https://example.test/", Name: longName},
	}}, ConvertOptions{})
	if len(result.Content) != 2 {
		t.Fatalf("resource link blocks = %d, want 2", len(result.Content))
	}
	for _, block := range result.Content {
		if block.Type != "text" || !utf8.ValidString(block.Text) || len(block.Text) > maxNoticeBytes {
			t.Fatalf("resource link should be bounded UTF-8 text: %q", block.Text)
		}
	}
	if want := linkPrefix + "example (" + strings.TrimSuffix(longURI, "界") + ")"; result.Content[0].Text != want {
		t.Errorf("URI was not clipped at a rune boundary: %q", result.Content[0].Text)
	}
	if !strings.HasSuffix(result.Content[1].Text, "...") {
		t.Errorf("missing ellipsis on clipped notice: %q", result.Content[1].Text)
	}
}

func TestResultConvertsParameterizedJSONResourceToText(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	const payload = `{"city":"東京"}`
	result := Convert(&sdk.CallToolResult{Content: []sdk.Content{&sdk.EmbeddedResource{
		Resource: &sdk.ResourceContents{
			MIMEType: `application/example+json; charset="utf-8"`,
			Blob:     []byte(payload),
		},
	}}}, ConvertOptions{})
	if len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != payload {
		t.Fatalf("JSON resource should be returned as text: %+v", result.Content)
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
