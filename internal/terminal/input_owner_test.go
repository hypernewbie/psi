package terminal

import (
	"bytes"
	"context"
	"testing"
)

func TestInputReleaseWipesOwnedPayloadOnly(t *testing.T) {
	backing := bytes.Repeat([]byte("S"), 32)
	key := backing[4:8]
	owned := Input{Key: key, owned: true}
	owned.Release()
	for i, b := range backing {
		if i >= 4 && b != 0 {
			t.Fatalf("owned key byte %d survived release: %q", i, backing)
		}
		if i < 4 && b != 'S' {
			t.Fatalf("adjacent sentinel %d changed: %q", i, backing)
		}
	}
	owned.Release() // repeated release is harmless

	borrowedBacking := []byte("borrowed")
	borrowedReply := []byte("borrowed-reply")
	borrowed := Input{Key: borrowedBacking, Reply: borrowedReply}
	borrowed.Release()
	if !bytes.Equal(borrowedBacking, []byte("borrowed")) || !bytes.Equal(borrowedReply, []byte("borrowed-reply")) {
		t.Fatal("borrowed input was wiped")
	}
}

func TestSendDecodedInputsAbortsUnsentPayloads(t *testing.T) {
	for _, reply := range []bool{false, true} {
		name := "key"
		if reply {
			name = "reply"
		}
		t.Run(name, func(t *testing.T) {
			first := []byte("transferred")
			second := []byte("unsent-key")
			third := []byte("unsent-reply")
			units := []Decoded{{Key: first, Reply: reply}, {Key: second}, {Key: third, Reply: true}}
			out := make(chan Input)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan bool, 1)
			go func() { finished <- sendDecodedInputs(ctx, out, units) }()

			received := <-out
			payload := received.Key
			if reply {
				payload = received.Reply
			}
			if !received.owned || string(payload) != "transferred" {
				t.Fatal("reader did not transfer the decoded payload")
			}
			cancel()
			if ok := <-finished; ok {
				t.Fatal("aborted batch reported success")
			}
			if string(payload) != "transferred" {
				t.Fatal("aborting the batch wiped a transferred payload")
			}
			for _, unsent := range [][]byte{second, third} {
				if !bytes.Equal(unsent, make([]byte, len(unsent))) {
					t.Fatal("aborted batch retained an unsent payload")
				}
			}
			received.Release()
			if !bytes.Equal(first, make([]byte, len(first))) {
				t.Fatal("receiver did not wipe its transferred payload")
			}
		})
	}
}

func TestDecoderResetClearsPendingAndPasteStorage(t *testing.T) {
	d := NewDecoder()
	d.pending = []byte("pending-secret")
	d.pasteBuf = []byte("paste-secret")
	pending := d.pending[:cap(d.pending)]
	paste := d.pasteBuf[:cap(d.pasteBuf)]
	d.reset()
	if len(d.pending) != 0 || len(d.pasteBuf) != 0 {
		t.Fatal("reset retained decoder buffers")
	}
	for i, b := range pending {
		if b != 0 {
			t.Fatalf("pending byte %d survived reset", i)
		}
	}
	for i, b := range paste {
		if b != 0 {
			t.Fatalf("paste byte %d survived reset", i)
		}
	}
}

func TestAppendOwnedBytesWipesReplacedAllocation(t *testing.T) {
	old := make([]byte, 3, 4)
	copy(old, "abc")
	alias := old[:cap(old)]
	got := appendOwnedBytes(old, []byte("def"))
	if string(got) != "abcdef" {
		t.Fatalf("grown bytes = %q", got)
	}
	for i, b := range alias {
		if b != 0 {
			t.Fatalf("old byte %d survived growth", i)
		}
	}
}

func TestConsoleCloseWipesHeldAndSecretState(t *testing.T) {
	var out bytes.Buffer
	c := &Console{Editor: NewEditor(), Out: &out, writer: NewWriter(&out), decoder: NewDecoder()}
	c.secret = newSecretEditor()
	if !c.secret.insert([]byte("close-secret")) {
		t.Fatal("secret insert rejected")
	}
	secretAlias := c.secret.buf[:cap(c.secret.buf)]
	heldAlias := []byte("held-secret")
	c.heldInputs = append(c.heldInputs, Input{Key: heldAlias, owned: true})
	c.Close()
	if c.secret != nil || len(c.heldInputs) != 0 {
		t.Fatal("close retained secret or held input state")
	}
	for i, b := range secretAlias {
		if b != 0 {
			t.Fatalf("secret byte %d survived close", i)
		}
	}
	for i, b := range heldAlias {
		if b != 0 {
			t.Fatalf("held byte %d survived close", i)
		}
	}
}
