package webaccess

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	metaFieldCap = 8 << 10
	overviewCap  = 64 << 10
)

// storedPage is one ordered fetched page. Text is already redacted and capped
// at the source limit.
type storedPage struct {
	URL       string
	FinalURL  string
	Title     string
	Text      string
	Truncated bool
	Error     string
}

// storedResult is one ordered search result. Content is optional inline page
// text, already redacted.
type storedResult struct {
	Title   string
	URL     string
	Snippet string
	Content string
	Engines string
	Date    string
	Error   string
}

// storedEntry is one retrievable snapshot. All variable-length fields count
// toward the byte budget; fixed object overhead does not.
type storedEntry struct {
	ID         string
	Kind       string
	Pages      []storedPage
	Results    []storedResult
	Overview   string
	Queries    []string
	Incomplete bool
	Created    time.Time
	size       int64
}

func (e *storedEntry) clip() {
	clip := func(s string) string {
		s = censorControl(s)
		return runePrefix(s, metaFieldCap)
	}
	for i := range e.Pages {
		page := &e.Pages[i]
		page.URL = clip(page.URL)
		page.FinalURL = clip(page.FinalURL)
		page.Title = clip(page.Title)
		page.Error = clip(page.Error)
	}
	for i := range e.Results {
		result := &e.Results[i]
		result.Title = clip(result.Title)
		result.URL = clip(result.URL)
		result.Snippet = clip(result.Snippet)
		result.Engines = clip(result.Engines)
		result.Date = clip(result.Date)
		result.Error = clip(result.Error)
	}
	e.Overview = clipOverview(e.Overview)
	for i := range e.Queries {
		e.Queries[i] = clip(e.Queries[i])
	}
	e.size = e.computeSize()
}

func (e *storedEntry) computeSize() int64 {
	var total int64
	add := func(s string) { total += int64(len(s)) }
	for _, page := range e.Pages {
		add(page.URL)
		add(page.FinalURL)
		add(page.Title)
		add(page.Text)
		add(page.Error)
	}
	for _, result := range e.Results {
		add(result.Title)
		add(result.URL)
		add(result.Snippet)
		add(result.Content)
		add(result.Engines)
		add(result.Date)
		add(result.Error)
	}
	add(e.Overview)
	for _, query := range e.Queries {
		add(query)
	}
	return total
}

// store is a process-lifetime LRU bounded by entry count and retained bytes.
type store struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int64
	items      map[string]*list.Element
	order      *list.List // front = most recently used
	total      int64
}

type storeItem struct {
	entry *storedEntry
}

func newStore(maxEntries int, maxBytes int64) *store {
	if maxEntries < 1 {
		maxEntries = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &store{maxEntries: maxEntries, maxBytes: maxBytes, items: map[string]*list.Element{}, order: list.New()}
}

var errIDGeneration = errors.New("cannot generate a response ID")
var errStoreFull = errors.New("content exceeds the memory cache limits")

// Add stores a bounded entry and returns its ID. An entry larger than the byte
// budget is refused before any eviction, so existing entries stay intact.
func (s *store) Add(entry *storedEntry) (string, error) {
	entry.clip()
	if entry.size > s.maxBytes {
		return "", errStoreFull
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Allocate the ID before eviction. An entropy failure must not discard
	// existing entries for an insert that cannot complete.
	var id string
	for attempt := 0; attempt < 4; attempt++ {
		candidate, err := newResponseID()
		if err != nil {
			return "", errIDGeneration
		}
		if _, exists := s.items[candidate]; !exists {
			id = candidate
			break
		}
	}
	if id == "" {
		return "", errIDGeneration
	}

	for s.total+entry.size > s.maxBytes || len(s.items)+1 > s.maxEntries {
		if len(s.items) == 0 {
			return "", errStoreFull
		}
		oldest := s.order.Back()
		if oldest == nil {
			break
		}
		item := oldest.Value.(*storeItem)
		s.total -= item.entry.size
		delete(s.items, item.entry.ID)
		s.order.Remove(oldest)
	}

	entry.ID = id
	if entry.Created.IsZero() {
		entry.Created = time.Now()
	}
	element := s.order.PushFront(&storeItem{entry: entry})
	s.items[id] = element
	s.total += entry.size
	return id, nil
}

// Get returns an entry and marks it most recently used.
func (s *store) Get(id string) (*storedEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.items[id]
	if !ok {
		return nil, false
	}
	s.order.MoveToFront(element)
	return element.Value.(*storeItem).entry, true
}

// Len and Bytes expose accounting for tests.
func (s *store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

func (s *store) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

var newResponseID = func() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// clipOverview keeps the line structure that separates queries and ranked
// results, while censoring other controls and bounding the total.
func clipOverview(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case unicode.IsControl(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return runePrefix(b.String(), overviewCap)
}

// censorControl removes control characters from retained metadata so one line
// of model-facing output cannot be forged by stored values.
func censorControl(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
