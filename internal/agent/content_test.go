package agent

import (
	"context"
	"encoding/base64"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
)

var testImageBytes = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4}

func imageInput(display string) UserInput {
	return UserInput{
		Content: []session.UserContentBlock{
			{Type: "text", Text: &session.TextContent{Type: "text", Text: "look"}},
			{Type: "image", Image: &session.ImageContent{
				Type: "image", MimeType: "image/png",
				Data: base64.StdEncoding.EncodeToString(testImageBytes),
			}},
		},
		Display:    display,
		ImageBytes: uint64(len(testImageBytes)),
	}
}

// The MVP path: one structured submission becomes one durable user message and
// the next provider request carries the same decoded bytes.
func TestSubmitContentPersistsAndSendsImageBytes(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}

	if err := loop.SubmitContent(context.Background(), imageInput("Attached: a.png"), cfg); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	var stored *session.UserMessage
	for _, entry := range path {
		if entry.Kind == "message" && entry.Message.Message.User != nil {
			stored = entry.Message.Message.User
		}
	}
	if stored == nil {
		t.Fatal("no durable user message")
	}
	blocks, ok := stored.Content.([]session.UserContentBlock)
	if !ok || len(blocks) != 2 || blocks[1].Image == nil {
		t.Fatalf("stored content = %#v, want one text and one image block", stored.Content)
	}
	if blocks[1].Image.MimeType != "image/png" || blocks[1].Image.Data != base64.StdEncoding.EncodeToString(testImageBytes) {
		t.Fatalf("stored image = %+v", blocks[1].Image)
	}

	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(requests))
	}
	var image *provider.Block
	for _, message := range requests[0].Messages {
		if message.Role != "user" {
			continue
		}
		for i := range message.Content {
			if message.Content[i].Type == "image" {
				block := message.Content[i]
				image = &block
			}
		}
	}
	if image == nil || image.MimeType != "image/png" || image.Data != base64.StdEncoding.EncodeToString(testImageBytes) {
		t.Fatalf("provider image = %+v, want the exact stored bytes", image)
	}
}

// Text callers keep the plain string shape so existing sessions stay byte
// compatible.
func TestTextSubmitKeepsAPlainStringContent(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", System: "base"}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range path {
		if entry.Kind == "message" && entry.Message.Message.User != nil {
			if text, ok := entry.Message.Message.User.Content.(string); !ok || text != "hello" {
				t.Fatalf("content = %#v, want the plain string", entry.Message.Message.User.Content)
			}
			return
		}
	}
	t.Fatal("no durable user message")
}

// A structured submission with no image or text is refused before any durable
// or billable work.
func TestSubmitContentRejectsEmptyStructuredInput(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	cfg := Config{Model: modelMeta(), Thinking: "off", System: "base"}
	empty := UserInput{Content: []session.UserContentBlock{
		{Type: "text", Text: &session.TextContent{Type: "text", Text: ""}},
	}}
	if err := loop.SubmitContent(context.Background(), empty, cfg); err == nil {
		t.Fatal("empty structured input was accepted")
	}
	if len(fake.Requests()) != 0 {
		t.Fatal("empty input reached the provider")
	}
}

// An append failure stops before the provider and leaves nothing queued.
func TestSubmitContentStopsBeforeProviderWhenStorageFails(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("initial")); err != nil {
		t.Fatal(err)
	}
	appendBrokenSessionTail(t, store.File())
	cfg := Config{Model: modelMeta(), Thinking: "off", System: "base"}
	if err := loop.SubmitContent(context.Background(), imageInput("Attached"), cfg); err == nil {
		t.Fatal("broken storage accepted the submission")
	}
	if len(fake.Requests()) != 0 {
		t.Fatal("broken storage still reached the provider")
	}
}

// Steering keeps images structured, preserves FIFO order against text, and
// accounts bytes until each message is durable.
func TestSteerContentKeepsImagesAndOrder(t *testing.T) {
	_, _, _, loop, _ := testSetup(t)
	loop.Steer("text first")
	loop.SteerContent(imageInput("Attached: a.png"))
	loop.FollowUp("follow-up")

	if got := loop.PendingSteering(); len(got) != 2 || got[0] != "text first" || got[1] != "Attached: a.png" {
		t.Fatalf("pending = %q", got)
	}
	if got := loop.PendingContentBytes(); got != uint64(len(testImageBytes)) {
		t.Fatalf("pending bytes = %d, want %d", got, len(testImageBytes))
	}
	taken := loop.TakeSteering()
	if len(taken) != 2 || !taken[1].Structured() {
		t.Fatalf("taken = %+v, want the image kept structured", taken)
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("bytes after take = %d, want 0", got)
	}
	if got := loop.PendingFollowUp(); len(got) != 1 || got[0] != "follow-up" {
		t.Fatalf("follow-ups = %q", got)
	}
}

