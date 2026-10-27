package terminal

import (
	"bytes"
	"strings"
	"time"
	"unicode/utf8"
)

// Decoder is the bounded input state machine (P2-018).
// It decodes UTF-8, legacy keys, bracketed paste, and terminal replies independently of rendering.
// - Sequences split at every byte boundary.
// - 50 ms idle timeout on incomplete Escape prefix.
// - Bounded pending legacy escape to 256 bytes.
// - CRLF/lone CR normalized to LF in pasted text.
// - Oversized paste rejected as a whole edit with drain through end marker.

const (
	maxPendingEscape = 256
	pasteBudget      = 256 * 1024
	escapeTimeout    = 50 * time.Millisecond
)

type Decoded struct {
	Key        []byte // single key unit (paste content delivered as chunks with Paste=true)
	Paste      bool   // inside bracketed paste content
	PasteStart bool
	PasteEnd   bool
	Rejected   bool // oversized paste rejected as whole edit
	Reply      bool // terminal reply (ignored, not consumed as input)
	Timeout    bool // escape prefix timed out
}

type Decoder struct {
	pending   []byte
	pendingAt time.Time
	inPaste   bool
	pasteBuf  []byte
	pasteOver bool // oversized paste: draining through end marker
	pasteSize int  // bytes accepted before over-limit
	now       func() time.Time
}

func NewDecoder() *Decoder { return &Decoder{now: time.Now} }

func (d *Decoder) SetClock(now func() time.Time) { d.now = now }

// Feed consumes bytes and returns decoded units.
func (d *Decoder) Feed(data []byte) []Decoded {
	var out []Decoded
	for _, b := range data {
		out = append(out, d.feedByte(b)...)
	}
	// Timeout check for incomplete escape.
	if len(d.pending) > 0 && d.now().Sub(d.pendingAt) >= escapeTimeout {
		// Emit pending as individual bytes (lone Escape first).
		if len(d.pending) == 1 && d.pending[0] == 27 {
			out = append(out, Decoded{Key: []byte{27}, Timeout: true})
		} else {
			for _, bb := range d.pending {
				out = append(out, Decoded{Key: []byte{bb}})
			}
		}
		d.pending = nil
	}
	return out
}

func (d *Decoder) feedByte(b byte) []Decoded {
	// Inside paste: buffer until end marker, normalize, enforce budget.
	if d.inPaste {
		d.pasteBuf = append(d.pasteBuf, b)
		if bytes.HasSuffix(d.pasteBuf, []byte("\x1b[201~")) {
			content := d.pasteBuf[:len(d.pasteBuf)-6]
			d.inPaste = false
			d.pasteBuf = nil
			if d.pasteOver {
				d.pasteOver = false
				d.pasteSize = 0
				return []Decoded{{Paste: true, PasteEnd: true, Rejected: true}} // signals rejection notice
			}
			// Normalize CRLF/lone CR to LF, preserve LF.
			s := strings.ReplaceAll(string(content), "\r\n", "\n")
			s = strings.ReplaceAll(s, "\r", "\n")
			// Filter controls except \n\t (do not interpret as cancel/commands).
			var f strings.Builder
			for _, r := range s {
				if r == '\n' || r == '\t' || (r >= 0x20 && (r < 0x7F || r > 0x9F)) || r > 0x9F {
					if r == utf8.RuneError {
						continue
					}
					f.WriteRune(r)
				}
			}
			norm := f.String()
			if len(norm) > pasteBudget {
				return []Decoded{{Paste: true, PasteEnd: true, Rejected: true}}
			}
			// Deliver as one bounded transaction (chunked if large, but single logical unit).
			const chunk = 64 * 1024
			var out []Decoded
			out = append(out, Decoded{Paste: true, PasteStart: true})
			for i := 0; i < len(norm); i += chunk {
				end := i + chunk
				if end > len(norm) {
					end = len(norm)
				}
				out = append(out, Decoded{Key: []byte(norm[i:end]), Paste: true})
			}
			out = append(out, Decoded{Paste: true, PasteEnd: true})
			return out
		}
		if len(d.pasteBuf) > pasteBudget+16 {
			// Oversized: keep draining, mark rejection.
			d.pasteOver = true
			// Bound memory: keep only suffix for end-marker detection.
			if len(d.pasteBuf) > 32 {
				d.pasteBuf = append([]byte(nil), d.pasteBuf[len(d.pasteBuf)-16:]...)
			}
		}
		return nil
	}
	// Check paste markers.
	d.pending = append(d.pending, b)
	if bytes.HasSuffix(d.pending, []byte("\x1b[200~")) {
		// Strip marker, enter paste.
		d.pending = d.pending[:len(d.pending)-6]
		var out []Decoded
		if len(d.pending) > 0 {
			out = append(out, d.emitPending()...)
		}
		d.inPaste = true
		d.pasteBuf = nil
		d.pasteOver = false
		out = append(out, Decoded{Paste: true, PasteStart: true})
		return out
	}
	// Bound pending escape.
	if len(d.pending) > maxPendingEscape {
		out := d.emitPending()
		return out
	}
	// Try to emit complete units.
	return d.emitReady()
}

func (d *Decoder) emitPending() []Decoded {
	var out []Decoded
	for len(d.pending) > 0 {
		u, n := splitUnit(d.pending)
		if n == 0 {
			break
		}
		out = append(out, Decoded{Key: append([]byte(nil), u...)})
		d.pending = append([]byte(nil), d.pending[n:]...)
	}
	if len(d.pending) > 0 {
		// Incomplete: keep for timeout, record time.
		if d.pendingAt.IsZero() {
			d.pendingAt = d.now()
		}
	} else {
		d.pendingAt = time.Time{}
	}
	return out
}

func (d *Decoder) emitReady() []Decoded {
	var out []Decoded
	for len(d.pending) > 0 {
		u, n := splitUnit(d.pending)
		if n == 0 {
			if d.pendingAt.IsZero() {
				d.pendingAt = d.now()
			}
			break
		}
		// Terminal replies (e.g. cursor reports, focus) are ignored, not consumed as input.
		if isReply(u) {
			out = append(out, Decoded{Key: u, Reply: true})
		} else {
			out = append(out, Decoded{Key: append([]byte(nil), u...)})
		}
		d.pending = append([]byte(nil), d.pending[n:]...)
		d.pendingAt = time.Time{}
	}
	return out
}

// splitUnit returns one complete input unit and its length, or 0 if incomplete.
func splitUnit(p []byte) ([]byte, int) {
	if len(p) == 0 {
		return nil, 0
	}
	if p[0] == 27 {
		if len(p) < 2 {
			return nil, 0
		}
		if p[1] == '[' || p[1] == 'O' {
			size := 2
			for size < len(p) && !(p[size] >= 0x40 && p[size] <= 0x7E) {
				size++
			}
			if size == len(p) {
				return nil, 0
			}
			return p[:size+1], size + 1
		}
		return p[:2], 2
	}
	if p[0] >= utf8.RuneSelf {
		if !utf8.FullRune(p) {
			return nil, 0
		}
		_, size := utf8.DecodeRune(p)
		return p[:size], size
	}
	return p[:1], 1
}

func isReply(u []byte) bool {
	s := string(u)
	// Cursor position reports, device attrs, focus in/out, kitty query replies.
	if s == "\x1b[I" || s == "\x1b[O" {
		return true
	}
	if len(s) >= 3 && s[0] == 27 && s[1] == '[' && s[len(s)-1] == 'R' {
		return true
	}
	if len(s) >= 3 && s[0] == 27 && s[1] == '[' && s[len(s)-1] == 'c' {
		return true
	}
	return false
}
