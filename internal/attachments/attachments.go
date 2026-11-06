// Package attachments verifies Phi attachment snapshots before their content
// enters a session. A ref names one basename under one checked root; it never
// carries a path supplied by the browser.
package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"psi/internal/session"
)

const (
	// MaxRefs bounds one prompt, matching Phi's own admission.
	MaxRefs = 8
	// MaxNameBytes bounds one basename. The accepted alphabet is ASCII, so
	// bytes and characters agree.
	MaxNameBytes = 128
	// MaxFileBytes bounds one declared file.
	MaxFileBytes = 25 << 20
	// MaxTotalBytes bounds one declared prompt and the retained image budget.
	MaxTotalBytes = 50 << 20
	// MaxTextBytes bounds the prompt text in UTF-8 bytes.
	MaxTextBytes = 64 << 10

	chunkBytes = 64 << 10
)

// Safe refusal classes. Callers map these to the bridge vocabulary; the error
// text never carries a path.
var (
	ErrUnavailable      = errors.New("attachments: unavailable")
	ErrMissing          = errors.New("attachments: missing item")
	ErrInvalid          = errors.New("attachments: invalid item")
	ErrUnsupportedImage = errors.New("attachments: unsupported image")
	ErrLimit            = errors.New("attachments: limit")
)

// Ref is one process-root basename and its content fence.
type Ref struct {
	Basename  string
	SizeBytes uint64
	MIMEType  string
	Kind      string
	SHA256    string
}

// Prepared is verified content ready for one user message. ImageBytes is the
// verified decoded image total; Content holds the only retained copy.
type Prepared struct {
	Content    []session.UserContentBlock
	Display    string
	ImageBytes uint64
}

// Root is one opened handoff directory. Path is the cleaned absolute path Phi
// provisioned; it is only published inside ordinary-file content.
type Root struct {
	mu   sync.RWMutex
	path string
	root *os.Root
	dev  uint64
	ino  uint64
}

// Open checks that path names a directory, not a symlink, and that the opened
// directory is the same inode the path named. Relative symlinks are rejected by
// the Lstat check even though os.Root would resolve them.
func Open(path string) (*Root, error) {
	if !supported() || !utf8.ValidString(path) || !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	clean := filepath.Clean(path)
	before, err := os.Lstat(clean)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnavailable
	}
	dev, ino, ok := identity(before)
	if !ok {
		return nil, ErrUnavailable
	}
	opened, err := os.OpenRoot(clean)
	if err != nil {
		return nil, ErrUnavailable
	}
	info, err := opened.Stat(".")
	if err != nil {
		_ = opened.Close()
		return nil, ErrUnavailable
	}
	openDev, openIno, ok := identity(info)
	if !ok || !info.IsDir() || openDev != dev || openIno != ino {
		_ = opened.Close()
		return nil, ErrUnavailable
	}
	return &Root{path: clean, root: opened, dev: dev, ino: ino}, nil
}

// handle snapshots the opened directory. A concurrent Close makes later
// operations fail with ordinary errors instead of racing on the field.
func (r *Root) handle() *os.Root {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.root
}

// Path is the cleaned absolute directory Phi provisioned.
func (r *Root) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Check confirms the path still names the opened directory. A renamed or
// replaced root refuses instead of redirecting later reads and paths.
func (r *Root) Check() error {
	root := r.handle()
	if root == nil {
		return ErrUnavailable
	}
	before, err := os.Lstat(r.path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	dev, ino, ok := identity(before)
	if !ok || dev != r.dev || ino != r.ino {
		return ErrUnavailable
	}
	opened, err := root.Stat(".")
	if err != nil {
		return ErrUnavailable
	}
	openDev, openIno, ok := identity(opened)
	if !ok || !opened.IsDir() || openDev != r.dev || openIno != r.ino {
		return ErrUnavailable
	}
	return nil
}

// Close releases the opened directory. A later Check fails, so late readers
// cannot use a closed root.
func (r *Root) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	root := r.root
	r.root = nil
	r.mu.Unlock()
	if root == nil {
		return nil
	}
	return root.Close()
}

