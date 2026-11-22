package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hypernewbie/psi/internal/provider"
)

// attachmentTransport serves synthetic attachment payloads by URL.
type attachmentTransport struct {
	mu       sync.Mutex
	sent     []string
	payloads map[string][]byte
	fail     map[string]bool
	fetched  []string
}

func newAttachmentTransport() *attachmentTransport {
	return &attachmentTransport{payloads: map[string][]byte{}, fail: map[string]bool{}}
}

func (a *attachmentTransport) readMessages(context.Context, string, int, string, string) ([]historyMessage, error) {
	return nil, nil
}

func (a *attachmentTransport) sendMessage(_ context.Context, _, _, text string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, text)
	return "sent", nil
}

func (a *attachmentTransport) fetchAttachment(_ context.Context, url string, maxBytes int64) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fetched = append(a.fetched, url)
	if a.fail[url] {
		return nil, fmt.Errorf("download failed")
	}
	data, ok := a.payloads[url]
	if !ok {
		return nil, fmt.Errorf("no payload")
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("payload too large")
	}
	return data, nil
}

func (a *attachmentTransport) fetches() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.fetched...)
}

// startTyping is a no-op for the test transport.
func (a *attachmentTransport) startTyping(context.Context, string) func() {
	return func() {}
}

func (a *attachmentTransport) messages() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

func TestAttachmentBlocksImageReachesVisionModel(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	tr := newAttachmentTransport()
	tr.payloads["https://cdn.discordapp.com/attachments/1/image.png"] = []byte("synthetic-png-bytes")
	factory := &fakeFactory{replies: []string{"seen"}}
	factory.model = provider.ModelMeta{ID: "test-model", API: "openai-completions", Provider: "demo", ContextWindow: 128000, MaxTokens: 4096, Custom: true, InputImage: true}
	rt := testRuntime(t, workspace, state, tr, factory, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	chat.handle(sourceTurn{message: dm, text: "look", replyTo: dm.MessageID, attachments: []inboundAttachment{{
		URL: "https://cdn.discordapp.com/attachments/1/image.png", ContentType: "image/png", Size: 19, Name: "image.png",
	}}})

	calls := factory.last().Calls
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d", len(calls))
	}
	var found bool
	for _, message := range calls[0].Messages {
		for _, block := range message.Content {
			if block.Type == "image" {
				found = true
				want := base64.StdEncoding.EncodeToString([]byte("synthetic-png-bytes"))
				if block.Data != want {
					t.Fatalf("image data = %q", block.Data)
				}
				if block.MimeType != "image/png" {
					t.Fatalf("mime = %q", block.MimeType)
				}
			}
		}
	}
	if !found {
		t.Fatal("no image block reached the provider")
	}
}

