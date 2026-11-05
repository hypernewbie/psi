package app

import (
	"context"
	"strings"
	"testing"

	"psi/internal/pibridge"

	pibridgepb "psi/internal/pibridge/pb"
)

// Validation rejects operation/input mismatches before any bridge work, with
// the reference tool's messages.
func TestValidateSyncboardCall(t *testing.T) {
	if _, err := validateSyncboardCall(map[string]any{"operation": "list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, args := range []map[string]any{
		{"operation": "list", "key": "k"},
		{"operation": "list", "value": 1},
		{"operation": "get"},
		{"operation": "get", "key": "k", "value": 1},
		{"operation": "get", "key": 42},
		{"operation": "delete"},
		{"operation": "delete", "key": "k", "value": "v"},
		{"operation": "upsert", "key": "k"},
		{"operation": "upsert", "key": ""},
		{"operation": "upsert", "key": ".", "value": 1},
		{"operation": "upsert", "key": "a/./b", "value": 1},
		{"operation": "upsert", "key": strings.Repeat("k", 257), "value": 1},
		{"operation": "upsert", "key": "k", "value": "  "[:0] + "\xff"},
		{"operation": "fetch", "key": "k"},
		{"operation": 42},
		{},
	} {
		if _, err := validateSyncboardCall(args); err == nil {
			t.Errorf("accepted invalid call: %#v", args)
		}
	}
	call, err := validateSyncboardCall(map[string]any{"operation": "upsert", "key": " k ", "value": map[string]any{"a": 1.0}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if call.key != "k" || string(call.value) != `{"a":1}` {
		t.Fatalf("upsert call = %+v", call)
	}
	big := strings.Repeat("x", syncboardMaxValueBytes)
	if _, err := validateSyncboardCall(map[string]any{"operation": "upsert", "key": "k", "value": big}); err == nil {
		t.Fatal("accepted an over-limit value")
	}
}

// The result mapping distinguishes found/absent, guards mutations with the
// uncertainty rule, and never trusts a misshapen body.
func TestParseSyncboardResult(t *testing.T) {
	message := `{"key":"k","value":"v","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-06-01T00:00:00.5Z"}`
	get := syncboardCall{opName: "get", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET, key: "k"}
	if value, err := parseSyncboardResult(httpSyncboard(200, message), get); err != nil {
		t.Fatalf("get: %v", err)
	} else if row := value.(map[string]any); row["key"] != "k" {
		t.Fatalf("get body = %#v", value)
	}
	if value, err := parseSyncboardResult(httpSyncboard(404, ""), get); err != nil {
		t.Fatalf("missing get: %v", err)
	} else if row := value.(map[string]any); row["found"] != false {
		t.Fatalf("missing get = %#v", value)
	}
	if _, err := parseSyncboardResult(httpSyncboard(500, ""), get); err == nil || strings.Contains(err.Error(), "Do not retry") {
		t.Fatalf("read error carried the mutation rule: %v", err)
	}
	upsert := syncboardCall{opName: "upsert", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_UPSERT, key: "k"}
	if _, err := parseSyncboardResult(httpSyncboard(500, ""), upsert); err == nil || !strings.Contains(err.Error(), "Do not retry") {
		t.Fatalf("mutation error lost the uncertainty rule: %v", err)
	}
	del := syncboardCall{opName: "delete", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_DELETE, key: "k"}
	if value, err := parseSyncboardResult(httpSyncboard(200, ""), del); err != nil {
		t.Fatalf("delete: %v", err)
	} else if row := value.(map[string]any); row["deleted"] != true || row["already_absent"] != false {
		t.Fatalf("delete = %#v", value)
	}
	if value, err := parseSyncboardResult(httpSyncboard(404, ""), del); err != nil {
		t.Fatalf("absent delete: %v", err)
	} else if row := value.(map[string]any); row["already_absent"] != true {
		t.Fatalf("absent delete = %#v", value)
	}
	if _, err := parseSyncboardResult(httpSyncboard(200, "{}"), del); err == nil {
		t.Fatal("accepted a delete with a body")
	}
	for _, res := range []pibridge.SyncboardResult{
		{Outcome: pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_NOT_SENT, Reason: "busy"},
		{Outcome: pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_UNKNOWN, Reason: "timeout"},
		{Outcome: pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_UNSPECIFIED},
	} {
		if _, err := parseSyncboardResult(res, get); err == nil {
			t.Fatalf("accepted outcome %v", res.Outcome)
		}
	}
	list := syncboardCall{opName: "list", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_LIST}
	if value, err := parseSyncboardResult(httpSyncboard(200, "["+message+"]"), list); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(value.([]any)) != 1 {
		t.Fatalf("list = %#v", value)
	}
	for _, body := range []string{`{}`, `{"key":"other","value":"v","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`, `{"key":"k","value":"v","created_at":"not-a-time","updated_at":"2026-01-01T00:00:00Z"}`, `not json`, `[` + message + `, 42]`} {
		op := get
		if strings.HasPrefix(body, "[") {
			op = list
		}
		if _, err := parseSyncboardResult(httpSyncboard(200, body), op); err == nil {
			t.Errorf("accepted invalid body: %s", body)
		}
	}
	// Unknown reasons collapse to invalid-response, never leak a body.
	if _, err := parseSyncboardResult(pibridge.SyncboardResult{Outcome: pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_NOT_SENT, Reason: "<script>"}, get); err == nil || strings.Contains(err.Error(), "<script>") {
		t.Fatalf("unsafe reason leaked: %v", err)
	}
}

func httpSyncboard(status uint32, body string) pibridge.SyncboardResult {
	return pibridge.SyncboardResult{
		Outcome:    pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_HTTP_RESPONSE,
		HTTPStatus: status,
		Body:       []byte(body),
	}
}

// The tool hides without a bridge and reports a failed call with the
// reference wording.
func TestRunSyncboardWithoutBridge(t *testing.T) {
	r := &runtime{}
	if r.syncboardAvailable() {
		t.Fatal("tool reads available without a bridge")
	}
	result := r.runSyncboard(context.Background(), map[string]any{"operation": "list"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "not connected") {
		t.Fatalf("result = %+v", result)
	}
	result = r.runSyncboard(context.Background(), map[string]any{"operation": "get"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "requires a key") {
		t.Fatalf("validation did not run first: %+v", result)
	}
	if got := syncboardCallErrorText(context.DeadlineExceeded); got != "Syncboard request failed." {
		t.Fatalf("non-call error = %q", got)
	}
	if got := syncboardCallErrorText(&pibridge.SyncboardError{Outcome: "unknown", Reason: "timeout"}); !strings.Contains(got, "Do not retry") {
		t.Fatalf("unknown call error = %q", got)
	}
	if got := syncboardCallErrorText(&pibridge.SyncboardError{Outcome: "not-sent", Reason: "busy"}); !strings.Contains(got, "not sent (busy)") {
		t.Fatalf("not-sent call error = %q", got)
	}
}
