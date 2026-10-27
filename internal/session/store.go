package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store owns mutation of one active session. One goroutine owns mutation;
// provider/process goroutines report results to that owner (enforced by mutex + settled checks).
type Store struct {
	mu        sync.Mutex
	header    *SessionHeader
	entries   []*Entry
	byID      map[string]*Entry
	leafID    string
	file      string
	dir       string
	cwd       string
	persist   bool
	flushed   bool
	lock      *sessionLock
	knownSize int64
	knownInfo os.FileInfo
}

// OpenOptions controls Store creation.
type OpenOptions struct {
	Cwd         string
	SessionDir  string // resolved session dir (empty = default per cwd)
	SessionFile string // explicit --session path
	AgentDir    string
	Persist     bool // false = in-memory (--no-session)
	ID          string
}

// New creates a new session (persisted unless Persist=false).
func New(opts OpenOptions) (*Store, error) {
	if opts.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.Cwd = wd
	}
	agentDir := opts.AgentDir
	if agentDir == "" {
		agentDir = AgentDir()
	}
	dir := opts.SessionDir
	if dir == "" {
		dir = DefaultSessionDir(opts.Cwd, agentDir)
	}
	s := &Store{byID: map[string]*Entry{}, dir: dir, cwd: opts.Cwd, persist: opts.Persist}
	if opts.ID != "" {
		if err := AssertValidSessionID(opts.ID); err != nil {
			return nil, err
		}
	}
	id := opts.ID
	if id == "" {
		id = newSessionID()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.header = &SessionHeader{Type: "session", Version: intPtr(CurrentSessionVersion), ID: id, Timestamp: now, Cwd: opts.Cwd}
	if s.persist {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		ts := strings.ReplaceAll(strings.ReplaceAll(now, ":", "-"), ".", "-")
		s.file = filepath.Join(dir, fmt.Sprintf("%s_%s.jsonl", ts, id))
		s.flushed = false
	}
	s.rebuildIndex()
	return s, nil
}

// OpenFile opens an explicit session path.
func OpenFile(path, cwd string, persist bool) (*Store, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	header, entries, _, err := ReadFileEntries(path)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, fmt.Errorf("session file has no valid header; refusing to initialize an explicit existing file: %s", path)
	}
	if err := checkVersion(header, path); err != nil {
		return nil, err
	}
	if incomplete, err := HasIncompleteTail(path); err != nil {
		return nil, err
	} else if incomplete {
		return nil, fmt.Errorf("session file has truncated final record, refusing to append: %s (recover from a copy)", path)
	}
	s := &Store{header: header, entries: entries, byID: map[string]*Entry{}, file: path, dir: filepath.Dir(path), cwd: cwd, persist: persist, flushed: true}
	s.rebuildIndex()
	s.knownInfo, err = os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, s.knownInfo) || before.Size() != s.knownInfo.Size() || !before.ModTime().Equal(s.knownInfo.ModTime()) {
		return nil, fmt.Errorf("session file changed while reading: %s", path)
	}
	s.knownSize = s.knownInfo.Size()
	if _, err := BuildPath(entries, ""); err != nil {
		return nil, err
	}
	// leaf = last entry (pinned default leaf selection on reopen).
	if len(entries) > 0 {
		s.leafID = entries[len(entries)-1].ID()
	}
	return s, nil
}

// Continue opens the most recent session for cwd.
func Continue(cwd, sessionDir, agentDir string, persist bool) (*Store, error) {
	dir := sessionDir
	if dir == "" {
		dir = DefaultSessionDir(cwd, agentDir)
	}
	latest, err := FindMostRecent(dir, cwd)
	if err != nil {
		return nil, err
	}
	if latest == "" {
		return nil, fmt.Errorf("no sessions found in %s", dir)
	}
	return OpenFile(latest, cwd, persist)
}

func checkVersion(h *SessionHeader, path string) error {
	if h.Version == nil {
		return fmt.Errorf("%s: v1 sessions are not supported (use Pi migration on a copy)", path)
	}
	switch *h.Version {
	case CurrentSessionVersion:
		return nil
	case 1, 2:
		return fmt.Errorf("%s: session version %d is not supported (no automatic migration)", path, *h.Version)
	default:
		return fmt.Errorf("%s: unsupported session version %d: refuse lossy continuation", path, *h.Version)
	}
}