func TestAttachmentBlocksSkipsImageForTextOnlyModel(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	tr := newAttachmentTransport()
	tr.payloads["https://cdn.discordapp.com/attachments/1/image.png"] = []byte("synthetic-png-bytes")
	factory := &fakeFactory{replies: []string{"seen"}}
	rt := testRuntime(t, workspace, state, tr, factory, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	chat.handle(sourceTurn{message: dm, text: "", replyTo: dm.MessageID, attachments: []inboundAttachment{{
		URL: "https://cdn.discordapp.com/attachments/1/image.png", ContentType: "image/png", Size: 19, Name: "image.png",
	}}})

	if got := tr.messages(); len(got) != 1 || got[0] != "Attachments are not supported." {
		t.Fatalf("sent = %v", got)
	}
	if fetches := tr.fetches(); len(fetches) != 0 {
		t.Fatalf("downloaded an image the model cannot read: %v", fetches)
	}
}

func TestAttachmentBlocksInlinesTextAsUntrustedData(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	tr := newAttachmentTransport()
	tr.payloads["https://cdn.discordapp.com/attachments/2/notes.txt"] = []byte("attached notes")
	factory := &fakeFactory{replies: []string{"seen"}}
	rt := testRuntime(t, workspace, state, tr, factory, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	chat.handle(sourceTurn{message: dm, text: "read it", replyTo: dm.MessageID, attachments: []inboundAttachment{{
		URL: "https://cdn.discordapp.com/attachments/2/notes.txt", ContentType: "text/plain", Size: 13, Name: "notes.txt",
	}}})

	calls := factory.last().Calls
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d", len(calls))
	}
	var text string
	for _, message := range calls[0].Messages {
		for _, block := range message.Content {
			if block.Type == "text" {
				text += block.Text
			}
		}
	}
	if !strings.Contains(text, "attached notes") {
		t.Fatalf("attachment text missing: %q", text)
	}
	if !strings.Contains(text, "<attachment name=\"notes.txt\">") {
		t.Fatalf("attachment delimiter missing: %q", text)
	}
}

func TestAttachmentBlocksRejectsOversizedWrongTypeAndFailedDownloads(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	tr := newAttachmentTransport()
	tr.payloads["https://cdn.discordapp.com/attachments/3/big.png"] = []byte("oversized")
	tr.payloads["https://cdn.discordapp.com/attachments/4/binary.bin"] = []byte("binary")
	tr.fail["https://cdn.discordapp.com/attachments/5/broken.png"] = true
	factory := &fakeFactory{replies: []string{"seen"}}
	factory.model = provider.ModelMeta{ID: "test-model", API: "openai-completions", Provider: "demo", ContextWindow: 128000, MaxTokens: 4096, Custom: true, InputImage: true}
	rt := testRuntime(t, workspace, state, tr, factory, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	chat.handle(sourceTurn{message: dm, text: "", replyTo: dm.MessageID, attachments: []inboundAttachment{
		// Oversized: the declared size is above the image cap, so it is
		// skipped without a download.
		{URL: "https://cdn.discordapp.com/attachments/3/big.png", ContentType: "image/png", Size: maxImageAttachment + 1, Name: "big.png"},
		// Wrong type: not an image and not text.
		{URL: "https://cdn.discordapp.com/attachments/4/binary.bin", ContentType: "application/octet-stream", Size: 6, Name: "binary.bin"},
		// Failed download.
		{URL: "https://cdn.discordapp.com/attachments/5/broken.png", ContentType: "image/png", Size: 6, Name: "broken.png"},
	}})

	if got := tr.messages(); len(got) != 1 || got[0] != "Attachments are not supported." {
		t.Fatalf("sent = %v", got)
	}
	fetches := tr.fetches()
	if len(fetches) != 1 || fetches[0] != "https://cdn.discordapp.com/attachments/5/broken.png" {
		t.Fatalf("downloads = %v", fetches)
	}
}

func TestAttachmentDownloadWritesNoFile(t *testing.T) {
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	owner := "123456789012345678"
	tr := newAttachmentTransport()
	tr.payloads["https://cdn.discordapp.com/attachments/6/notes.txt"] = []byte("attached notes")
	factory := &fakeFactory{replies: []string{"seen"}}
	rt := testRuntime(t, workspace, state, tr, factory, owner)
	chat := &conversation{key: "dm:one", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.close()

	before := workspaceFileNames(t, workspace)
	dm := inboundMessage{MessageID: "223456789012345678", ChannelID: "323456789012345678", AuthorID: owner, IsDM: true}
	chat.handle(sourceTurn{message: dm, text: "read it", replyTo: dm.MessageID, attachments: []inboundAttachment{{
		URL: "https://cdn.discordapp.com/attachments/6/notes.txt", ContentType: "text/plain", Size: 13, Name: "notes.txt",
	}}})
	after := workspaceFileNames(t, workspace)
	if len(before) != len(after) {
		t.Fatalf("workspace changed: %v -> %v", before, after)
	}
	for _, name := range after {
		if strings.Contains(name, "notes.txt") {
			t.Fatalf("attachment landed in the workspace: %s", name)
		}
	}
}

func workspaceFileNames(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel != "." {
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestAttachmentKindSelection(t *testing.T) {
	cases := []struct {
		contentType string
		wantKind    attachmentKindT
	}{
		{"image/png", attachmentImage},
		{"IMAGE/JPEG; charset=binary", attachmentImage},
		{"text/plain", attachmentText},
		{"application/json", attachmentText},
		{"application/octet-stream", attachmentUnusable},
		{"", attachmentUnusable},
	}
	for _, tc := range cases {
		_, kind := attachmentKind(inboundAttachment{ContentType: tc.contentType})
		if kind != tc.wantKind {
			t.Fatalf("content type %q: kind = %v", tc.contentType, kind)
		}
	}
}

func TestCheckAttachmentHostAllowsOnlyDiscordCDN(t *testing.T) {
	if err := checkAttachmentHost("https://cdn.discordapp.com/attachments/1/a.png"); err != nil {
		t.Fatalf("discord CDN rejected: %v", err)
	}
	if err := checkAttachmentHost("https://media.discordapp.net/attachments/1/a.png"); err != nil {
		t.Fatalf("discord media host rejected: %v", err)
	}
	for _, bad := range []string{
		"https://example.invalid/a.png",
		"http://cdn.discordapp.com/a.png",
		"https://cdn.discordapp.com.evil.invalid/a.png",
		"file:///etc/passwd",
	} {
		if err := checkAttachmentHost(bad); err == nil {
			t.Fatalf("host accepted: %s", bad)
		}
	}
}

func TestSanitizeAttachmentName(t *testing.T) {
	if got := sanitizeAttachmentName("re\u0000port\n.txt"); strings.ContainsAny(got, "\x00\n") {
		t.Fatalf("control characters kept: %q", got)
	}
	if got := sanitizeAttachmentName(strings.Repeat("a", 300)); len(got) > 128 {
		t.Fatalf("name length = %d", len(got))
	}
	if got := sanitizeAttachmentName("   "); got != "attachment" {
		t.Fatalf("blank name = %q", got)
	}
}
