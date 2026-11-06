//go:build !windows

package pibridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pibridgepb "psi/internal/pibridge/pb"
)

func TestAttachmentRequestDispatchCopiesRefs(t *testing.T) {
	c := &Client{}
	msg := &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_AttachmentPromptRequest{
		AttachmentPromptRequest: &pibridgepb.AttachmentPromptRequest{
			RequestId:         "11",
			ExpectedSessionId: "s",
			ExpectedLeafId:    "l",
			ExpectedModel:     &pibridgepb.ModelSnapshot{Provider: "p", Model: "m"},
			Text:              "look",
			Attachments: []*pibridgepb.AttachmentRef{{
				Basename: "att-1.png", SizeBytes: 9, MimeType: "image/png",
				Kind: "image", Sha256: strings.Repeat("a", 64),
			}},
		},
	}}
	out := newOutbox()
	defer out.close()
	req, ok := c.dispatch(msg, context.Background(), out, 4)
	if !ok || req.Kind() != RequestAttachment || req.RequestID() != "11" || req.Generation() != 4 {
		t.Fatalf("dispatch = %v %q gen=%d", ok, req.Kind(), req.Generation())
	}
	expect := req.AttachmentExpectation()
	if expect.Session != "s" || expect.Leaf != "l" || !expect.HasModel ||
		expect.Model.Provider != "p" || expect.Model.Model != "m" || expect.Text != "look" {
		t.Fatalf("identity = %+v", expect)
	}
	if len(expect.Refs) != 1 || expect.Refs[0].Basename != "att-1.png" || expect.Refs[0].SizeBytes != 9 ||
		expect.Refs[0].MIMEType != "image/png" || expect.Refs[0].Kind != "image" {
		t.Fatalf("refs = %+v", expect.Refs)
	}
	// A later host mutation of the protobuf must not change validated refs.
	msg.GetAttachmentPromptRequest().Attachments[0].Basename = "other.png"
	if got := req.AttachmentExpectation().Refs[0].Basename; got != "att-1.png" {
		t.Fatalf("refs aliased the request: %q", got)
	}
	// An absent model pins absence rather than an empty match.
	bare, ok := c.dispatch(&pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_AttachmentPromptRequest{
		AttachmentPromptRequest: &pibridgepb.AttachmentPromptRequest{RequestId: "12"},
	}}, context.Background(), out, 4)
	if !ok || bare.AttachmentExpectation().HasModel {
		t.Fatal("absent model read present")
	}
}

func TestAttachmentAckShapes(t *testing.T) {
	c := &Client{}
	out := newOutbox()
	defer out.close()
	req, _ := c.dispatch(&pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_AttachmentPromptRequest{
		AttachmentPromptRequest: &pibridgepb.AttachmentPromptRequest{RequestId: "13"},
	}}, context.Background(), out, 1)

	if !req.ReplyAttachment(AttachmentDispatched, "ignored") {
		t.Fatal("dispatch ack rejected")
	}
	select {
	case frame := <-out.ch:
		ack := frame.GetAttachmentPromptAck()
		if ack.GetRequestId() != "13" ||
			ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_DISPATCHED ||
			ack.GetReason() != "" {
			t.Fatalf("dispatch ack = %+v", ack)
		}
	default:
		t.Fatal("dispatch ack never queued")
	}

	// Unknown reasons and the reserved "duplicate" become unavailable.
	if !req.ReplyAttachment(AttachmentRefused, "duplicate") {
		t.Fatal("refusal rejected")
	}
	select {
	case frame := <-out.ch:
		ack := frame.GetAttachmentPromptAck()
		if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_REFUSED ||
			ack.GetReason() != "unavailable" {
			t.Fatalf("duplicate refusal = %+v", ack)
		}
	default:
		t.Fatal("refusal never queued")
	}

	if !req.ReplyAttachment(AttachmentUnknown, "") {
		t.Fatal("unknown ack rejected")
	}
	select {
	case frame := <-out.ch:
		ack := frame.GetAttachmentPromptAck()
		if ack.GetOutcome() != pibridgepb.AttachmentPromptOutcome_ATTACHMENT_PROMPT_OUTCOME_UNKNOWN ||
			ack.GetReason() != "uncertain" {
			t.Fatalf("unknown ack = %+v", ack)
		}
	default:
		t.Fatal("unknown ack never queued")
	}
}

func provisionEnv(t *testing.T, root string) {
	t.Helper()
	t.Setenv(envEndpoint, "unix:/tmp/never.sock")
	t.Setenv(envPaneID, "pane")
	t.Setenv(envProcessID, "proc")
	t.Setenv(envToken, "token")
	t.Setenv(envAttachmentRoot, root)
}

func TestAttachmentCapabilityFollowsTheRoot(t *testing.T) {
	dir := t.TempDir()
	provisionEnv(t, dir)
	withRoot := FromEnv(Snapshot{Generation: 1, SessionID: "s"})
	if withRoot == nil || !withRoot.AttachmentsAvailable() {
		t.Fatal("valid root did not offer attachments")
	}
	if got := withRoot.offered; len(got) != len(baseCapabilities)+1 || got[len(got)-1] != capAttachments {
		t.Fatalf("offered = %v, want base capabilities plus attachments", got)
	}
	withRoot.Close()
	if withRoot.AttachmentsAvailable() {
		t.Fatal("closed client still offers attachments")
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	provisionEnv(t, link)
	withLink := FromEnv(Snapshot{Generation: 1, SessionID: "s"})
	if withLink == nil {
		t.Fatal("symlink root rejected the bridge itself")
	}
	defer withLink.Close()
	if withLink.AttachmentsAvailable() {
		t.Fatal("symlink root offered attachments")
	}
	if got := withLink.offered; len(got) != len(baseCapabilities) {
		t.Fatalf("offered = %v, want the base capabilities alone", got)
	}
}

// A full optional outbox refuses the ack without blocking: the request stays
// unanswered, and space frees up for the next answer.
func TestAttachmentAckRefusedWhenOutboxIsFull(t *testing.T) {
	c := &Client{}
	out := newOutbox()
	defer out.close()
	req, _ := c.dispatch(&pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_AttachmentPromptRequest{
		AttachmentPromptRequest: &pibridgepb.AttachmentPromptRequest{RequestId: "21"},
	}}, context.Background(), out, 1)
	for i := 0; i < optionalReplySlots; i++ {
		if !req.ReplyAttachment(AttachmentDispatched, "") {
			t.Fatalf("ack %d rejected before the outbox was full", i)
		}
	}
	if req.ReplyAttachment(AttachmentRefused, "busy") {
		t.Fatal("ack accepted into a full outbox")
	}
	frame := <-out.ch
	out.sent(frame)
	if !req.ReplyAttachment(AttachmentRefused, "busy") {
		t.Fatal("ack still rejected after the outbox drained a slot")
	}
}
