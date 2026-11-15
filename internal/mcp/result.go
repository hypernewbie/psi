package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"psi/internal/tools"
)

const (
	maxResultText  = 20 << 10
	maxResultImage = 3 << 20
	maxNoticeBytes = 1 << 10
)

// ConvertOptions carries the safe identity attached to a result.
type ConvertOptions struct {
	Server        string
	Tool          string
	ReadAvailable bool
}

type toolDetails struct {
	Server         string `json:"server"`
	Tool           string `json:"tool"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

// Convert maps one MCP result to psi's tool result without changing provider
// or session types. Images that the existing provider paths cannot persist
// become bounded text notices instead.
func Convert(result *sdk.CallToolResult, opts ConvertOptions) tools.Result {
	if result == nil {
		return textError("MCP tool returned an empty result")
	}
	if result.NeedsInput() {
		return textError(fmt.Sprintf("MCP tool %s/%s requires interactive input, which psi does not support", opts.Server, opts.Tool))
	}
	out := tools.Result{IsError: result.IsError}
	var textParts []string
	var content []tools.Block
	for _, block := range result.Content {
		switch item := block.(type) {
		case *sdk.TextContent:
			textParts = append(textParts, item.Text)
			content = append(content, tools.Block{Type: "text", Text: item.Text})
		case *sdk.ImageContent:
			if converted, ok := imageBlock(item.Data, item.MIMEType); ok {
				content = append(content, converted)
			} else {
				notice := fmt.Sprintf("MCP image omitted: unsupported MIME type %q or image larger than 3 MiB", safeMIME(item.MIMEType))
				textParts = append(textParts, notice)
				content = append(content, tools.Block{Type: "text", Text: notice})
			}
		case *sdk.EmbeddedResource:
			block, notice, err := convertResource(item.Resource)
			if notice != "" {
				textParts = append(textParts, notice)
			}
			if err != nil {
				notice := "MCP embedded resource could not be converted: " + err.Error()
				textParts = append(textParts, notice)
				content = append(content, tools.Block{Type: "text", Text: notice})
				continue
			}
			if block != nil {
				content = append(content, *block)
				if block.Type == "text" && notice == "" {
					textParts = append(textParts, block.Text)
				}
			}
		case *sdk.ResourceLink:
			notice := resourceLinkText(item)
			textParts = append(textParts, notice)
			content = append(content, tools.Block{Type: "text", Text: notice})
		case *sdk.AudioContent:
			notice := "MCP audio content is not supported"
			textParts = append(textParts, notice)
			content = append(content, tools.Block{Type: "text", Text: notice})
		default:
			notice := "MCP returned unsupported content"
			textParts = append(textParts, notice)
			content = append(content, tools.Block{Type: "text", Text: notice})
		}
	}
	if len(content) == 0 && result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return textError("MCP structured result could not be encoded")
		}
		textParts = append(textParts, string(data))
		content = append(content, tools.Block{Type: "text", Text: string(data)})
	}
	if len(content) == 0 {
		if result.IsError {
			text := fmt.Sprintf("MCP tool %s/%s returned an error", opts.Server, opts.Tool)
			textParts = append(textParts, text)
			content = append(content, tools.Block{Type: "text", Text: text})
		} else {
			content = append(content, tools.Block{Type: "text", Text: "(no output)"})
		}
	}
	combined := strings.Join(textParts, "\n")
	details := toolDetails{Server: opts.Server, Tool: opts.Tool}
	var noticeBlocks []tools.Block
	if len(combined) > maxResultText {
		fullPath, pathErr := writeFullOutput(combined)
		truncated := truncateMiddle(combined, maxResultText)
		var kept []tools.Block
		for _, block := range content {
			if block.Type == "image" {
				kept = append(kept, block)
			}
		}
		content = append([]tools.Block{{Type: "text", Text: truncated}}, kept...)
		if pathErr == nil {
			details.FullOutputPath = fullPath
			noticeBlocks = append(noticeBlocks, tools.Block{Type: "text", Text: fullOutputNotice(fullPath, opts.ReadAvailable)})
		} else {
			noticeBlocks = append(noticeBlocks, tools.Block{Type: "text", Text: "MCP output was truncated and the full output could not be saved."})
		}
	}
	for _, block := range noticeBlocks {
		content = append(content, block)
	}
	out.Content = content
	out.Details = details
	return out
}

func textError(text string) tools.Result {
	return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: text}}}
}

func safeMIME(mime string) string {
	if len(mime) > 64 {
		return mime[:64]
	}
	return mime
}

func imageBlock(data []byte, mime string) (tools.Block, bool) {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return tools.Block{}, false
	}
	if len(data) == 0 || len(data) > maxResultImage {
		return tools.Block{}, false
	}
	return tools.Block{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: mime}, true
}

func convertResource(resource *sdk.ResourceContents) (*tools.Block, string, error) {
	if resource == nil {
		return nil, "", fmt.Errorf("missing resource")
	}
	if resource.Text != "" {
		block := &tools.Block{Type: "text", Text: resource.Text}
		return block, "", nil
	}
	if len(resource.Blob) == 0 {
		return nil, "", fmt.Errorf("empty resource")
	}
	mime := strings.ToLower(strings.TrimSpace(resource.MIMEType))
	if strings.HasPrefix(mime, "image/") {
		if block, ok := imageBlock(resource.Blob, mime); ok {
			return &block, "", nil
		}
		return nil, "MCP embedded image omitted: unsupported MIME type or image larger than 3 MiB", nil
	}
	if isTextMIME(mime) {
		if !utf8.Valid(resource.Blob) {
			return nil, "MCP text resource was not valid UTF-8 and was omitted", nil
		}
		text := string(resource.Blob)
		return &tools.Block{Type: "text", Text: text}, "", nil
	}
	path, err := writeResourceFile(resource.Blob, mime)
	if err != nil {
		return nil, "", err
	}
	notice := "MCP binary resource saved to " + path
	return &tools.Block{Type: "text", Text: notice}, notice, nil
}

func isTextMIME(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/xhtml+xml":
		return true
	}
	return strings.HasSuffix(mime, "+json") || strings.HasSuffix(mime, "+xml")
}

func resourceLinkText(link *sdk.ResourceLink) string {
	name := strings.TrimSpace(link.Name)
	if name == "" {
		name = strings.TrimSpace(link.Title)
	}
	if name == "" {
		name = "resource"
	}
	uri := link.URI
	if len(uri) > 512 {
		uri = uri[:512]
	}
	text := fmt.Sprintf("MCP resource link: %s (%s)", name, uri)
	return boundNotice(text)
}

func boundNotice(text string) string {
	if len(text) <= maxNoticeBytes {
		return text
	}
	return text[:maxNoticeBytes-3] + "..."
}

func truncateMiddle(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	marker := fmt.Sprintf("\n\n[... output truncated: %d bytes omitted ...]\n\n", len(text)-limit)
	if len(marker) >= limit/2 {
		marker = "\n\n[... truncated ...]\n\n"
	}
	budget := limit - len(marker)
	startBudget := budget / 2
	endBudget := budget - startBudget
	start := text[:startBudget]
	for len(start) > 0 && !utf8.ValidString(start) {
		start = start[:len(start)-1]
	}
	end := text[len(text)-endBudget:]
	for len(end) > 0 && !utf8.ValidString(end) {
		end = end[1:]
	}
	return start + marker + end
}

func writeFullOutput(text string) (string, error) {
	f, err := os.CreateTemp("", "psi-mcp-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func writeResourceFile(data []byte, mime string) (string, error) {
	ext := ".bin"
	switch mime {
	case "application/pdf":
		ext = ".pdf"
	case "application/zip":
		ext = ".zip"
	case "application/octet-stream":
		ext = ".bin"
	}
	f, err := os.CreateTemp("", "psi-mcp-resource-*"+ext)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func fullOutputNotice(path string, readAvailable bool) string {
	if readAvailable {
		return boundNotice(fmt.Sprintf("Full output saved to %s. Read it with offset/limit to view the complete text.", path))
	}
	return boundNotice(fmt.Sprintf("Full output saved to %s, but the read tool is not enabled in this session.", path))
}
