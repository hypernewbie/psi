package terminal

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/secretmem"
	"github.com/hypernewbie/psi/internal/testutil"
)

func startSecretTest(t *testing.T) (*Console, *testutil.VTModel, *bytes.Buffer) {
	t.Helper()
	c, vt, out := newTestConsole(80, 24)
	c.BeginSecret()
	c.Status("API key · Enter save · Esc cancel")
	return c, vt, out
}

func secretInput(c *Console, input Input) ([]byte, bool, bool, error) {
	submitted, cancelled, err := c.HandleSecret(input)
	draft := []byte(nil)
	if submitted {
		draft = c.secret.buf
	}
	return draft, submitted, cancelled, err
}

func wipeTestSecret(t *testing.T, b []byte) {
	t.Helper()
	t.Cleanup(func() { secretmem.Wipe(b[:cap(b)]) })
}

func TestSecretEditorMasksAndSubmitsWithoutHistory(t *testing.T) {
	c, vt, _ := startSecretTest(t)
	canary := []byte("sk-test-$-!")
	if _, submitted, cancelled, err := secretInput(c, Input{Key: canary}); err != nil || submitted || cancelled {
		t.Fatalf("typed key: submitted=%v cancelled=%v err=%v", submitted, cancelled, err)
	}
	c.Draw()
	if got := vt.Text(); !strings.Contains(got, strings.Repeat("*", len(canary))) || strings.Contains(got, string(canary)) {
		t.Fatalf("masked screen does not hide secret: %q", got)
	}
	if c.lastDraft != strings.Repeat("*", len(canary)) {
		t.Fatalf("redraw cache contains unexpected draft: %q", c.lastDraft)
	}
	text, submitted, cancelled, err := secretInput(c, Input{Key: []byte{'\r'}})
	if err != nil || !submitted || cancelled || !bytes.Equal(text, canary) {
		t.Fatalf("submit = %q, %v, %v, %v", text, submitted, cancelled, err)
	}
	if len(c.Editor.History()) != 0 {
		t.Fatal("secret input was added to editor history")
	}
}

func TestSecretPasteIsSingleLineAtomicAndBounded(t *testing.T) {
	c, _, _ := startSecretTest(t)
	_, _, _, _ = secretInput(c, Input{Key: []byte("prior")})
	_, _, _, _ = secretInput(c, Input{PasteStart: true})
	_, _, _, _ = secretInput(c, Input{Key: []byte("part\nmore"), Paste: true})
	_, submitted, cancelled, err := secretInput(c, Input{Paste: true, PasteEnd: true})
	if err == nil || submitted || cancelled || !bytes.Equal(c.secret.buf, []byte("prior")) {
		t.Fatalf("newline paste changed draft: text=%q submitted=%v cancelled=%v err=%v", c.secret.buf, submitted, cancelled, err)
	}

	_, _, _, _ = secretInput(c, Input{PasteStart: true})
	_, _, _, _ = secretInput(c, Input{Key: []byte("-paste"), Paste: true})
	_, submitted, cancelled, err = secretInput(c, Input{Paste: true, PasteEnd: true})
	if err != nil || submitted || cancelled || !bytes.Equal(c.secret.buf, []byte("prior-paste")) {
		t.Fatalf("valid paste: text=%q submitted=%v cancelled=%v err=%v", c.secret.buf, submitted, cancelled, err)
	}

	_, _, _, _ = secretInput(c, Input{PasteStart: true})
	_, _, _, err = secretInput(c, Input{Paste: true, PasteEnd: true, Rejected: true})
	if err == nil || !bytes.Equal(c.secret.buf, []byte("prior-paste")) {
		t.Fatalf("decoder-rejected paste changed draft: text=%q err=%v", c.secret.buf, err)
	}
	if _, _, _, err := secretInput(c, Input{Key: []byte{'x'}}); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = secretInput(c, Input{PasteStart: true})
	tooLarge := bytes.Repeat([]byte{'x'}, editorBudget+1)
	_, _, _, _ = secretInput(c, Input{Key: tooLarge, Paste: true})
	if len(c.secretPaste) > editorBudget {
		t.Fatalf("paste buffer exceeded budget: %d", len(c.secretPaste))
	}
	_, submitted, cancelled, err = secretInput(c, Input{Paste: true, PasteEnd: true})
	if err == nil || submitted || cancelled || !bytes.Equal(c.secret.buf, []byte("prior-pastex")) {
		t.Fatalf("oversized paste was not rejected atomically: text length=%d submitted=%v cancelled=%v err=%v", len(c.secret.buf), submitted, cancelled, err)
	}
}

