package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	mu               sync.Mutex
	header           *SessionHeader
	entries          []*Entry
	byID             map[string]*Entry
	leafID           string
	file             string
	dir              string
	cwd              string
	persist          bool
	flushed          bool
	lock             *sessionLock
	knownSize        int64
	knownInfo        os.FileInfo
	writeErr         error
	sessionInfoCount int
	records          map[string]RecordMeta // disk-backed payloads; entries contains small metadata only
	storage          *storageOps
	// LoadHook fires before each lazy payload read (nil in production).
	// Deterministic fault schedules use it to rewrite the session mid-walk.
	LoadHook func(RecordMeta)
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
		dir, err := canonicalSessionPath(dir)
		if err != nil {
			return nil, err
		}
		s.dir = dir
		ts := strings.ReplaceAll(strings.ReplaceAll(now, ":", "-"), ".", "-")
		s.file = filepath.Join(dir, fmt.Sprintf("%s_%s.jsonl", ts, id))
		s.flushed = false
	}
	s.rebuildIndex()
	return s, nil
}

// OpenFile opens an explicit session path.
func OpenFile(path, cwd string, persist bool) (*Store, error) {
	if persist {
		resolved, err := canonicalSessionPath(path)
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("session must be a regular file: %s", path)
	}
	if persist {
		if err := checkSessionLinks(path); err != nil {
			return nil, err
		}
	}
	idx, err := BuildIndex(path)
	if err != nil {
		return nil, err
	}
	header := idx.Header
	var entries []*Entry
	records := map[string]RecordMeta{}
	sessionInfoCount := 0
	for _, m := range idx.Records {
		if m.Type == "session" {
			continue
		}
		entries = append(entries, m.entry)
		if m.entry.SessionInfo != nil {
			sessionInfoCount++
		}
		if _, exists := records[m.ID]; exists {
			return nil, fmt.Errorf("duplicate entry id %q", m.ID)
		}
		records[m.ID] = m
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
	s := &Store{header: header, entries: entries, records: records, byID: map[string]*Entry{}, file: path, dir: filepath.Dir(path), cwd: cwd, persist: persist, flushed: true, sessionInfoCount: sessionInfoCount}
	s.rebuildIndex()
	s.knownInfo, err = os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, s.knownInfo) || before.Size() != s.knownInfo.Size() || !before.ModTime().Equal(s.knownInfo.ModTime()) {
		return nil, fmt.Errorf("session file changed while reading: %s", path)
	}
	s.knownSize = s.knownInfo.Size()
	if err := validateGraph(entries); err != nil {
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

// Entries materializes every record for diagnostics and tests. A load error
// returns nil. Runtime paths use Projection, Timeline, and MetadataPath instead.
func (s *Store) Entries() []*Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := s.loadEntriesLocked(s.entries)
	return entries
}

// SessionName returns the latest session_info name on the active path. It
// walks only indexed metadata and does not load message payloads.
func (s *Store) SessionName() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionInfoCount == 0 {
		return "", nil
	}
	id := s.leafID
	for steps := 0; id != ""; steps++ {
		if steps >= len(s.byID) {
			return "", fmt.Errorf("session parent cycle while finding session name")
		}
		entry, ok := s.byID[id]
		if !ok {
			return "", fmt.Errorf("session parent %q not found while finding session name", id)
		}
		if entry.SessionInfo != nil {
			if entry.SessionInfo.Name == nil {
				return "", nil
			}
			return *entry.SessionInfo.Name, nil
		}
		parent := entry.ParentID()
		if parent == nil {
			return "", nil
		}
		id = *parent
	}
	return "", nil
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

// WriteError is sticky for this activation. Reopen only after checking the file.
func (s *Store) WriteError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeErr
}

// ActivePath returns the chronological path from root to leaf.
func (s *Store) ActivePath() ([]*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := BuildPath(s.entries, s.leafID)
	if err != nil {
		return nil, err
	}
	return s.loadEntriesLocked(path)
}

// Projection builds the provenance-preserving context.
func (s *Store) Projection() (*Projection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := BuildPath(s.entries, s.leafID)
	if err != nil {
		return nil, err
	}
	state := RestoreModelState(path)
	path, err = s.loadEntriesLocked(BuildContextEntries(path))
	if err != nil {
		return nil, err
	}
	return projectEntries(path, state), nil
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
	loaded, err := s.loadEntriesLocked([]*Entry{target})
	if err != nil {
		return "", err
	}
	target = loaded[0]
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
	e := s.byID[id]
	if e == nil {
		return nil
	}
	loaded, err := s.loadEntriesLocked([]*Entry{e})
	if err != nil {
		return nil
	}
	return loaded[0]
}

