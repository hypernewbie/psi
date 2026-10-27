package session

import (
	"bytes"
	"testing"
)

func FuzzDecodeEntries(f *testing.F) {
	f.Add([]byte(`{"type":"session","version":3,"id":"fuzz","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp"}` + "\n"))
	f.Add([]byte("not json\n"))
	f.Add([]byte("{\"type\":\"message\","))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			return
		}
		_, entries, _, err := DecodeEntries(bytes.NewReader(data), "fuzz")
		if err != nil {
			return
		}
		path, err := BuildPath(entries, "")
		if err != nil {
			return
		}
		projection := BuildProjection(path)
		_ = CheckContinuable(projection.Flat, projection.Entries)
	})
}

func FuzzIndexedMetadata(f *testing.F) {
	f.Add([]byte(`{"type":"session","version":3,"id":"fuzz","cwd":"/synthetic/work"}` + "\n" + `{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"synthetic"}}` + "\n"))
	f.Add([]byte(`{"type":"custom","id":"x","data":["opaque"]}`))
	f.Add([]byte("not json\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			return
		}
		idx, err := BuildIndexFrom(bytes.NewReader(data), "synthetic-fuzz.jsonl")
		if err != nil || idx.Header == nil {
			return
		}
		var entries []*Entry
		for _, record := range idx.Records {
			if record.entry != nil {
				entries = append(entries, record.entry)
			}
		}
		if err := validateGraph(entries); err != nil {
			return
		}
		path, err := BuildPath(entries, "")
		if err != nil {
			return
		}
		_ = RestoreModelState(path)
		_ = BuildContextEntries(path)
	})
}

func FuzzExactKeysDifferential(f *testing.F) {
	seeds := []string{
		`{"type":"message","id":"u1","message":{"role":"user","content":"hi"}}`,
		`{"TYPE":"message","ID":"u1","message":{"ROLE":"user","content":"hi"}}`,
		`{"type":"message","id":"canon","ID":"alias","parentId":"p1","ParentID":"evil","message":{"role":"user","ROLE":"admin","content":"hi"}}`,
		`{"type":"message","id":"a","id":"b","message":{"role":"user","content":"hi"}}`,
		`{"type":"message","id":"t1","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","ID":"evil","name":"read","arguments":{}}]}}`,
		`{"type":"message","id":"x","unknownField":{"a":[1,2]},"nothing":null,"message":{"role":"user","content":"hi"}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}
		line := bytes.TrimSpace(data)
		if len(line) == 0 || line[0] != '{' {
			return
		}
		eager, eagerErr := UnmarshalEntry(line)
		// Indexed reader path: metadata identity from the same bytes.
		// (Disk payload loading is covered with real files in exact_test.go.)
		idx, indexErr := BuildIndexFrom(bytes.NewReader(append(append([]byte(nil), line...), '\n')), "fuzz.jsonl")
		var meta *RecordMeta
		if indexErr == nil {
			for _, m := range idx.Records {
				if m.Type == "session" {
					continue
				}
				cp := m
				meta = &cp
				break
			}
		}
		// Agreement: both reject, or both accept with identical identity.
		if (eagerErr == nil) != (indexErr == nil && meta != nil) {
			t.Fatalf("eager/indexed disagree: %v vs %v on %.80q", eagerErr, indexErr, line)
		}
		if eagerErr != nil {
			return
		}
		if eager.ID() != meta.ID || eager.Kind != meta.Type {
			t.Fatalf("identity mismatch: %s/%s vs %+v", eager.ID(), eager.Kind, meta)
		}
		lp, mp := eager.ParentID(), meta.ParentID
		if (lp == nil) != (mp == "") || (lp != nil && *lp != mp) {
			t.Fatalf("parent mismatch: %v vs %q", lp, mp)
		}
	})
}
