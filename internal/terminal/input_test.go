package terminal

import (
	"bytes"
	"math/rand"
	"testing"
	"time"

	"psi/internal/testutil/leakcheck"
)

func TestInputDecoderTimeoutAndEscapes(t *testing.T) {
	defer leakcheck.Verify(t)

	now := time.Now()
	clock := func() time.Time { return now }

	// Case 1: ESC alone timed out -> Decoded{Key: [27], Timeout: true}
	d := NewDecoder()
	d.SetClock(clock)

	out := d.Feed([]byte{27})
	if len(out) != 0 {
		t.Fatalf("expected 0 decoded immediately for ESC, got %v", out)
	}

	// 20ms later: still pending
	now = now.Add(20 * time.Millisecond)
	out = d.Feed(nil)
	if len(out) != 0 {
		t.Fatalf("expected 0 decoded at 20ms, got %v", out)
	}

	// 60ms later: timed out
	now = now.Add(40 * time.Millisecond)
	out = d.Feed(nil)
	if len(out) != 1 || !out[0].Timeout || len(out[0].Key) != 1 || out[0].Key[0] != 27 {
		t.Fatalf("expected lone ESC with Timeout=true, got %+v", out)
	}

	// Case 2: Alt+Key (ESC followed by key within timeout)
	d = NewDecoder()
	d.SetClock(clock)
	out = d.Feed([]byte("\x1ba"))
	if len(out) != 1 || out[0].Timeout || string(out[0].Key) != "\x1ba" {
		t.Fatalf("expected Alt+a, got %+v", out)
	}

	// Case 3: CSI sequence (ESC [ A) split across feed calls
	d = NewDecoder()
	d.SetClock(clock)
	out = d.Feed([]byte("\x1b["))
	if len(out) != 0 {
		t.Fatalf("expected 0 decoded for partial CSI, got %v", out)
	}
	out = d.Feed([]byte("A"))
	if len(out) != 1 || string(out[0].Key) != "\x1b[A" {
		t.Fatalf("expected Up arrow CSI, got %+v", out)
	}

	// Case 4: Incomplete CSI sequence timed out -> splits into raw bytes
	d = NewDecoder()
	d.SetClock(clock)
	d.Feed([]byte("\x1b["))
	now = now.Add(60 * time.Millisecond)
	out = d.Feed(nil)
	if len(out) != 2 || out[0].Key[0] != 27 || out[1].Key[0] != '[' {
		t.Fatalf("expected split raw bytes on CSI timeout, got %+v", out)
	}

	// Case 5: Incomplete OSC sequence timed out -> discarded without leaking
	d = NewDecoder()
	d.SetClock(clock)
	d.Feed([]byte("\x1b]52;clipboard"))
	now = now.Add(60 * time.Millisecond)
	out = d.Feed(nil)
	if len(out) != 0 {
		t.Fatalf("expected OSC timeout to discard without emitting, got %+v", out)
	}
	// Terminating byte for discarded OSC
	out = d.Feed([]byte{0x07})
	if len(out) != 0 {
		t.Fatalf("expected discardOSC terminator swallowed, got %+v", out)
	}
	// Normal input resumes
	out = d.Feed([]byte("z"))
	if len(out) != 1 || string(out[0].Key) != "z" {
		t.Fatalf("expected normal input after discarded OSC, got %+v", out)
	}
}

func TestInputDecoderEmitPendingWithPaste(t *testing.T) {
	defer leakcheck.Verify(t)

	// Test emitPending directly with complete units and incomplete trailing escape
	dDirect := NewDecoder()
	now := time.Now()
	dDirect.SetClock(func() time.Time { return now })
	dDirect.pending = []byte("abc\x1b")
	outDirect := dDirect.emitPending()
	if len(outDirect) != 3 || string(outDirect[0].Key) != "a" || string(outDirect[1].Key) != "b" || string(outDirect[2].Key) != "c" {
		t.Fatalf("emitPending units mismatch: %+v", outDirect)
	}
	if string(dDirect.pending) != "\x1b" {
		t.Fatalf("emitPending incomplete pending mismatch: %q", dDirect.pending)
	}
	if dDirect.pendingAt != now {
		t.Fatalf("pendingAt not recorded on incomplete pending: %v", dDirect.pendingAt)
	}

	// Now clear pending and verify emitPending resets pendingAt
	dDirect.pending = []byte("x")
	outDirect = dDirect.emitPending()
	if len(outDirect) != 1 || string(outDirect[0].Key) != "x" || len(dDirect.pending) != 0 || !dDirect.pendingAt.IsZero() {
		t.Fatalf("emitPending reset failed: %+v pending=%q pendingAt=%v", outDirect, dDirect.pending, dDirect.pendingAt)
	}

	// Feed with incomplete sequence right before paste marker
	d := NewDecoder()
	d.SetClock(func() time.Time { return now })
	out := d.Feed([]byte("a\x1ba\x1b[200~pasted\x1b[201~"))
	var keys []string
	hasStart, hasEnd := false, false
	for _, dec := range out {
		if dec.PasteStart {
			hasStart = true
		} else if dec.PasteEnd {
			hasEnd = true
		} else if dec.Paste {
			keys = append(keys, "paste:"+string(dec.Key))
		} else {
			keys = append(keys, string(dec.Key))
		}
	}

	if !hasStart || !hasEnd {
		t.Fatalf("missing paste markers: start=%v end=%v", hasStart, hasEnd)
	}
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "\x1ba" || keys[2] != "paste:pasted" {
		t.Fatalf("unexpected decoded units: %v", keys)
	}
}