func (s *Store) rebuildIndex() {
	s.byID = make(map[string]*Entry, len(s.entries))
	for _, e := range s.entries {
		s.byID[e.ID()] = e
	}
	// labels resolved on demand; leaf tracked separately
}

// Header returns the session header.
func (s *Store) Header() *SessionHeader { return s.header }

// Entries returns all non-header entries (shallow copy).
func (s *Store) Entries() []*Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Entry(nil), s.entries...)
}

// LeafID returns the current leaf.
func (s *Store) LeafID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafID
}

// File returns the session file (empty for in-memory).
func (s *Store) File() string { return s.file }

// Dir returns the session directory.
func (s *Store) Dir() string { return s.dir }

// Cwd returns the workspace cwd.
func (s *Store) Cwd() string { return s.cwd }

// Persistent reports the storage policy, including ephemeral copies of real files.
func (s *Store) Persistent() bool { return s.persist }

// ActivePath returns the chronological path from root to leaf.
func (s *Store) ActivePath() ([]*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return BuildPath(s.entries, s.leafID)
}

// Projection builds the provenance-preserving context.
func (s *Store) Projection() (*Projection, error) {
	path, err := s.ActivePath()
	if err != nil {
		return nil, err
	}
	return BuildProjection(path), nil
}

// AppendMessage appends a message as a child of the leaf.
func (s *Store) AppendMessage(msg AgentMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	e := &Entry{Kind: "message", Base: entryBase{Type: "message", ID: id, ParentID: parent, Timestamp: nowISO()},
		Message: &SessionMessageEntry{Type: "message", ID: id, ParentID: parent, Timestamp: nowISO(), Message: msg}}
	// Ensure consistent timestamps between base and payload.
	e.Base.Timestamp = e.Message.Timestamp
	if err := s.appendLocked(e, e.Message); err != nil {
		return "", err
	}
	return id, nil
}

// AppendThinkingLevelChange appends a thinking-level change.
func (s *Store) AppendThinkingLevelChange(level string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	e := &Entry{Kind: "thinking_level_change", Base: entryBase{Type: "thinking_level_change", ID: id, ParentID: parent, Timestamp: ts},
		ThinkingLevelChange: &ThinkingLevelChangeEntry{Type: "thinking_level_change", ID: id, ParentID: parent, Timestamp: ts, ThinkingLevel: level}}
	return id, s.appendLocked(e, e.ThinkingLevelChange)
}

// AppendModelChange appends a model change.
func (s *Store) AppendModelChange(provider, modelID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	e := &Entry{Kind: "model_change", Base: entryBase{Type: "model_change", ID: id, ParentID: parent, Timestamp: ts},
		ModelChange: &ModelChangeEntry{Type: "model_change", ID: id, ParentID: parent, Timestamp: ts, Provider: provider, ModelID: modelID}}
	return id, s.appendLocked(e, e.ModelChange)
}

// AppendUsage appends model-attributed usage (no LLM context).
func (s *Store) AppendUsage(kind, provider, model string, u Usage, note string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	var notePtr *string
	if note != "" {
		notePtr = &note
	}
	e := &Entry{Kind: "usage", Base: entryBase{Type: "usage", ID: id, ParentID: parent, Timestamp: ts},
		Usage: &UsageEntry{Type: "usage", ID: id, ParentID: parent, Timestamp: ts, Kind2: kind, Provider: provider, Model: model, Usage: u, Note: notePtr}}
	return id, s.appendLocked(e, e.Usage)
}

// AppendCompaction appends a compaction summary.
func (s *Store) AppendCompaction(summary, firstKeptID string, tokensBefore float64, system *SystemMessage) (string, error) {
	return s.AppendCompactionWithUsage(summary, firstKeptID, tokensBefore, system, nil)
}