// A steered image is delivered before follow-up text and reaches the provider
// request that follows it.
func TestSteeredImageReachesTheNextProviderRequest(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	loop.SteerContent(imageInput("Attached: a.png"))
	if err := loop.Submit(context.Background(), "go", Config{Model: modelMeta(), Thinking: "off", System: "base"}); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider calls = %d", len(requests))
	}
	found := false
	for _, message := range requests[0].Messages {
		for _, block := range message.Content {
			if block.Type == "image" && block.Data == base64.StdEncoding.EncodeToString(testImageBytes) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("steered image never reached the provider request")
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("bytes after delivery = %d, want 0", got)
	}
}

// A clean late-queue arrival drains the structured steering through
// ContinuePending's own run, exactly once.
func TestContinuePendingDeliversStructuredSteering(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	loop.SteerContent(imageInput("Attached: a.png"))
	if err := loop.ContinuePending(context.Background(), Config{Model: modelMeta(), Thinking: "off", System: "base"}); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider calls = %d, want one continued run", len(requests))
	}
	want := base64.StdEncoding.EncodeToString(testImageBytes)
	found := false
	for _, message := range requests[0].Messages {
		for _, block := range message.Content {
			if block.Type == "image" && block.Data == want {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("continued run did not carry the steered image")
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("pending bytes = %d, want 0", got)
	}
	if got := loop.PendingSteering(); len(got) != 0 {
		t.Fatalf("steering still queued: %q", got)
	}
}

// The same broken append that today restores queued text must restore the
// structured input intact, including its byte accounting.
func TestSteeringStructuredAppendFailureRestoresInput(t *testing.T) {
	store, _, _, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("initial")); err != nil {
		t.Fatal(err)
	}
	appendBrokenSessionTail(t, store.File())
	loop.SteerContent(imageInput("Attached: a.png"))
	if delivered, err := loop.deliverQueuedSteering(); delivered || err == nil {
		t.Fatalf("delivery = %v, %v; want a reported storage failure", delivered, err)
	}
	if got := loop.PendingContentBytes(); got != uint64(len(testImageBytes)) {
		t.Fatalf("pending bytes = %d, want the failed input still counted", got)
	}
	taken := loop.TakeSteering()
	if len(taken) != 1 || !taken[0].Structured() {
		t.Fatalf("taken = %+v, want the image kept structured", taken)
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("bytes after take = %d, want 0", got)
	}
}

// Queue modes and image accounting must not drift apart: one-at-a-time keeps
// the undelivered image counted, "all" releases everything, and recovery takes
// the whole batch regardless of the mode.
func TestQueueModesKeepImageBytesAccounted(t *testing.T) {
	_, _, _, loop, _ := testSetup(t)
	loop.SteerContent(imageInput("first"))
	loop.SteerContent(imageInput("second"))

	delivered, err := loop.deliverQueuedSteering()
	if err != nil || !delivered {
		t.Fatalf("delivery = %v, %v", delivered, err)
	}
	if got := loop.PendingContentBytes(); got != uint64(len(testImageBytes)) {
		t.Fatalf("bytes after one-at-a-time delivery = %d, want the remaining image", got)
	}
	if got := loop.PendingSteering(); len(got) != 1 || got[0] != "second" {
		t.Fatalf("pending = %q, want the second message only", got)
	}

	loop.SetQueueModes("all", "")
	delivered, err = loop.deliverQueuedSteering()
	if err != nil || !delivered {
		t.Fatalf("all-mode delivery = %v, %v", delivered, err)
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("bytes after all-mode delivery = %d, want 0", got)
	}

	// Recovery takes the whole queue even in one-at-a-time mode: a resumed
	// session must not strand the rest of the batch.
	loop.SteerContent(imageInput("third"))
	loop.SteerContent(imageInput("fourth"))
	loop.SetQueueModes("one-at-a-time", "one-at-a-time")
	taken := loop.TakeSteering()
	if len(taken) != 2 {
		t.Fatalf("recovered inputs = %d, want the whole batch", len(taken))
	}
	if got := loop.PendingContentBytes(); got != 0 {
		t.Fatalf("bytes after recovery = %d, want 0", got)
	}
}
