package terminal

import (
	"bytes"
	"errors"

	"psi/internal/secretmem"
)

const (
	secretPasteTooLarge = "Paste exceeds 256 KiB. Draft unchanged."
	secretPasteNewline  = "Paste must be on one line. Draft unchanged."
	secretInputTooLarge = "Input exceeds 256 KiB. Draft unchanged."
)

type displayEditor interface {
	DisplayBuffer() []rune
	DisplayPos() int
}

func (c *Console) activeEditor() displayEditor {
	if c.secret != nil {
		return c.secret
	}
	return c.Editor
}

// BeginSecret starts a fresh masked editor and removes normal prompt state.
func (c *Console) BeginSecret() {
	if c.secret != nil {
		c.secret.discard()
		c.secret = nil
	}
	c.Editor.Clear()
	c.clearCompletion(false)
	c.picker = nil
	c.resetPaste()
	c.resetSecretPaste()
	c.secret = newSecretEditor()
	c.lastDraft, c.lastCursor = "", 0
}

// EndSecret transfers the secret to the caller and clears all secret-editor
// state. The caller owns the returned bytes and must wipe them after use.
func (c *Console) EndSecret() []byte {
	if c.secret == nil {
		return nil
	}
	draft := c.secret.detach()
	c.clearSecretState()
	return draft
}

// DiscardSecret clears the active secret without creating a plaintext copy.
func (c *Console) DiscardSecret() {
	if c.secret != nil {
		c.clearSecret()
	}
}

func (c *Console) clearSecret() {
	if c.secret != nil {
		c.secret.discard()
	}
	c.clearSecretState()
}

func (c *Console) clearSecretState() {
	c.secret = nil
	c.resetSecretPaste()
	c.lastDraft, c.lastCursor = "", 0
	c.drawIfChanged(true)
}

func (c *Console) resetSecretPaste() {
	secretmem.Wipe(c.secretPaste[:cap(c.secretPaste)])
	c.secretPaste = nil
	c.secretPasteActive = false
	c.secretPasteRejected = false
}

func (c *Console) appendSecretPaste(b []byte) {
	if len(b) == 0 {
		return
	}
	newLen := len(c.secretPaste) + len(b)
	if newLen > cap(c.secretPaste) {
		newCap := cap(c.secretPaste) * 2
		if newCap < newLen {
			newCap = newLen
		}
		next := make([]byte, newLen, newCap)
		copy(next, c.secretPaste)
		copy(next[len(c.secretPaste):], b)
		secretmem.Wipe(c.secretPaste[:cap(c.secretPaste)])
		c.secretPaste = next
		return
	}
	c.secretPaste = append(c.secretPaste, b...)
}

// HandleSecret edits a masked single-line prompt without using Submit or history.
func (c *Console) HandleSecret(input Input) (submitted, cancelled bool, err error) {
	if c.secret == nil {
		return false, true, errors.New("secret prompt is not active")
	}
	if input.Err != nil {
		return false, true, nil
	}
	if input.Resize {
		c.RequestFullRedraw()
		return false, false, nil
	}
	if len(input.Reply) > 0 || input.FollowUp {
		return false, false, nil
	}
	if input.PasteStart {
		c.resetSecretPaste()
		c.secretPasteActive = true
		return false, false, nil
	}
	if input.Paste || input.PasteEnd {
		if !c.secretPasteActive {
			return false, false, nil
		}
		if input.Rejected {
			c.secretPasteRejected = true
		}
		if input.Paste && len(input.Key) > 0 && !c.secretPasteRejected {
			if len(input.Key)+len(c.secret.buf)+len(c.secretPaste) > editorBudget {
				c.secretPasteRejected = true
			} else {
				c.appendSecretPaste(input.Key)
			}
		}
		if !input.PasteEnd {
			return false, false, nil
		}
		wasRejected := c.secretPasteRejected
		content := c.secretPaste
		if wasRejected {
			c.resetSecretPaste()
			return false, false, errors.New(secretPasteTooLarge)
		}
		hasNewline := bytes.IndexByte(content, '\r') >= 0 || bytes.IndexByte(content, '\n') >= 0
		if hasNewline {
			c.resetSecretPaste()
			return false, false, errors.New(secretPasteNewline)
		}
		beforeLen, beforePos := len(c.secret.buf), c.secret.pos
		inserted := c.secret.insert(content)
		c.resetSecretPaste()
		if !inserted {
			return false, false, errors.New(secretInputTooLarge)
		}
		if beforeLen != len(c.secret.buf) || beforePos != c.secret.pos {
			c.drawIfChanged(false)
		}
		return false, false, nil
	}

	key := input.Key
	if len(key) == 1 {
		switch key[0] {
		case '\r':
			return true, false, nil
		case '\n':
			return false, false, nil
		case 3, 27:
			return false, true, nil
		case 4:
			beforeLen, beforePos := len(c.secret.buf), c.secret.pos
			c.secret.delete()
			if beforeLen != len(c.secret.buf) || beforePos != c.secret.pos {
				c.drawIfChanged(false)
			}
			return false, false, nil
		}
	}
	beforeLen, beforePos := len(c.secret.buf), c.secret.pos
	c.secret.handleKey(key)
	if c.secret.rejectedInsert {
		return false, false, errors.New(secretInputTooLarge)
	}
	if beforeLen != len(c.secret.buf) || beforePos != c.secret.pos {
		c.drawIfChanged(false)
	}
	return false, false, nil
}