// Validate applies every metadata bound without touching the filesystem. It
// reports whether the prompt needs image input and how many decoded image
// bytes it declares, so admission can check the model and budget before a read.
func Validate(text string, refs []Ref) (hasImage bool, imageBytes uint64, err error) {
	if !utf8.ValidString(text) {
		return false, 0, ErrInvalid
	}
	if len(text) > MaxTextBytes {
		return false, 0, ErrLimit
	}
	if len(refs) < 1 || len(refs) > MaxRefs {
		return false, 0, ErrLimit
	}
	seen := make(map[string]struct{}, len(refs))
	var total uint64
	for _, ref := range refs {
		if !validBasename(ref.Basename) {
			return false, 0, ErrInvalid
		}
		if _, duplicate := seen[ref.Basename]; duplicate {
			return false, 0, ErrInvalid
		}
		seen[ref.Basename] = struct{}{}
		if ref.SizeBytes > MaxFileBytes {
			return false, 0, ErrLimit
		}
		if len(ref.SHA256) != 64 || strings.ToLower(ref.SHA256) != ref.SHA256 {
			return false, 0, ErrInvalid
		}
		if _, err := hex.DecodeString(ref.SHA256); err != nil {
			return false, 0, ErrInvalid
		}
		switch ref.Kind {
		case "image":
			if ref.SizeBytes == 0 {
				return false, 0, ErrInvalid
			}
			if !imageExtension(ref.Basename, ref.MIMEType) {
				return false, 0, ErrUnsupportedImage
			}
			hasImage = true
			imageBytes += ref.SizeBytes
		case "file":
			if ref.MIMEType != "application/octet-stream" {
				return false, 0, ErrInvalid
			}
		default:
			return false, 0, ErrInvalid
		}
		total += ref.SizeBytes
	}
	if total > MaxTotalBytes {
		return false, 0, ErrLimit
	}
	return hasImage, imageBytes, nil
}

// Prepare verifies every ref and returns one user message. All refs must pass
// or no content is returned, so a later failure cannot leave partial input.
func (r *Root) Prepare(ctx context.Context, text string, refs []Ref) (Prepared, error) {
	if err := ctx.Err(); err != nil {
		// A cancelled stream must refuse before even opening the root, not
		// after a first Lstat or Check.
		return Prepared{}, ErrUnavailable
	}
	_, imageBytes, err := Validate(text, refs)
	if err != nil {
		return Prepared{}, err
	}
	if err := r.Check(); err != nil {
		return Prepared{}, err
	}
	prepared := Prepared{ImageBytes: imageBytes}
	if text != "" {
		prepared.Content = append(prepared.Content, textBlock(text))
	}
	display := strings.TrimSpace(text)
	if display == "" {
		names := make([]string, 0, len(refs))
		for _, ref := range refs {
			names = append(names, ref.Basename)
		}
		display = "Attached: " + strings.Join(names, ", ")
	}
	prepared.Display = display
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return Prepared{}, ErrUnavailable
		}
		switch ref.Kind {
		case "image":
			data, err := r.read(ctx, ref)
			if err != nil {
				return Prepared{}, err
			}
			if !imageSignature(data, ref.MIMEType) {
				return Prepared{}, ErrUnsupportedImage
			}
			prepared.Content = append(prepared.Content, imageBlock(data, ref.MIMEType))
		case "file":
			data, err := r.read(ctx, ref)
			if err != nil {
				return Prepared{}, err
			}
			if looksLikeImage(data) {
				// The bytes would reach the read tool as a broken image. Claiming
				// them as text is worse than refusing the prompt.
				return Prepared{}, ErrUnsupportedImage
			}
			// The bytes are verified and discarded; only the checked absolute
			// path text survives for the existing read tool.
			prepared.Content = append(prepared.Content, textBlock(fileLabel(r.path, ref.Basename)))
		}
	}
	return prepared, nil
}

