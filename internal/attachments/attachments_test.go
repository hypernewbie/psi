//go:build !windows

package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"psi/internal/tools"
)

var testPNG = append(append([]byte{}, pngMagic...), 0x00, 0x01, 0x02, 0x03)

func refFor(name string, data []byte, kind, mime string) Ref {
	sum := sha256.Sum256(data)
	return Ref{
		Basename: name, SizeBytes: uint64(len(data)), MIMEType: mime,
		Kind: kind, SHA256: hex.EncodeToString(sum[:]),
	}
}

func imageRef(name string, data []byte) Ref {
	return refFor(name, data, "image", "image/png")
}

func writeFixture(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openRoot(t *testing.T) (*Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) = %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

func TestPrepareReturnsExactImageBytesAndSafeDisplay(t *testing.T) {
	root, dir := openRoot(t)
	writeFixture(t, dir, "att-1.png", testPNG)

	prepared, err := root.Prepare(context.Background(), "", []Ref{imageRef("att-1.png", testPNG)})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ImageBytes != uint64(len(testPNG)) {
		t.Fatalf("image bytes = %d, want %d", prepared.ImageBytes, len(testPNG))
	}
	if len(prepared.Content) != 1 || prepared.Content[0].Image == nil {
		t.Fatalf("content = %+v, want one image block", prepared.Content)
	}
	decoded, err := base64.StdEncoding.DecodeString(prepared.Content[0].Image.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, testPNG) || prepared.Content[0].Image.MimeType != "image/png" {
		t.Fatalf("image block does not carry the verified bytes")
	}
	if prepared.Display == "" || strings.Contains(prepared.Display, prepared.Content[0].Image.Data) {
		t.Fatalf("display = %q, want a nonempty non-base64 label", prepared.Display)
	}

	withText, err := root.Prepare(context.Background(), "look here", []Ref{imageRef("att-1.png", testPNG)})
	if err != nil {
		t.Fatal(err)
	}
	if len(withText.Content) != 2 || withText.Content[0].Text == nil || withText.Content[0].Text.Text != "look here" {
		t.Fatalf("content = %+v, want leading text then image", withText.Content)
	}
	if withText.Display != "look here" {
		t.Fatalf("display = %q, want the prompt text", withText.Display)
	}
}

func TestPrepareRefusesUnsafeReferences(t *testing.T) {
	root, dir := openRoot(t)
	writeFixture(t, dir, "att-1.png", testPNG)
	writeFixture(t, dir, "other.png", testPNG)

	if err := os.Symlink(filepath.Join(dir, "att-1.png"), filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.png"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		refs []Ref
		want error
	}{
		{"symlink", []Ref{imageRef("link.png", testPNG)}, ErrInvalid},
		{"fifo", []Ref{imageRef("pipe.png", testPNG)}, ErrInvalid},
		{"missing", []Ref{imageRef("absent.png", testPNG)}, ErrMissing},
		{"size mismatch", []Ref{{Basename: "att-1.png", SizeBytes: uint64(len(testPNG)) + 1, MIMEType: "image/png", Kind: "image", SHA256: refFor("", testPNG, "", "").SHA256}}, ErrInvalid},
		{"digest mismatch", []Ref{func() Ref { r := imageRef("att-1.png", testPNG); r.SHA256 = strings.Repeat("0", 64); return r }()}, ErrInvalid},
		{"bad signature", []Ref{imageRef("other.png", []byte("not a png at all"))}, ErrUnsupportedImage},
	}
	writeFixture(t, dir, "other.png", []byte("not a png at all"))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := root.Prepare(context.Background(), "", tc.refs); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPrepareReturnsNoContentWhenALaterRefFails(t *testing.T) {
	root, dir := openRoot(t)
	writeFixture(t, dir, "att-1.png", testPNG)
	writeFixture(t, dir, "att-2.png", testPNG)

	bad := imageRef("att-2.png", testPNG)
	bad.SHA256 = strings.Repeat("0", 64)
	prepared, err := root.Prepare(context.Background(), "two", []Ref{imageRef("att-1.png", testPNG), bad})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want invalid", err)
	}
	if len(prepared.Content) != 0 || prepared.ImageBytes != 0 {
		t.Fatalf("prepared = %+v, want nothing after a later failure", prepared)
	}
}

func TestPrepareEmitsAReadableFilePath(t *testing.T) {
	root, dir := openRoot(t)
	content := []byte("hello, ordinary file\n")
	path := writeFixture(t, dir, "notes.txt", content)
	prepared, err := root.Prepare(context.Background(), "", []Ref{refFor("notes.txt", content, "file", "application/octet-stream")})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ImageBytes != 0 {
		t.Fatalf("image bytes = %d, want none for a file ref", prepared.ImageBytes)
	}
	if len(prepared.Content) != 1 || prepared.Content[0].Text == nil || prepared.Content[0].Image != nil {
		t.Fatalf("content = %+v, want one text block and no inline bytes", prepared.Content)
	}
	label := prepared.Content[0].Text.Text
	const prefix = "Attached file path (JSON-quoted): "
	if !strings.HasPrefix(label, prefix) {
		t.Fatalf("label = %q, want the file path prefix", label)
	}
	var decoded string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(label, prefix)), &decoded); err != nil {
		t.Fatalf("path is not JSON-quoted: %v", err)
	}
	if decoded != path {
		t.Fatalf("path = %q, want %q", decoded, path)
	}
	// The existing read tool, from an unrelated workspace, must open it.
	registry := tools.NewRegistry(t.TempDir())
	result := registry.Dispatch(tools.Invocation{Name: tools.Read, Arguments: map[string]any{"path": decoded}})
	if result.IsError {
		t.Fatalf("read tool refused the emitted path: %+v", result.Content)
	}
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "hello, ordinary file") {
		t.Fatalf("read result = %+v, want the fixture contents", result.Content)
	}
}

