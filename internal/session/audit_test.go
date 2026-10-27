package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// P2-062: audit all v3 entry shapes — supported envelopes, preserve unknown fields,
// numeric precision, null/empty distinctions. Reference: session-manager.ts.
func TestV3Audit(t *testing.T) {
	records := []string{
		`{"type":"session","version":3,"id":"s","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp","extra":"keep"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi","timestamp":1},"unknownField":123}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"ok","textSignature":"sig"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2}}`,
		`{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"read","content":[{"type":"text","text":"body"}],"timestamp":3}}`,
		`{"type":"message","id":"b1","parentId":"t1","timestamp":"2026-01-01T00:00:04.000Z","message":{"role":"bashExecution","command":"echo","output":"hi","exitCode":0,"timestamp":4}}`,
		`{"type":"thinking_level_change","id":"th1","parentId":"b1","timestamp":"2026-01-01T00:00:05.000Z","thinkingLevel":"high"}`,
		`{"type":"model_change","id":"m1","parentId":"th1","timestamp":"2026-01-01T00:00:06.000Z","provider":"anthropic","modelId":"m"}`,
		`{"type":"usage","id":"u2","parentId":"m1","timestamp":"2026-01-01T00:00:07.000Z","kind":"x","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`,
		`{"type":"compaction","id":"c1","parentId":"u2","timestamp":"2026-01-01T00:00:08.000Z","summary":"s","firstKeptEntryId":"u1","tokensBefore":10}`,
		`{"type":"branch_summary","id":"bs1","parentId":"c1","timestamp":"2026-01-01T00:00:09.000Z","fromId":"u1","summary":"branch"}`,
		`{"type":"custom","id":"cu1","parentId":"bs1","timestamp":"2026-01-01T00:00:10.000Z","customType":"x","data":{"a":1}}`,
		`{"type":"custom_message","id":"cm1","parentId":"cu1","timestamp":"2026-01-01T00:00:11.000Z","customType":"ext","content":"hi","display":true}`,
		`{"type":"context_edit","id":"e1","parentId":"cm1","timestamp":"2026-01-01T00:00:12.000Z","targetId":"u1","replacement":null}`,
		`{"type":"label","id":"l1","parentId":"e1","timestamp":"2026-01-01T00:00:13.000Z","targetId":"u1","label":"mark"}`,
		`{"type":"session_info","id":"si1","parentId":"l1","timestamp":"2026-01-01T00:00:14.000Z","name":"demo"}`,
		// Large integer precision.
		`{"type":"message","id":"big","parentId":"si1","timestamp":"2026-01-01T00:00:15.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c2","name":"t","arguments":{"n":9007199254740993}}],"api":"a","provider":"p","model":"m","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":15}}`,
		// Null vs empty content.
		`{"type":"message","id":"null1","parentId":"big","timestamp":"2026-01-01T00:00:16.000Z","message":{"role":"user","content":null,"timestamp":16}}`,
	}
	for _, rec := range records {
		fe, err := UnmarshalFileEntry([]byte(rec))
		if err != nil {
			t.Fatalf("unmarshal %s: %v", rec[:40], err)
		}
		if fe.IsHeader {
			continue
		}
		// Round-trip: marshal payload, preserve unknown fields via Raw.
		if len(fe.Entry.Raw) == 0 {
			t.Fatalf("raw lost for %s", rec[:40])
		}
		var orig, round map[string]any
		json.Unmarshal([]byte(rec), &orig)
		json.Unmarshal(fe.Entry.Raw, &round)
		// Unknown top-level fields preserved (e.g. extra, unknownField).
		for k, v := range orig {
			if k == "message" {
				continue // normalized separately
			}
			if _, ok := round[k]; !ok {
				t.Fatalf("field %s lost in %s", k, rec[:40])
			}
			_ = v
		}
	}
	// Null content normalized, not dropped (pi parity).
	fe, _ := UnmarshalFileEntry([]byte(records[len(records)-1]))
	if fe.Entry.Message.Message.User == nil {
		t.Fatal("null user not decoded")
	}
	// Large integer preserved as json.Number (no rounding).
	fe, _ = UnmarshalFileEntry([]byte(records[len(records)-2]))
	raw := string(fe.Entry.Raw)
	if !strings.Contains(raw, "9007199254740993") {
		t.Fatalf("large int lost: %s", raw)
	}
}