// appendLocked validates, persists, and advances the leaf. Caller holds mu.
func (s *Store) appendLocked(e *Entry, payload any) (err error) {
	if s.writeErr != nil {
		return fmt.Errorf("storage blocked: %w", s.writeErr)
	}
	defer func() {
		if err != nil && s.persist {
			s.writeErr = err
		}
	}()
	if s.persist && s.file != "" && s.flushed {
		st, err := s.statDurable()
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
	thin, err := metadataEntry(data)
	if err != nil {
		return err
	}
	e.Raw = data
	expectedSize := int64(-1)
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
			f, err := s.openDurable(s.file, os.O_RDWR|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("append failed (stopping further model/tool actions): %w", err)
			}
			opened, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}
			if err := checkSessionLinks(s.file); err != nil {
				f.Close()
				return err
			}
			if opened.Size() != s.knownSize || s.knownInfo != nil && (!os.SameFile(opened, s.knownInfo) || !opened.ModTime().Equal(s.knownInfo.ModTime())) {
				f.Close()
				return fmt.Errorf("session file changed before append: %s", s.file)
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
			expectedSize = s.knownSize + int64(len(record))
			if err := writeDurable(f, record); err != nil {
				return fmt.Errorf("append failed (stopping further model/tool actions): %w", err)
			}
		}
	}
	var st os.FileInfo
	if s.persist && s.file != "" && s.flushed {
		st, err = s.statDurable()
		if err != nil {
			return fmt.Errorf("cannot inspect durable append: %w", err)
		}
		if expectedSize >= 0 && (st.Size() != expectedSize || s.knownInfo != nil && !os.SameFile(st, s.knownInfo)) {
			return fmt.Errorf("session file changed during append: %s", s.file)
		}
	}
	s.entries = append(s.entries, e)
	s.byID[e.ID()] = e
	if thin.SessionInfo != nil {
		s.sessionInfoCount++
	}
	s.leafID = e.ID()
	if st != nil {
		if s.records == nil {
			// Initial flush contains only buffered setup and the first message.
			s.records = map[string]RecordMeta{}
			hdr, _ := json.Marshal(s.header)
			offset := int64(len(hdr) + 1)
			for i, entry := range s.entries {
				thin, err := metadataEntry(entry.Raw)
				if err != nil {
					return err
				}
				parent := ""
				if entry.ParentID() != nil {
					parent = *entry.ParentID()
				}
				s.records[entry.ID()] = RecordMeta{Offset: offset, Length: int64(len(entry.Raw) + 1), Type: entry.Kind, ID: entry.ID(), ParentID: parent, Hash: recordHash(entry.Raw), entry: thin}
				offset += int64(len(entry.Raw) + 1)
				s.entries[i], s.byID[entry.ID()] = thin, thin
			}
		} else {
			parent := ""
			if e.ParentID() != nil {
				parent = *e.ParentID()
			}
			s.records[e.ID()] = RecordMeta{Offset: st.Size() - int64(len(data)+1), Length: int64(len(data) + 1), Type: e.Kind, ID: e.ID(), ParentID: parent, Hash: recordHash(data), entry: thin}
			s.entries[len(s.entries)-1], s.byID[e.ID()] = thin, thin
		}
		s.knownSize, s.knownInfo = st.Size(), st
	}
	return nil
}

func (s *Store) rewriteFileWith(extra *Entry, extraData []byte) error {
	if !s.persist || s.file == "" {
		return nil
	}
	// O_EXCL: never clobber an existing session file on first flush.
	f, err := s.openDurable(s.file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("session file already exists, refusing to overwrite: %s: %w", s.file, err)
		}
		return fmt.Errorf("cannot create session file %s: %w", s.file, err)
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
	if err := writeDurable(f, buf); err != nil {
		return fmt.Errorf("append failed (stopping further model/tool actions): %w", err)
	}
	// A synced new file also needs a durable directory entry.
	return syncSessionDir(filepath.Dir(s.file))
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
	Name         string
	Unavailable  string
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Modified.Equal(out[j].Modified) {
			return out[i].Path < out[j].Path
		}
		return out[i].Modified.After(out[j].Modified)
	})
	return out, nil
}

// InspectSession reads bounded metadata (header + message counts + first user text).
func InspectSession(path string) (*SessionInfo, error) {
	return inspectSessionMetadata(path)
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
