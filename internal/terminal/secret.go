package terminal

import (
	"errors"
	"strings"
)

const (
	secretPasteTooLarge = "Paste exceeds 256 KiB. Draft unchanged."
	secretPasteNewline  = "Pasted API key must be on one line. Draft unchanged."
	secretInputTooLarge = "Input exceeds 256 KiB. Draft unchanged."
)

func (c *Console) activeEditor() *Editor {
	if c.secret != nil {
		return c.secret
	}
	return c.Editor
}

// BeginSecret starts a fresh masked editor and removes normal prompt state.
func (c *Console) BeginSecret() *Editor {
	c.Editor.Clear()
	c.clearCompletion(false)
	c.picker = nil
	c.pasteActive = false
	c.pasteBuf.Reset()
	c.resetSecretPaste()
	c.secret = NewEditor()
	c.secret.Masked = true
	c.lastDraft, c.lastCursor = "", 0
	return c.secret
}

// EndSecret returns the secret and clears all secret-editor state.
func (c *Console) EndSecret() string {
	if c.secret == nil {
		return ""
	}
	text := c.secret.Text()
	c.clearSecret()
	return text
}

// DiscardSecret clears the active secret without creating a plaintext copy.
func (c *Console) DiscardSecret() {
	if c.secret != nil {
		c.clearSecret()
	}
}

func (c *Console) clearSecret() {
	clear(c.secret.buf)
	clear(c.secret.history)
	c.secret.buf = nil
	c.secret.history = nil
	c.secret = nil
	c.resetSecretPaste()
	c.lastDraft, c.lastCursor = "", 0
	c.drawIfChanged(true)
}

func (c *Console) resetSecretPaste() {
	clear(c.secretPaste.Bytes())
	c.secretPaste.Reset()
	c.secretPasteActive = false
	c.secretPasteRejected = false
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
			if len(input.Key)+runeToByte(c.secret.buf, len(c.secret.buf))+c.secretPaste.Len() > editorBudget {
				c.secretPasteRejected = true
			} else {
				_, _ = c.secretPaste.Write(input.Key)
			}
		}
		if !input.PasteEnd {
			return false, false, nil
		}
		wasRejected := c.secretPasteRejected
		content := string(c.secretPaste.Bytes())
		c.resetSecretPaste()
		if wasRejected {
			return false, false, errors.New(secretPasteTooLarge)
		}
		if strings.ContainsAny(content, "\r\n") {
			return false, false, errors.New(secretPasteNewline)
		}
		beforeLen, beforePos := len(c.secret.buf), c.secret.Pos()
		if !c.secret.Insert(content) {
			return false, false, errors.New(secretInputTooLarge)
		}
		if beforeLen != len(c.secret.buf) || beforePos != c.secret.Pos() {
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
			beforeLen, beforePos := len(c.secret.buf), c.secret.Pos()
			c.secret.Delete()
			if beforeLen != len(c.secret.buf) || beforePos != c.secret.Pos() {
				c.drawIfChanged(false)
			}
			return false, false, nil
		}
	}
	beforeLen, beforePos := len(c.secret.buf), c.secret.Pos()
	c.secret.rejectedInsert = false
	c.secret.HandleKey(key, false)
	if c.secret.rejectedInsert {
		return false, false, errors.New(secretInputTooLarge)
	}
	if beforeLen != len(c.secret.buf) || beforePos != c.secret.Pos() {
		c.drawIfChanged(false)
	}
	return false, false, nil
}
