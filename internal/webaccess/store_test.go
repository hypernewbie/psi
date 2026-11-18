package webaccess

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func entryWith(text string) *storedEntry {
	return &storedEntry{Kind: "fetch", Pages: []storedPage{{URL: "https://example.com/", FinalURL: "https://example.com/", Title: "T", Text: text}}}
}

func TestStoreLRUAndByteBudget(t *testing.T) {
	s := newStore(2, 1024)
	first, err := s.Add(entryWith(strings.Repeat("a", 100)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Add(entryWith(strings.Repeat("b", 100)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(first); !ok {
		t.Fatal("first entry missing")
	}
	// Reading the first entry makes the second the least recently used.
	third, err := s.Add(entryWith(strings.Repeat("c", 100)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(second); ok {
		t.Fatal("least recently used entry survived")
	}
	for _, id := range []string{first, third} {
		if _, ok := s.Get(id); !ok {
			t.Fatalf("entry %s missing", id)
		}
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d", s.Len())
	}
}

func TestStoreOversizedEntryLeavesExisting(t *testing.T) {
	s := newStore(4, 256)
	keep, err := s.Add(entryWith("keep"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(entryWith(strings.Repeat("x", 300))); !errors.Is(err, errStoreFull) {
		t.Fatalf("err = %v, want full", err)
	}
	if _, ok := s.Get(keep); !ok {
		t.Fatal("oversized insert evicted existing content")
	}
}

func TestStoreByteEviction(t *testing.T) {
	s := newStore(10, 300)
	first, _ := s.Add(entryWith(strings.Repeat("a", 60)))
	second, _ := s.Add(entryWith(strings.Repeat("b", 60)))
	if s.Bytes() > 300 {
		t.Fatalf("bytes = %d", s.Bytes())
	}
	if _, ok := s.Get(first); !ok {
		t.Fatal("first entry should survive within budget")
	}
	if _, ok := s.Get(second); !ok {
		t.Fatal("second entry should survive within budget")
	}
	third, err := s.Add(entryWith(strings.Repeat("c", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if s.Bytes() > 300 || s.Len() > 2 {
		t.Fatalf("budget exceeded: len=%d bytes=%d", s.Len(), s.Bytes())
	}
	if _, ok := s.Get(third); !ok {
		t.Fatal("new entry missing")
	}
}

func TestStoreUnknownLookupDoesNotTouchLRU(t *testing.T) {
	s := newStore(2, 1024)
	first, _ := s.Add(entryWith("a"))
	_, _ = s.Add(entryWith("b"))
	s.Get("missing")
	_, _ = s.Add(entryWith("c"))
	if _, ok := s.Get(first); ok {
		t.Fatal("unknown lookup changed LRU order")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	s := newStore(8, 1<<20)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				id, err := s.Add(entryWith(strings.Repeat("x", n+1)))
				if err != nil {
					continue
				}
				s.Get(id)
				s.Get("unknown")
				_ = s.Bytes()
				_ = s.Len()
			}
		}(worker)
	}
	wg.Wait()
	if s.Bytes() > 1<<20 {
		t.Fatalf("bytes = %d", s.Bytes())
	}
}

func TestStoreIDGenerationFailure(t *testing.T) {
	original := newResponseID
	newResponseID = func() (string, error) { return "", errors.New("entropy") }
	t.Cleanup(func() { newResponseID = original })
	s := newStore(1, 1024)
	if _, err := s.Add(entryWith("x")); !errors.Is(err, errIDGeneration) {
		t.Fatalf("err = %v", err)
	}
}

func TestStoredMetadataIsCapped(t *testing.T) {
	entry := entryWith("body")
	entry.Pages[0].Title = strings.Repeat("t", metaFieldCap*2) + "\nforged: line\u0085end"
	entry.Queries = []string{strings.Repeat("q", metaFieldCap*2)}
	entry.clip()
	if len(entry.Pages[0].Title) > metaFieldCap {
		t.Fatalf("title len = %d", len(entry.Pages[0].Title))
	}
	if strings.ContainsAny(entry.Pages[0].Title, "\n\u0085") {
		t.Fatal("metadata control character survived")
	}
}

func TestStoredOverviewKeepsLineStructure(t *testing.T) {
	s := newStore(2, 1<<20)
	overview := "Search results for: q\nprovider: searxng\n\n1. A\n   https://a.example/\nfake: \x00\x07 line\n"
	id, err := s.Add(&storedEntry{Kind: "search", Overview: overview})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := s.Get(id)
	if !ok {
		t.Fatal("entry missing")
	}
	want := "Search results for: q\nprovider: searxng\n\n1. A\n   https://a.example/\nfake:    line\n"
	if entry.Overview != want {
		t.Fatalf("overview = %q", entry.Overview)
	}
}

func TestStoreIDFailurePreservesExistingEntries(t *testing.T) {
	s := newStore(1, 1<<20)
	first, err := s.Add(entryWith("keep"))
	if err != nil {
		t.Fatal(err)
	}
	original := newResponseID
	newResponseID = func() (string, error) { return "", errors.New("entropy") }
	t.Cleanup(func() { newResponseID = original })
	if _, err := s.Add(entryWith("new")); !errors.Is(err, errIDGeneration) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := s.Get(first); !ok {
		t.Fatal("failed insert evicted the existing entry")
	}
}