func (s *Store) AppendCompactionWithUsage(summary, firstKeptID string, tokensBefore float64, system *SystemMessage, usage *Usage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	if firstKeptID == "" {
		firstKeptID = id // retain-none
	}
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	e := &Entry{Kind: "compaction", Base: entryBase{Type: "compaction", ID: id, ParentID: parent, Timestamp: ts},
		Compaction: &CompactionEntry{Type: "compaction", ID: id, ParentID: parent, Timestamp: ts, Summary: summary, FirstKeptEntryID: firstKeptID, TokensBefore: tokensBefore, SystemMessage: system, Usage: usage}}
	return id, s.appendLocked(e, e.Compaction)
}

// AppendContextEdit appends a branch-local edit to an earlier entry.
func (s *Store) AppendContextEdit(targetID string, replacement json.RawMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(replacement) != 0 && string(replacement) != "null" {
		var rep struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(replacement, &rep); err != nil || len(rep.Content) == 0 {
			return "", fmt.Errorf("context edit replacement must be null or contain content")
		}
	}
	target, ok := s.byID[targetID]
	if !ok {
		return "", fmt.Errorf("entry %s not found", targetID)
	}
	// Must be on the active branch.
	path, err := BuildPath(s.entries, s.leafID)
	if err != nil {
		return "", err
	}
	onBranch := false
	for _, e := range path {
		if e.ID() == targetID {
			onBranch = true
			break
		}
	}
	if !onBranch {
		return "", fmt.Errorf("entry %s is not on the active branch", targetID)
	}
	editable := target.Kind == "custom_message" ||
		(target.Kind == "message" && (target.Message.Message.User != nil || target.Message.Message.Assistant != nil || target.Message.Message.ToolResult != nil))
	if !editable {
		return "", fmt.Errorf("entry %s does not contribute editable model content", targetID)
	}
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	// Normalize string replacements for assistant/toolResult (Pi parity).
	role := ""
	if target.Kind == "message" {
		role = target.Message.Message.Role
	} else {
		role = "custom"
	}
	norm := replacement
	if len(replacement) != 0 && string(replacement) != "null" && (role == "assistant" || role == "toolResult") {
		var rep struct {
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(replacement, &rep)
		var asString string
		if json.Unmarshal(rep.Content, &asString) == nil {
			arr, _ := json.Marshal(map[string]json.RawMessage{"content": mustMarshal([]map[string]string{{"type": "text", "text": asString}})})
			norm = arr
		}
	}
	e := &Entry{Kind: "context_edit", Base: entryBase{Type: "context_edit", ID: id, ParentID: parent, Timestamp: ts},
		ContextEdit: &ContextEditEntry{Type: "context_edit", ID: id, ParentID: parent, Timestamp: ts, TargetID: targetID, Replacement: norm}}
	return id, s.appendLocked(e, e.ContextEdit)
}

// AppendProvenance records psi-owned input provenance (P2-043).
// It uses type "custom" (state-only, no model context) with customType "psi.input".
// Pi's sessionEntryToContextMessages ignores "custom" entries, so model context is unchanged.
func (s *Store) AppendProvenance(targetID, invocation, source string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	det, _ := json.Marshal(map[string]string{"targetId": targetID, "invocation": invocation, "source": source})
	e := &Entry{Kind: "custom", Base: entryBase{Type: "custom", ID: id, ParentID: parent, Timestamp: ts},
		Custom: &CustomEntry{Type: "custom", ID: id, ParentID: parent, Timestamp: ts, CustomType: "psi.input", Data: det}}
	return id, s.appendLocked(e, e.Custom)
}

// Branch moves the leaf pointer (new branch without modifying history).
func (s *Store) Branch(branchFromID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[branchFromID]; !ok {
		return fmt.Errorf("entry %s not found", branchFromID)
	}
	s.leafID = branchFromID
	return nil
}

// GetEntry returns an entry by id.
func (s *Store) GetEntry(id string) *Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

// appendLocked validates, persists, and advances the leaf. Caller holds mu.
func (s *Store) appendLocked(e *Entry, payload any) error {
	if s.persist && s.file != "" && s.flushed {
		st, err := os.Stat(s.file)
		if err != nil {
			return fmt.Errorf("cannot inspect active session: %w", err)
		}
		if st.Size() != s.knownSize || (s.knownInfo != nil && (!os.SameFile(st, s.knownInfo) || !st.ModTime().Equal(s.knownInfo.ModTime()))) {
			return fmt.Errorf("session file changed externally, refusing to append: %s", s.file)
		}
		if incomplete, err := HasIncompleteTail(s.file); err != nil {
			return err
		} else if incomplete {
			return fmt.Errorf("session file has truncated final record, refusing to append: %s", s.file)
		}
	}
	// Validate duplicate (should not happen with generated ids).
	if _, dup := s.byID[e.ID()]; dup {
		return fmt.Errorf("duplicate entry id %q", e.ID())
	}
	// Validate parent chain: parent must exist unless this is a root.
	if e.ParentID() != nil && *e.ParentID() != "" {
		if _, ok := s.byID[*e.ParentID()]; !ok {
			return fmt.Errorf("missing parent %q for entry %q", *e.ParentID(), e.ID())
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > MaxRecordBytes {
		return fmt.Errorf("record exceeds %d bytes", MaxRecordBytes)
	}
	e.Raw = data
	if s.persist && s.file != "" {
		if !s.flushed {
			// First durable message boundary: write header + all entries atomically via O_EXCL.
			hasConversation := false
			for _, ex := range s.entries {
				if ex.Kind == "message" {
					r := ex.Message.Message.Role
					if r == "user" || r == "assistant" {
						hasConversation = true
						break
					}
				}
			}
			if !hasConversation && e.Kind == "message" {
				r := e.Message.Message.Role
				if r == "user" || r == "assistant" {
					hasConversation = true
				}
			}
			if hasConversation {
				if err := s.rewriteFileWith(e, data); err != nil {
					return err
				}
				s.flushed = true
			}
			// Setup-only entries stay in memory (no file) until first user/assistant message.
		} else {
			f, err := os.OpenFile(s.file, os.O_RDWR|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("append failed (stopping further model/tool actions): %w", err)
			}
			record := append(append([]byte(nil), data...), '\n')
			if s.knownSize > 0 {
				var last [1]byte
				if _, err := f.ReadAt(last[:], s.knownSize-1); err != nil {
					f.Close()
					return err
				}
				if last[0] != '\n' {
					record = append([]byte{'\n'}, record...)
				}
			}
			_, writeErr := f.Write(record)
			if writeErr == nil {
				writeErr = f.Sync()
			}
			closeErr := f.Close()
			if writeErr != nil {
				return fmt.Errorf("append failed (stopping further model/tool actions): %w", writeErr)
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	s.entries = append(s.entries, e)
	s.byID[e.ID()] = e
	s.leafID = e.ID()
	if s.persist && s.file != "" {
		if st, err := os.Stat(s.file); err == nil {
			s.knownSize = st.Size()
			s.knownInfo = st
		}
	}
	return nil
}

func (s *Store) rewriteFileWith(extra *Entry, extraData []byte) error {
	if !s.persist || s.file == "" {
		return nil
	}
	// O_EXCL: never clobber an existing session file on first flush.
	f, err := os.OpenFile(s.file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("session file already exists, refusing to overwrite: %s: %w", s.file, err)
	}
	hdr, err := json.Marshal(s.header)
	if err != nil {
		f.Close()
		return err
	}
	buf := append(append(hdr, '\n'), func() []byte {
		var b []byte
		for _, e := range s.entries {
			b = append(b, e.Raw...)
			b = append(b, '\n')
		}
		return b
	}()...)
	buf = append(buf, extraData...)
	buf = append(buf, '\n')
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return fmt.Errorf("append failed (stopping further model/tool actions): %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SessionInfo summarizes a session for discovery.
type SessionInfo struct {
	Path         string
	ID           string
	Cwd          string
	Created      time.Time
	Modified     time.Time
	MessageCount int
	FirstMessage string
}

// Discover enumerates sessions for a workspace dir without loading full transcripts.
func Discover(sessionDir string) ([]SessionInfo, error) {
	dirents, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, d := range dirents {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			continue
		}
		files = append(files, filepath.Join(sessionDir, d.Name()))
	}
	sort.Strings(files)
	var out []SessionInfo
	for _, f := range files {
		info, err := InspectSession(f)
		if err != nil {
			continue // skip unreadable with diagnostic elsewhere
		}
		if info != nil {
			out = append(out, *info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// InspectSession reads bounded metadata (header + message counts + first user text).
func InspectSession(path string) (*SessionInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 256*1024*1024 {
		return nil, fmt.Errorf("session exceeds discovery bounds")
	}
	br := bufio.NewReader(f)
	info := &SessionInfo{Path: path, Modified: st.ModTime(), FirstMessage: "(no messages)"}
	// Hold only one record at a time; discovery must not load an entire tree.
	for {
		line, readErr := readRecord(br)
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if len(strings.TrimSpace(string(line))) > 0 {
			var meta struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Cwd       string `json:"cwd"`
				Timestamp string `json:"timestamp"`
				Message   struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(line, &meta); err != nil {
				return nil, err
			}
			if info.ID == "" {
				if meta.Type != "session" || meta.ID == "" {
					return nil, fmt.Errorf("missing session header")
				}
				info.ID, info.Cwd = meta.ID, meta.Cwd
				info.Created, _ = time.Parse(time.RFC3339Nano, meta.Timestamp)
			} else if meta.Type == "message" {
				info.MessageCount++
				if info.FirstMessage == "(no messages)" && meta.Message.Role == "user" {
					var content any
					if json.Unmarshal(meta.Message.Content, &content) == nil {
						blocks, _ := UserBlocks(content)
						for _, b := range blocks {
							if b.Text != nil {
								info.FirstMessage = b.Text.Text
								break
							}
						}
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if info.ID == "" {
		return nil, fmt.Errorf("missing session header")
	}
	return info, nil
}

func userText(u *UserMessage) string {
	switch c := u.Content.(type) {
	case string:
		return c
	case []any:
		for _, b := range c {
			if m, ok := b.(map[string]any); ok {
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok {
						return t
					}
				}
			}
		}
	}
	// Typed decode fallback via raw.
	if len(u.Raw) > 0 {
		var probe struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		// Content may be string; ignore errors.
		_ = json.Unmarshal(u.Raw, &probe)
		for _, b := range probe.Content {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}

// FindMostRecent returns the most recently modified session file matching cwd ("" = any).
func FindMostRecent(sessionDir, cwd string) (string, error) {
	infos, err := Discover(sessionDir)
	if err != nil {
		return "", err
	}
	for _, info := range infos {
		if cwd == "" || info.Cwd == cwd {
			return info.Path, nil
		}
	}
	return "", nil
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	// RFC 9562 UUIDv7: 48-bit Unix millisecond time, version, variant, randomness.
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

func newEntryID(byID map[string]*Entry) string {
	for i := 0; i < 100; i++ {
		var b [4]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		if _, dup := byID[id]; !dup {
			return id
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func parentPtr(leafID string, n int) *string {
	if n == 0 || leafID == "" {
		return nil
	}
	cp := leafID
	return &cp
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func intPtr(v int) *int { return &v }

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// TryLock acquires the advisory psi writer lock. Non-blocking: second psi
// process gets an error instead of waiting.
func (s *Store) TryLock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persist {
		return nil
	}
	if s.lock != nil {
		return fmt.Errorf("session already locked")
	}
	l, err := acquireLock(s.file)
	if err != nil {
		return err
	}
	s.lock = l
	return nil
}

// Unlock releases the advisory lock.
func (s *Store) Unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		s.lock.Release()
		s.lock = nil
	}
}

// AssertValidSessionId matches Pi: alnum start/end, interior [A-Za-z0-9._-].
func AssertValidSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session id must be non-empty")
	}
	isAlnum := func(c byte) bool {
		return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
	}
	if !isAlnum(id[0]) || !isAlnum(id[len(id)-1]) {
		return fmt.Errorf("session id must start and end with an alphanumeric character")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if isAlnum(c) || c == '.' || c == '_' || c == '-' {
			continue
		}
		return fmt.Errorf("invalid character %q in session id", c)
	}
	return nil
}