func TestInputDecoderByteChunkingInvariance(t *testing.T) {
	defer leakcheck.Verify(t)

	// Stream with diverse input: ASCII, UTF-8, CSI, SS3, Alt+key, replies, paste
	var stream bytes.Buffer
	stream.WriteString("Hello World 123! ")
	stream.WriteString("日本語 and emoji: 🌍 🔥 👩‍❤️‍💋‍👩 ")
	stream.WriteString("\x1b[A\x1b[B\x1b[C\x1b[D")                                // Arrows
	stream.WriteString("\x1b[H\x1b[F\x1b[1;5H\x1b[1;5F\x1b[3~")                   // Home/End/Ctrl+Home/Ctrl+End/Del
	stream.WriteString("\x1bOA\x1bOB\x1bOC\x1bOD")                                // SS3
	stream.WriteString("\x1ba\x1bb\x1bc")                                         // Alt keys
	stream.WriteString("\x1b[6;1R")                                               // Cursor report reply
	stream.WriteString("\x1b[?1;2c")                                              // DA query reply
	stream.WriteString("\x1b]11;rgb:00/00/00\x07")                                // OSC reply
	stream.WriteString("\x1b[200~pasted line 1\r\npasted line 2 with 🚀\x1b[201~") // Paste

	data := stream.Bytes()

	// 1. Reference: decode all bytes at once
	refDec := NewDecoder()
	baseline := refDec.Feed(data)
	if len(baseline) == 0 {
		t.Fatal("baseline decode produced 0 units")
	}

	// Helper to check decoded equivalence
	checkEqual := func(name string, got []Decoded) {
		t.Helper()
		if len(got) != len(baseline) {
			t.Fatalf("%s: length mismatch: got %d, want %d", name, len(got), len(baseline))
		}
		for i := range baseline {
			if !bytes.Equal(got[i].Key, baseline[i].Key) ||
				got[i].Paste != baseline[i].Paste ||
				got[i].PasteStart != baseline[i].PasteStart ||
				got[i].PasteEnd != baseline[i].PasteEnd ||
				got[i].Rejected != baseline[i].Rejected ||
				got[i].Reply != baseline[i].Reply {
				t.Fatalf("%s at index %d mismatch: got %+v, want %+v", name, i, got[i], baseline[i])
			}
		}
	}

	// 2. Decode 1 byte at a time
	dec1 := NewDecoder()
	var out1 []Decoded
	for _, b := range data {
		out1 = append(out1, dec1.Feed([]byte{b})...)
	}
	checkEqual("1 byte per chunk", out1)

	// 3. Decode 2 bytes at a time
	dec2 := NewDecoder()
	var out2 []Decoded
	for i := 0; i < len(data); i += 2 {
		end := min(i+2, len(data))
		out2 = append(out2, dec2.Feed(data[i:end])...)
	}
	checkEqual("2 bytes per chunk", out2)

	// 4. Decode 3 bytes at a time
	dec3 := NewDecoder()
	var out3 []Decoded
	for i := 0; i < len(data); i += 3 {
		end := min(i+3, len(data))
		out3 = append(out3, dec3.Feed(data[i:end])...)
	}
	checkEqual("3 bytes per chunk", out3)

	// 5. Random chunks (1 to 7 bytes) with deterministic seed
	r := rand.New(rand.NewSource(42))
	for run := 0; run < 10; run++ {
		decRand := NewDecoder()
		var outRand []Decoded
		for idx := 0; idx < len(data); {
			chunkSize := r.Intn(7) + 1
			end := min(idx+chunkSize, len(data))
			outRand = append(outRand, decRand.Feed(data[idx:end])...)
			idx = end
		}
		checkEqual("random chunks", outRand)
	}
}

func TestInputDecoderOversizedPaste(t *testing.T) {
	defer leakcheck.Verify(t)

	d := NewDecoder()
	d.Feed([]byte("\x1b[200~"))

	oversized := bytes.Repeat([]byte{'a'}, pasteBudget+100)
	// Feed oversized in chunks
	for i := 0; i < len(oversized); i += 4096 {
		end := min(i+4096, len(oversized))
		d.Feed(oversized[i:end])
	}
	out := d.Feed([]byte("\x1b[201~"))
	if len(out) != 1 || !out[0].PasteEnd || !out[0].Rejected {
		t.Fatalf("expected rejected PasteEnd, got %+v", out)
	}
}