// Every signature Phi treats as an image is refused as an ordinary file, and a
// plain binary is accepted as a path.
func TestPrepareRefusesDisguisedImages(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "png", data: testPNG},
		{name: "jpeg", data: []byte{0xff, 0xd8, 0xff, 0xe0}},
		{name: "gif", data: []byte("GIF89a")},
		{name: "webp", data: []byte("RIFF\x00\x00\x00\x00WEBP")},
		{name: "bmp", data: []byte("BM\x00\x00")},
		{name: "heic", data: []byte("\x00\x00\x00\x18ftypheic")},
		{name: "avif", data: []byte("\x00\x00\x00\x18ftypavif")},
		{name: "svg", data: []byte("<?xml version=\"1.0\"?><SvG width=\"1\"></SvG>")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, dir := openRoot(t)
			writeFixture(t, dir, "notes.bin", tc.data)
			if _, err := root.Prepare(context.Background(), "", []Ref{refFor("notes.bin", tc.data, "file", "application/octet-stream")}); !errors.Is(err, ErrUnsupportedImage) {
				t.Fatalf("err = %v, want unsupported-image", err)
			}
		})
	}
	t.Run("plain binary", func(t *testing.T) {
		root, dir := openRoot(t)
		data := []byte{0x00, 0x01, 0x02, 0xff, 0x00}
		writeFixture(t, dir, "notes.bin", data)
		prepared, err := root.Prepare(context.Background(), "", []Ref{refFor("notes.bin", data, "file", "application/octet-stream")})
		if err != nil || len(prepared.Content) != 1 {
			t.Fatalf("prepared = %+v, %v; want one path block", prepared, err)
		}
	})
}

func TestFileLabelQuotesAnAwkwardRootPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), `root "quoted"`)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	content := []byte("x")
	writeFixture(t, dir, "notes.txt", content)
	prepared, err := root.Prepare(context.Background(), "", []Ref{refFor("notes.txt", content, "file", "application/octet-stream")})
	if err != nil {
		t.Fatal(err)
	}
	label := prepared.Content[0].Text.Text
	var decoded string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(label, "Attached file path (JSON-quoted): ")), &decoded); err != nil {
		t.Fatalf("awkward root is not JSON-quoted: %v", err)
	}
	if decoded != filepath.Join(dir, "notes.txt") {
		t.Fatalf("path = %q, want %q", decoded, filepath.Join(dir, "notes.txt"))
	}
}

