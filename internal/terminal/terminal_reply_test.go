package terminal

import (
	"bytes"
	"testing"
)

func TestDecoderRoutesOSCColorAndAppearanceRepliesAtEverySplit(t *testing.T) {
	for _, response := range [][]byte{
		[]byte("\x1b]10;#102030\a"),
		[]byte("\x1b]11;rgb:ffff/0000/8000\x1b\\"),
		[]byte("\x1b]4;15;#abcdef\a"),
		[]byte("\x1b[?997;2n"),
	} {
		for split := 0; split <= len(response); split++ {
			decoder := NewDecoder()
			var decoded []Decoded
			decoded = append(decoded, decoder.Feed(response[:split])...)
			decoded = append(decoded, decoder.Feed(response[split:])...)
			if len(decoded) != 1 || !decoded[0].Reply || !bytes.Equal(decoded[0].Key, response) {
				t.Fatalf("split %d/%d decoded=%+v", split, len(response), decoded)
			}
		}
	}
}

func TestDecoderRejectsMalformedAndOversizedOSCWithoutLeakingText(t *testing.T) {
	decoder := NewDecoder()
	malformed := decoder.Feed([]byte("\x1b]10;not-a-color\a"))
	if len(malformed) != 1 || !malformed[0].Reply {
		t.Fatalf("malformed OSC leaked as editor input: %+v", malformed)
	}
	decoder = NewDecoder()
	tooLong := append([]byte("\x1b]"), bytes.Repeat([]byte{'x'}, maxPendingEscape+10)...)
	tooLong = append(tooLong, 0x07, 'z')
	decoded := decoder.Feed(tooLong)
	if len(decoded) != 1 || decoded[0].Reply || string(decoded[0].Key) != "z" {
		t.Fatalf("oversized OSC discard=%+v", decoded)
	}
}

func TestPastedOSCColorLookalikeIsNotATerminalReply(t *testing.T) {
	decoder := NewDecoder()
	data := []byte("\x1b[200~\x1b]10;#123456\a\x1b[201~")
	decoded := decoder.Feed(data)
	for _, item := range decoded {
		if item.Reply {
			t.Fatalf("pasted lookalike was treated as terminal reply: %+v", decoded)
		}
	}
	if len(decoded) == 0 || !decoded[len(decoded)-1].PasteEnd {
		t.Fatalf("paste did not finish: %+v", decoded)
	}
}