func TestSecretTypedBudgetAndUnsupportedControls(t *testing.T) {
	c, vt, _ := startSecretTest(t)
	if !c.secret.insert(bytes.Repeat([]byte("x"), editorBudget)) {
		t.Fatal("could not prepare budget-boundary draft")
	}
	_, submitted, cancelled, err := secretInput(c, Input{Key: []byte{'z'}})
	if err == nil || submitted || cancelled || len(c.secret.buf) != editorBudget {
		t.Fatalf("typed over-budget input changed draft: submitted=%v cancelled=%v err=%v", submitted, cancelled, err)
	}

	c.secret.discard()
	canary := []byte("secret")
	_, _, _, _ = secretInput(c, Input{Key: canary})
	for _, key := range []byte{10, 26, 16, 12, 20, 24} {
		_, submitted, cancelled, err = secretInput(c, Input{Key: []byte{key}})
		if err != nil || submitted || cancelled {
			t.Fatalf("control %d escaped secret prompt: submitted=%v cancelled=%v err=%v", key, submitted, cancelled, err)
		}
	}
	if !bytes.Equal(c.secret.buf, canary) || c.Editor.Text() != "" || strings.Contains(vt.Text(), string(canary)) {
		t.Fatalf("control input exposed or transferred secret: secret=%q main=%q screen=%q", c.secret.buf, c.Editor.Text(), vt.Text())
	}
	_, _, _, _ = secretInput(c, Input{Key: []byte("\x1b[D")})
	_, submitted, cancelled, err = secretInput(c, Input{Key: []byte{4}})
	if err != nil || submitted || cancelled || !bytes.Equal(c.secret.buf, []byte("secre")) {
		t.Fatalf("Ctrl+D did not edit the secret: text=%q submitted=%v cancelled=%v err=%v", c.secret.buf, submitted, cancelled, err)
	}
}

func TestSecretResizeCancelAndEndDoNotRestoreDraftOrHistory(t *testing.T) {
	c, vt, _ := startSecretTest(t)
	canary := []byte("sk-resize-canary")
	_, _, _, _ = secretInput(c, Input{Key: canary})
	_, submitted, cancelled, err := secretInput(c, Input{Resize: true})
	if err != nil || submitted || cancelled {
		t.Fatalf("resize: submitted=%v cancelled=%v err=%v", submitted, cancelled, err)
	}
	c.Draw()
	if strings.Contains(vt.Text(), string(canary)) || c.lastDraft != strings.Repeat("*", len(canary)) {
		t.Fatalf("resize exposed secret: screen=%q cache=%q", vt.Text(), c.lastDraft)
	}
	_, submitted, cancelled, err = secretInput(c, Input{Key: []byte{27}})
	if err != nil || submitted || !cancelled {
		t.Fatalf("Escape: submitted=%v cancelled=%v err=%v", submitted, cancelled, err)
	}
	got := c.EndSecret()
	wipeTestSecret(t, got)
	if !bytes.Equal(got, canary) {
		t.Fatalf("EndSecret = %q", got)
	}
	c.Status("")
	if c.secret != nil || c.Editor.Text() != "" || len(c.Editor.History()) != 0 {
		t.Fatal("ending secret restored it to the normal editor or history")
	}

	c.BeginSecret()
	_, _, _, _ = secretInput(c, Input{Key: canary})
	_, submitted, cancelled, err = secretInput(c, Input{Key: []byte{3}})
	if err != nil || submitted || !cancelled {
		t.Fatalf("Ctrl+C: submitted=%v cancelled=%v err=%v", submitted, cancelled, err)
	}
	got = c.EndSecret()
	wipeTestSecret(t, got)
	if c.Editor.Text() != "" || len(c.Editor.History()) != 0 {
		t.Fatal("Ctrl+C cancellation restored secret or changed history")
	}
}

func TestSecretPromptKeepsCursorVisibleWithActivityLabel(t *testing.T) {
	var out bytes.Buffer
	c := &Console{Editor: NewEditor(), Out: &out, writer: NewWriter(&out), SizeFunc: func() (int, int) { return 80, 24 }}
	c.Status("working")
	if !c.hidden {
		t.Fatal("activity label did not hide the normal cursor")
	}
	c.BeginSecret()
	c.Status("API key")
	if c.hidden || !strings.Contains(out.String(), "\x1b[?25h") {
		t.Fatalf("secret prompt cursor was hidden: hidden=%v output=%q", c.hidden, out.String())
	}
}

func TestSecretPasteChunksWithoutTransactionAreIgnored(t *testing.T) {
	c, _, _ := startSecretTest(t)
	_, _, _, _ = secretInput(c, Input{Key: []byte("orphan"), Paste: true})
	_, _, _, _ = secretInput(c, Input{Paste: true, PasteEnd: true})
	if len(c.secret.buf) != 0 {
		t.Fatalf("stray paste chunks changed draft: %q", c.secret.buf)
	}
}