// read opens one regular file without following the final symlink and proves
// the opened descriptor is the inode the pre-open Lstat described.
func (r *Root) read(ctx context.Context, ref Ref) ([]byte, error) {
	root := r.handle()
	if root == nil {
		return nil, ErrUnavailable
	}
	before, err := root.Lstat(ref.Basename)
	if err != nil {
		return nil, ErrMissing
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() < 0 {
		return nil, ErrInvalid
	}
	if uint64(before.Size()) != ref.SizeBytes {
		return nil, ErrInvalid
	}
	dev, ino, ok := identity(before)
	if !ok {
		return nil, ErrInvalid
	}
	file, err := root.OpenFile(ref.Basename, readFlags, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, ErrInvalid
	}
	openDev, openIno, ok := identity(opened)
	if !ok || !opened.Mode().IsRegular() || openDev != dev || openIno != ino {
		return nil, ErrInvalid
	}
	if opened.Size() < 0 || uint64(opened.Size()) != ref.SizeBytes {
		return nil, ErrInvalid
	}
	if afterOpenHook != nil {
		afterOpenHook()
	}
	data := make([]byte, 0, int(ref.SizeBytes))
	buf := make([]byte, chunkBytes)
	remaining := int64(ref.SizeBytes) + 1
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return nil, ErrUnavailable
		}
		want := int64(chunkBytes)
		if want > remaining {
			want = remaining
		}
		n, readErr := file.Read(buf[:want])
		if n > 0 {
			data = append(data, buf[:n]...)
			remaining -= int64(n)
			if remaining == 0 {
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, ErrInvalid
		}
	}
	after, err := file.Stat()
	if err != nil {
		return nil, ErrInvalid
	}
	afterDev, afterIno, ok := identity(after)
	if !ok || afterDev != openDev || afterIno != openIno || after.Size() != opened.Size() ||
		!after.ModTime().Equal(opened.ModTime()) {
		return nil, ErrInvalid
	}
	if uint64(len(data)) != ref.SizeBytes {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, ErrInvalid
	}
	return data, nil
}

// afterOpenHook lets tests simulate a file swapped between the pre-open
// identity check and the read. Production leaves it nil.
var afterOpenHook func()

// validBasename mirrors Phi's handoff alphabet. It rejects any separator or
// ".." substring before the name can reach the filesystem.
func validBasename(name string) bool {
	if name == "" || len(name) > MaxNameBytes || name == "." || name == ".." {
		return false
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// imageExtension keeps the declared kind, MIME, and suffix in agreement with
// Phi's own admission. Semantics are case-sensitive there, so they are here.
func imageExtension(name, mime string) bool {
	switch mime {
	case "image/png":
		return strings.HasSuffix(name, ".png")
	case "image/jpeg":
		return strings.HasSuffix(name, ".jpg")
	default:
		return false
	}
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func imageSignature(data []byte, mime string) bool {
	switch mime {
	case "image/png":
		return len(data) >= len(pngMagic) && bytes.Equal(data[:len(pngMagic)], pngMagic)
	case "image/jpeg":
		return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	default:
		return false
	}
}

func textBlock(text string) session.UserContentBlock {
	return session.UserContentBlock{Type: "text", Text: &session.TextContent{Type: "text", Text: text}}
}

func imageBlock(data []byte, mime string) session.UserContentBlock {
	return session.UserContentBlock{Type: "image", Image: &session.ImageContent{
		Type:     "image",
		Data:     base64.StdEncoding.EncodeToString(data),
		MimeType: mime,
	}}
}

// fileLabel renders the one ordinary-file content block. The path is
// JSON-quoted so a file name containing quotes cannot break the label, and it
// is the only place the checked absolute path is published.
func fileLabel(root, basename string) string {
	quoted, _ := json.Marshal(filepath.Join(root, basename))
	return "Attached file path (JSON-quoted): " + string(quoted)
}

// looksLikeImage mirrors Phi's staging check so a refused image cannot be
// smuggled in as an ordinary file.
func looksLikeImage(data []byte) bool {
	if imageSignature(data, "image/png") || imageSignature(data, "image/jpeg") {
		return true
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return true
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return true
	}
	if len(data) >= 2 && string(data[:2]) == "BM" {
		return true
	}
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		switch string(data[8:12]) {
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1", "avif", "avis":
			return true
		}
	}
	// Phi lowercases the first 1 MiB of UTF-8 and looks for "<svg", which is
	// equivalent to an ASCII case-insensitive search for that prefix.
	prefix := data
	if len(prefix) > 1<<20 {
		prefix = prefix[:1<<20]
	}
	return containsFold(prefix, "<svg")
}

func containsFold(haystack []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			c := haystack[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
