package session

import (
	"bytes"
	"strings"
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
		// Line-aligned differential: each JSONL record through eager
		// (UnmarshalEntry) and indexed (BuildIndexFrom metadata) paths.
		// Blank lines are skipped by both; trailing partial lines count.
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		lines := strings.Split(text, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		// want/wantErr align with ALL lines (blanks included) so indices
		// match the indexed reader's 1-based ParseError lines.
		var want []string
		var wantErr []bool
		for _, ln := range lines {
			if strings.TrimSpace(ln) == "" {
				wantErr = append(wantErr, false)
				want = append(want, "\x00blank\x00")
				continue
			}
			e, err := UnmarshalEntry([]byte(ln))
			if err != nil {
				wantErr = append(wantErr, true)
				want = append(want, "")
				continue
			}
			wantErr = append(wantErr, false)
			pid := ""
			if e.ParentID() != nil {
				pid = *e.ParentID()
			}
			want = append(want, e.Kind+"\x00"+e.ID()+"\x00"+pid)
		}
		idx, indexErr := BuildIndexFrom(bytes.NewReader(data), "fuzz.jsonl")
		if indexErr != nil {
			// The indexed path reports the first bad line; every prior
			// record must have agreed, and the failing line must be one
			// the eager path also rejects.
			if perr, ok := indexErr.(*ParseError); ok {
				for i := 0; i < perr.Line-1 && i < len(want); i++ {
					if wantErr[i] {
						t.Fatalf("line %d: eager rejected but indexed passed it", i+1)
					}
				}
				// Find the failing line among non-blank lines.
				nonBlank := 0
				for _, ln := range lines {
					if strings.TrimSpace(ln) == "" {
						continue
					}
					nonBlank++
					if nonBlank == perr.Line {
						if _, err := UnmarshalEntry([]byte(ln)); err == nil {
							t.Fatalf("line %d: indexed rejects, eager accepts: %.80q", perr.Line, ln)
						}
						return
					}
				}
				return
			}
			t.Fatalf("non-parse index error: %v", indexErr)
		}
		var got []string
		for _, m := range idx.Records {
			if m.Type == "session" {
				continue
			}
			got = append(got, m.Type+"\x00"+m.ID+"\x00"+m.ParentID)
		}
		var wantNonErr []string
		for i, w := range want {
			if wantErr[i] {
				continue
			}
			if w == "\x00blank\x00" {
				continue
			}
			wantNonErr = append(wantNonErr, w)
		}
		if len(got) != len(wantNonErr) {
			// Eager-rejected lines are skipped by agreement above; any
			// count gap here is a real divergence.
			t.Fatalf("record count: eager %d vs indexed %d on %.120q", len(wantNonErr), len(got), text)
		}
		for i := range got {
			if got[i] != wantNonErr[i] {
				t.Fatalf("record %d identity: %q vs %q", i, wantNonErr[i], got[i])
			}
		}
	})
}
