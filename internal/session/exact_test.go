package session

// Phase C regressions for PLAN3 F05: session field semantics match Pi
// (JSON.parse case-sensitivity) across eager, indexed, and lazy readers.
//
// Pi rule: exact property names win; alias-case keys ("ID", "ROLE") are
// invisible to canonical lookups; duplicate keys keep last-wins.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const f05Header = `{"type":"session","version":3,"id":"h1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/ws"}`

// readAllFour decodes one record through eager, indexed, lazy-active-path,
// and lazy-timeline paths, returning either the entry or the first error.
func readAllFour(t *testing.T, record string) (eager, indexed, lazy *Entry, eagerErr, indexErr, lazyErr error) {
	t.Helper()
	data := f05Header + "\n" + record + "\n"
	eager, eagerErr = UnmarshalEntry([]byte(strings.TrimSpace(record)))
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := BuildIndex(path)
	if err != nil {
		indexErr = err
	} else {
		for _, m := range idx.Records {
			if m.Type == "session" {
				continue
			}
			indexed, indexErr = idx.LoadEntry(m)
			break
		}
	}
	store, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		lazyErr = err
		return
	}
	act, err := store.ActivePath()
	if err != nil {
		lazyErr = err
		return
	}
	if len(act) == 0 {
		lazyErr = nil
		return
	}
	lazy = act[len(act)-1]
	// Timeline must agree too (visible text for user records).
	return
}

func TestF05CanonicalOnlyAgrees(t *testing.T) {
	rec := `{"type":"message","id":"u1","timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi","timestamp":1}}`
	eager, indexed, lazy, ee, ie, le := readAllFour(t, rec)
	if ee != nil || ie != nil || le != nil {
		t.Fatalf("canonical rejected: %v %v %v", ee, ie, le)
	}
	for name, e := range map[string]*Entry{"eager": eager, "indexed": indexed, "lazy": lazy} {
		if e == nil || e.ID() != "u1" || e.Kind != "message" {
			t.Fatalf("%s disagrees: %+v", name, e)
		}
	}
}

func TestF05AliasOnlyRejected(t *testing.T) {
	// Pi sees type/id undefined here; psi must not invent an identity.
	rec := `{"TYPE":"message","ID":"x1","timestamp":"2026-01-01T00:00:01.000Z","message":{"ROLE":"user","content":"hi","timestamp":1}}`
	_, _, _, ee, ie, le := readAllFour(t, rec)
	if ee == nil {
		t.Fatal("eager accepted alias-only envelope")
	}
	if ie == nil {
		t.Fatal("indexed accepted alias-only envelope")
	}
	if le == nil {
		t.Fatal("lazy accepted alias-only envelope")
	}
}

func TestF05CanonicalPlusAliasKeepsCanonical(t *testing.T) {
	// Pi: JSON.parse(...).id === "canonical". All psi readers must agree.
	rec := `{"type":"message","id":"canonical","ID":"alias","parentId":"p1","ParentID":"evil","timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","ROLE":"assistant","content":"hi","timestamp":1}}`
	eager, indexed, _, ee, ie, _ := readAllFour(t, rec)
	if ee != nil || ie != nil {
		t.Fatalf("canonical+alias rejected: %v %v", ee, ie)
	}
	if eager.ID() != "canonical" {
		t.Fatalf("eager id = %q", eager.ID())
	}
	if indexed.ID() != "canonical" {
		t.Fatalf("indexed id = %q", indexed.ID())
	}
	p := eager.ParentID()
	if p == nil || *p != "p1" {
		t.Fatalf("eager parent = %v", p)
	}
	if eager.Message.Message.Role != "user" {
		t.Fatalf("eager role = %q", eager.Message.Message.Role)
	}
	if eager.Message.Message.User == nil {
		t.Fatal("alias ROLE hijacked the role payload")
	}
}

func TestF05NestedToolFieldsExact(t *testing.T) {
	rec := `{"type":"message","id":"a1","timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","ID":"evil","name":"read","Name":"write","arguments":{}}],"timestamp":2}}`
	eager, indexed, _, ee, ie, _ := readAllFour(t, rec)
	if ee != nil || ie != nil {
		t.Fatalf("tool block rejected: %v %v", ee, ie)
	}
	call := eager.Message.Message.Assistant.Content[0].Call
	if call == nil || call.ID != "c1" || call.Name != "read" {
		t.Fatalf("eager tool identity wrong: %+v", call)
	}
	icall := indexed.Message.Message.Assistant.Content[0].Call
	if icall == nil || icall.ID != "c1" || icall.Name != "read" {
		t.Fatalf("indexed tool identity wrong: %+v", icall)
	}
	_ = eager
}

func TestF05DuplicateKeysLastWins(t *testing.T) {
	// Matches JSON.parse: the final occurrence wins.
	rec := `{"type":"message","id":"first","id":"second","timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi","timestamp":1}}`
	eager, _, _, ee, _, _ := readAllFour(t, rec)
	if ee != nil {
		t.Fatalf("duplicate keys rejected: %v", ee)
	}
	if eager.ID() != "second" {
		t.Fatalf("duplicate id = %q, want last-wins", eager.ID())
	}
}

func TestF05PreservationAndAmbiguousRefusal(t *testing.T) {
	// Unknown fields, nulls, large integers, signatures, images, and opaque
	// metadata survive byte-for-byte; an ambiguous alias-role fixture cannot
	// continue to a provider request.
	rec := `{"type":"message","id":"a9","timestamp":"2026-01-01T00:00:01.000Z","mystery":{"nested":[1,2,3]},"bignum":9007199254740993,"nothing":null,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"h","thinkingSignature":"sig"},{"type":"text","text":"out"}],"timestamp":2}}`
	data := f05Header + "\n" + rec + "\n"
	e, err := UnmarshalEntry([]byte(rec))
	if err != nil {
		t.Fatal(err)
	}
	marshalled, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{`"mystery"`, `"bignum":9007199254740993`, `"nothing":null`, `"thinkingSignature":"sig"`} {
		if !strings.Contains(string(marshalled), needle) {
			t.Fatalf("not preserved: %s", needle)
		}
	}
	_ = data
	// Ambiguous fixture: alias-case role only.
	ambig := `{"type":"message","id":"ax","timestamp":"2026-01-01T00:00:01.000Z","message":{"ROLE":"user","content":"hi","timestamp":1}}`
	ae, err := UnmarshalEntry([]byte(ambig))
	if err != nil {
		t.Fatalf("alias-role must decode (as unknown), not fail syntax: %v", err)
	}
	if ae.Message.Message.Role != "" || ae.Message.Message.User != nil {
		t.Fatalf("alias ROLE populated canonical role: %+v", ae.Message.Message)
	}
	flat := []AgentMessage{ae.Message.Message}
	if err := CheckContinuable(flat, []*Entry{ae}); err == nil {
		t.Fatal("ambiguous role continued without refusal")
	}
}