func TestPrepareDetectsAFileSwappedAfterOpen(t *testing.T) {
	root, dir := openRoot(t)
	path := writeFixture(t, dir, "att-1.png", testPNG)

	afterOpenHook = func() {
		afterOpenHook = nil
		if err := os.WriteFile(path, append(append([]byte{}, pngMagic...), 9, 9, 9, 9), 0o600); err != nil {
			t.Error(err)
		}
	}
	defer func() { afterOpenHook = nil }()

	if _, err := root.Prepare(context.Background(), "", []Ref{imageRef("att-1.png", testPNG)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want invalid after the file changed", err)
	}
}

func TestOpenRefusesSymlinkAndReplacedRoots(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("symlink root accepted: %v", err)
	}

	root, err := Open(real)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Check(); err != nil {
		t.Fatalf("fresh root check = %v", err)
	}
	// Rename the checked directory away: the path now names nothing.
	if err := os.Rename(real, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := root.Check(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("moved root check = %v, want unavailable", err)
	}
	if _, err := root.Prepare(context.Background(), "", []Ref{imageRef("att-1.png", testPNG)}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("prepare with a moved root = %v, want unavailable", err)
	}
	_ = root.Close()
}

func TestValidateEnforcesDeclaredBounds(t *testing.T) {
	png := imageRef("att-1.png", testPNG)
	cases := []struct {
		name     string
		text     string
		refs     []Ref
		want     error
		hasImage bool
	}{
		{name: "no refs", refs: nil, want: ErrLimit},
		{name: "nine refs", refs: nineRefs(png), want: ErrLimit},
		{name: "text too long", text: strings.Repeat("a", MaxTextBytes+1), refs: []Ref{png}, want: ErrLimit},
		{name: "invalid utf8", text: "\xff", refs: []Ref{png}, want: ErrInvalid},
		{name: "file too large", refs: []Ref{bigImage(uint64(MaxFileBytes) + 1)}, want: ErrLimit},
		{name: "total too large", refs: []Ref{rename(bigImage(MaxFileBytes), "a.png"), rename(bigImage(MaxFileBytes), "b.png"), rename(bigImage(1), "c.png")}, want: ErrLimit},
		{name: "over-long basename", refs: []Ref{rename(png, strings.Repeat("a", MaxNameBytes+1)+".png")}, want: ErrInvalid},
		{name: "traversal", refs: []Ref{rename(png, "../escape.png")}, want: ErrInvalid},
		{name: "separator", refs: []Ref{rename(png, "a/b.png")}, want: ErrInvalid},
		{name: "duplicate", refs: []Ref{png, png}, want: ErrInvalid},
		{name: "bad kind", refs: []Ref{func() Ref { r := png; r.Kind = "blob"; return r }()}, want: ErrInvalid},
		{name: "bad image mime", refs: []Ref{func() Ref { r := png; r.MIMEType = "image/gif"; return r }()}, want: ErrUnsupportedImage},
		{name: "wrong suffix", refs: []Ref{rename(png, "att-1.jpeg")}, want: ErrUnsupportedImage},
		{name: "uppercase digest", refs: []Ref{func() Ref { r := png; r.SHA256 = strings.ToUpper(r.SHA256); return r }()}, want: ErrInvalid},
		{name: "file mime", refs: []Ref{func() Ref { r := png; r.Kind, r.MIMEType = "file", "image/png"; return r }()}, want: ErrInvalid},
		{name: "image", refs: []Ref{png}, hasImage: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hasImage, _, err := Validate(tc.text, tc.refs)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
				return
			}
			if err != nil || hasImage != tc.hasImage {
				t.Fatalf("Validate = (%v, %v), want image=%v and no error", hasImage, err, tc.hasImage)
			}
		})
	}
}

func TestValidateAcceptsTheDeclaredBoundary(t *testing.T) {
	perFile := uint64(MaxTotalBytes / MaxRefs)
	refs := make([]Ref, MaxRefs)
	for i := range refs {
		refs[i] = rename(bigImage(perFile), "att-"+strings.Repeat("x", i)+".png")
	}
	if _, _, err := Validate(strings.Repeat("a", MaxTextBytes), refs); err != nil {
		t.Fatalf("64 KiB text and eight refs at the 50 MiB total = %v, want accepted", err)
	}
	if _, _, err := Validate("", []Ref{rename(bigImage(MaxFileBytes), "a.png"), rename(bigImage(MaxFileBytes), "b.png")}); err != nil {
		t.Fatalf("two 25 MiB refs = %v, want accepted", err)
	}
}

func nineRefs(base Ref) []Ref {
	refs := make([]Ref, 9)
	for i := range refs {
		refs[i] = rename(base, "att-"+strings.Repeat("x", i)+".png")
	}
	return refs
}

func bigImage(size uint64) Ref {
	return Ref{Basename: "att-big.png", SizeBytes: size, MIMEType: "image/png", Kind: "image", SHA256: strings.Repeat("a", 64)}
}

func rename(ref Ref, name string) Ref {
	ref.Basename = name
	return ref
}

// A cancelled context refuses before any payload byte is read, so a lost
// bridge stream cannot keep a worker reading a large file.
func TestPrepareRefusesOnACancelledContextBeforeReading(t *testing.T) {
	root, _ := openRoot(t)
	// The ref names a file that does not exist: if the read ran, the error
	// would be "missing item" instead of "unavailable".
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := root.Prepare(ctx, "", []Ref{imageRef("absent.png", testPNG)}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable before the read", err)
	}
	// A context cancelled mid-preparation still discards earlier refs.
	root2, dir2 := openRoot(t)
	writeFixture(t, dir2, "att-1.png", testPNG)
	if _, err := root2.Prepare(ctx, "", []Ref{imageRef("att-1.png", testPNG), imageRef("absent.png", testPNG)}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable between refs", err)
	}
}
