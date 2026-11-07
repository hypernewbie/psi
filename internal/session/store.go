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
	opened           string
	dir              string
	cwd              string
	persist          bool
	flushed          bool
	lock             *sessionLock
	knownSize        int64
	knownInfo        os.FileInfo
	writeErr         error
	sessionInfoCount int
	projection       *Projection // last completed context; written incrementally
	projectionLeaf   string      // leaf that projection represents
	storage          *storageOps
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
		if s.persist && reservedSessionID(opts.ID) {
			return nil, fmt.Errorf("session ID %q cannot be used as a session file name", opts.ID)
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
		s.file = filepath.Join(dir, id+".jsonl")
		s.flushed = false
		if _, err := os.Lstat(s.file); err == nil {
			if opts.ID != "" {
				return nil, fmt.Errorf("session ID %q is already used by %s", opts.ID, s.file)
			}
			return nil, fmt.Errorf("session file already exists: %s", s.file)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	s.rebuildIndex()
	return s, nil
}

// OpenFile opens an explicit session path.
func OpenFile(path, cwd string, persist bool) (*Store, error) {
	opened := path
	if abs, err := filepath.Abs(path); err == nil {
		opened = abs
	}
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
	// Pi loads the whole session once at open (loadEntriesFromFile) and serves
	// every later read from memory. Do the same: full payloads, single pass.
	header, entries, err := loadFileEntries(path)
	if err != nil {
		return nil, err
	}
	records := map[string]bool{}
	sessionInfoCount := 0
	for _, e := range entries {
		if e.SessionInfo != nil {
			sessionInfoCount++
		}
		if _, exists := records[e.ID()]; exists {
			return nil, fmt.Errorf("duplicate entry id %q", e.ID())
		}
		records[e.ID()] = true
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
	s := &Store{header: header, entries: entries, byID: map[string]*Entry{}, file: path, opened: opened, dir: filepath.Dir(path), cwd: cwd, persist: persist, flushed: true, sessionInfoCount: sessionInfoCount}
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

// Entries returns every stored entry. Payloads are materialized in memory
// at open, matching Pi's session manager.
func (s *Store) Entries() []*Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// SessionName returns the latest session_info name, matching Pi's footer.
// It reads indexed metadata only and does not load message payloads.
func (s *Store) SessionName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionInfoCount == 0 {
		return ""
	}
	for i := len(s.entries) - 1; i >= 0; i-- {
		entry := s.entries[i]
		if entry.SessionInfo != nil {
			if entry.SessionInfo.Name == nil {
				return ""
			}
			return strings.TrimSpace(*entry.SessionInfo.Name)
		}
	}
	return ""
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
	return s.pathLocked()
}

// ActiveCustomEntries loads only custom entries of the given type on the active branch.
func (s *Store) ActiveCustomEntries(customType string) ([]*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.pathLocked()
	if err != nil {
		return nil, err
	}
	var selected []*Entry
	for _, entry := range path {
		if entry.Kind == "custom" && entry.Custom != nil && entry.Custom.CustomType == customType {
			selected = append(selected, entry)
		}
	}
	return selected, nil
}

func (s *Store) projectionCacheValidLocked() bool {
	return s.projection != nil && s.projectionLeaf == s.leafID
}

// Projection builds or reuses the provenance-preserving context. Callers get a
// snapshot header: later appends grow the internal cache without changing the
// slices already returned. The entry/message values are shared and read-only.
func (s *Store) Projection() (*Projection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectionCacheValidLocked() {
		return s.projectionViewLocked(), nil
	}
	s.projection = nil
	path, err := s.pathLocked()
	if err != nil {
		return nil, err
	}
	state := RestoreModelState(path)
	ctx := BuildContextEntries(path)
	s.projection = projectEntries(ctx, state)
	s.projectionLeaf = s.leafID
	return s.projectionViewLocked(), nil
}

// ProjectionIfCached returns the current projection only if one is already
// built and fresh. Startup/footer code uses this to avoid paying for decode.
func (s *Store) ProjectionIfCached() *Projection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectionCacheValidLocked() {
		return s.projectionViewLocked()
	}
	return nil
}

func (s *Store) projectionViewLocked() *Projection {
	if s.projection == nil {
		return nil
	}
	view := *s.projection
	return &view
}

// ModelState restores provider/model/thinking from indexed metadata.
func (s *Store) ModelState() (ModelState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectionCacheValidLocked() {
		return s.projection.State, nil
	}
	path, err := s.pathLocked()
	if err != nil {
		return ModelState{}, err
	}
	return RestoreModelState(path), nil
}

// LastContextTokens returns the newest valid assistant usage after the latest
// compaction. It reads indexed metadata only; false means no trustworthy usage.
// A latest-wins null context edit removes its target from the projected context,
// so dropped assistant usage must not leak into the footer estimate.
func (s *Store) LastContextTokens() (int, bool) {
	tokens, _, ok := s.LastContextUsage()
	return tokens, ok
}

// LastContextUsage is LastContextTokens plus the id of the assistant entry
// that reported the usage, so callers can estimate the messages after it.
func (s *Store) LastContextUsage() (int, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.pathLocked()
	if err != nil {
		return 0, "", false
	}
	dropped := map[string]bool{}
	latestCompaction := -1
	for i, e := range path {
		if e.Kind == "context_edit" && e.ContextEdit != nil {
			replacement := e.ContextEdit.Replacement
			dropped[e.ContextEdit.TargetID] = len(replacement) == 0 || string(replacement) == "null"
		}
		if e.Kind == "compaction" {
			latestCompaction = i
		}
	}
	for i := len(path) - 1; i > latestCompaction; i-- {
		e := path[i]
		if e.Kind != "message" || e.Message == nil || e.Message.Message.Assistant == nil {
			continue
		}
		if dropped[e.ID()] {
			continue
		}
		a := e.Message.Message.Assistant
		if a.StopReason == "aborted" || a.StopReason == "error" {
			continue
		}
		if tokens := ContextTokens(a.Usage); tokens > 0 {
			return tokens, e.ID(), true
		}
	}
	return 0, "", false
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

// AppendSessionInfo appends a display-name entry (Pi's /name). The latest
// session_info entry wins; an empty name clears it.
func (s *Store) AppendSessionInfo(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	e := &Entry{Kind: "session_info", Base: entryBase{Type: "session_info", ID: id, ParentID: parent, Timestamp: ts},
		SessionInfo: &SessionInfoEntry{Type: "session_info", ID: id, ParentID: parent, Timestamp: ts, Name: &name}}
	return id, s.appendLocked(e, e.SessionInfo)
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
	path, err := s.pathLocked()
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

// AppendCustomEntry records extension-owned state that does not enter model context.
func (s *Store) AppendCustomEntry(customType string, data any) (string, error) {
	if strings.TrimSpace(customType) == "" {
		return "", fmt.Errorf("custom entry type must not be empty")
	}
	var raw json.RawMessage
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return "", err
		}
		raw = encoded
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	entry := &Entry{Kind: "custom", Base: entryBase{Type: "custom", ID: id, ParentID: parent, Timestamp: ts},
		Custom: &CustomEntry{Type: "custom", ID: id, ParentID: parent, Timestamp: ts, CustomType: customType, Data: raw}}
	return id, s.appendLocked(entry, entry.Custom)
}

// AppendCustomMessage appends a Pi-compatible custom message. A message with
// display=false remains in model context but stays out of visible history.
func (s *Store) AppendCustomMessage(customType, content string, display bool, details any) (string, error) {
	if strings.TrimSpace(customType) == "" {
		return "", fmt.Errorf("custom message type must not be empty")
	}
	var raw json.RawMessage
	if details != nil {
		encoded, err := json.Marshal(details)
		if err != nil {
			return "", err
		}
		raw = encoded
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newEntryID(s.byID)
	parent := parentPtr(s.leafID, len(s.entries))
	ts := nowISO()
	entry := &Entry{Kind: "custom_message", Base: entryBase{Type: "custom_message", ID: id, ParentID: parent, Timestamp: ts},
		CustomMessage: &CustomMessageEntry{Type: "custom_message", ID: id, ParentID: parent, Timestamp: ts, CustomType: customType, Content: content, Details: raw, Display: display}}
	return id, s.appendLocked(entry, entry.CustomMessage)
}

// Branch moves the leaf pointer (new branch without modifying history).
func (s *Store) Branch(branchFromID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[branchFromID]; !ok {
		return fmt.Errorf("entry %s not found", branchFromID)
	}
	s.leafID = branchFromID
	s.projection = nil
	return nil
}

// GetEntry returns an entry by id.
func (s *Store) GetEntry(id string) *Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

// cacheProjectionAppendLocked grows the completed projection by one entry.
// Compaction and context edits change earlier contributions; drop the cache and
// rebuild on next use instead of trying to patch history.
func (s *Store) cacheProjectionAppendLocked(e *Entry, oldLeaf string) {
	if s.projection == nil {
		return
	}
	if s.projectionLeaf != oldLeaf {
		s.projection = nil
		return
	}
	switch e.Kind {
	case "compaction", "context_edit":
		s.projection = nil
		return
	}
	p := s.projection
	p.Entries = append(p.Entries, e)
	msgs := EntryToContextMessages(e)
	p.Messages = append(p.Messages, msgs)
	p.Flat = append(p.Flat, msgs...)
	switch e.Kind {
	case "model_change":
		if e.ModelChange != nil {
			p.State.Provider, p.State.ModelID, p.State.HasModel = e.ModelChange.Provider, e.ModelChange.ModelID, true
		}
	case "thinking_level_change":
		if e.ThinkingLevelChange != nil {
			p.State.ThinkingLevel = e.ThinkingLevelChange.ThinkingLevel
		}
	case "message":
		if e.Message != nil && e.Message.Message.Assistant != nil {
			a := e.Message.Message.Assistant
			if a.Provider != "" && a.Model != "" {
				p.State.Provider, p.State.ModelID, p.State.HasModel = a.Provider, a.Model, true
			}
		}
	}
	s.projectionLeaf = e.ID()
}

// appendLocked validates, persists, and advances the leaf. Caller holds mu.
func (s *Store) appendLocked(e *Entry, payload any) (err error) {
	oldLeaf := s.leafID
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
	// The metadata scan carries the continuation flags. The full entry stays
	// in memory, so only the flags come from the scan.
	thin, err := metadataEntry(data)
	if err != nil {
		return err
	}
	e.Raw = data
	// Keep metadata flags on in-memory entries too; activation checks the same
	// Entry values whether the store is durable or --no-session.
	e.provenanceTarget = thin.provenanceTarget
	e.toolSummaries = thin.toolSummaries
	e.contentIssue = thin.contentIssue
	e.deferred = thin.deferred
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
	if e.SessionInfo != nil {
		s.sessionInfoCount++
	}
	s.leafID = e.ID()
	if st != nil {
		s.knownSize, s.knownInfo = st.Size(), st
	}
	s.cacheProjectionAppendLocked(e, oldLeaf)
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
	return findRecentFast(sessionDir, cwd)
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
//
// A persisted session whose filename is not yet <session_id>.jsonl moves
// to its canonical name while both the source and target locks are held.
// The move never replaces an existing target.
func (s *Store) TryLock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persist {
		return nil
	}
	if s.lock != nil {
		return fmt.Errorf("session already locked")
	}
	if err := AssertValidSessionID(s.header.ID); err != nil {
		return fmt.Errorf("cannot canonicalize session filename: %w", err)
	}
	target := filepath.Join(s.dir, s.header.ID+".jsonl")
	if s.file == target {
		return s.lockOnly()
	}
	// A session opened through a symbolic link keeps its current name:
	// renaming the link target would move the session out of the session
	// directory and leave the link stale. A reserved device name cannot
	// become a file name either, so it keeps its current name as well.
	if s.opened != "" {
		if fi, err := os.Lstat(s.opened); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return s.lockOnly()
		}
	}
	if reservedSessionID(s.header.ID) {
		return s.lockOnly()
	}
	source := s.file
	srcLock, err := acquireLock(source)
	if err != nil {
		return err
	}
	dstLock, err := acquireLock(target)
	if err != nil {
		srcLock.Release()
		return err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			dstLock.Release()
			srcLock.Release()
		}
	}()
	// The source may have been replaced between OpenFile and TryLock.
	// Revalidate identity under both locks before the move.
	st, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("cannot inspect session source %s: %w", source, err)
	}
	if !st.Mode().IsRegular() || s.knownInfo == nil || !os.SameFile(st, s.knownInfo) || st.Size() != s.knownSize || !st.ModTime().Equal(s.knownInfo.ModTime()) {
		return fmt.Errorf("session file changed externally, refusing to rename: %s", source)
	}
	if err := checkSessionLinks(source); err != nil {
		return err
	}
	if err := renameNoReplace(source, target); err != nil {
		s.recoverRenamePath(target)
		return fmt.Errorf("cannot rename session %s to %s: %w", source, target, err)
	}
	if err := s.syncDirDurable(s.dir); err != nil {
		s.recoverRenamePath(target)
		return fmt.Errorf("cannot sync session directory after renaming %s to %s: %w", source, target, err)
	}
	s.file = target
	s.lock = dstLock
	srcLock.Release()
	succeeded = true
	return nil
}

// lockOnly takes the writer lock on the current path without renaming.
func (s *Store) lockOnly() error {
	l, err := acquireLock(s.file)
	if err != nil {
		return err
	}
	s.lock = l
	return nil
}

// recoverRenamePath re-inspects the rename target after a failed move or
// directory sync. The move may have completed despite the error; the
// store follows the target only when it is the known file. Content
// metadata stays untouched so later appends still detect external changes.
func (s *Store) recoverRenamePath(target string) {
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || s.knownInfo == nil || !os.SameFile(info, s.knownInfo) {
		return
	}
	s.file = target
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
