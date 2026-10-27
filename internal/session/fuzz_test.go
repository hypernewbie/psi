package session

import (
	"bytes"
	"reflect"
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
		`{"type":"session","version":3,"Version":99,"id":"h","ID":"alias","cwd":"/synthetic"}`,
		`{"type":"message","id":"r","message":{"role":"toolResult","content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}}`,
		`{"type":"message","id":"a","message":{"role":"assistant","role":null,"content":null}}`,
		`{"type":"message","id":"a","message":{"role":"assistant","usage":{"input":777,"input":null,"cost":{"total":2.5}}}}`,
		`{"type":"custom","id":"p","customType":"psi.input","data":{"targetId":"user"},"details":{"targetId":"wrong"}}`,
		`{"type":"compaction","id":"c","firstKeptEntryId":"u","summary":"synthetic","usage":{"input":3}}`,
		`{"type":"message","id":"s","message":{"role":"system","sections":{"a":"x"},"toolsAdded":[{"name":"read","parameters":{}}]}}`,
		`{"type":"message","id":"b","message":{"role":"bashExecution","cancelled":true,"exitCode":3}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}
		// Compare one complete record, including headers, acceptance, and
		// every metadata field used by activation/accounting/display. The
		// former identity-only comparison missed role and usage divergences.
		full, eagerErr := UnmarshalFileEntry(data)
		thin, header, indexErr := metadataRecord(data)
		if (eagerErr == nil) != (indexErr == nil) {
			t.Fatalf("schema disagreement: eager=%v metadata=%v", eagerErr, indexErr)
		}
		if eagerErr != nil {
			return
		}
		if full.IsHeader {
			if thin != nil || !reflect.DeepEqual(full.Header, header) {
				t.Fatalf("header disagreement: eager=%+v metadata=%+v", full.Header, header)
			}
			return
		}
		want, got := reviewMetadataState(full.Entry), reviewMetadataState(thin)
		if header != nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("metadata disagreement: eager=%#v metadata=%#v", want, got)
		}
	})
}
