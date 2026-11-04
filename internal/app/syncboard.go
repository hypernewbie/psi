package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"psi/internal/pibridge"
	"psi/internal/provider"
	"psi/internal/tools"

	pibridgepb "psi/internal/pibridge/pb"
)

// Native Syncboard tool. It is advertised only while the Phi bridge
// negotiated pi.syncboard.v1, and every call rechecks: availability can
// change on reconnect. Validation, the safe reason vocabulary, and the
// result mapping translate the reference extension's syncboard tool; the
// uncertainty rule is load-bearing, so it is kept verbatim.
const (
	syncboardToolName       = "syncboard"
	syncboardMaxValueBytes  = 64 * 1024
	syncboardUncertaintyMsg = "Outcome unknown. Do not retry automatically. Read the key after reconnection before deciding what to do."
)

type syncboardCall struct {
	opName string
	op     pibridgepb.SyncboardOperation
	key    string
	value  []byte
}

func syncboardToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name: syncboardToolName,
		Description: "Read and update messages on the shared Syncboard configured by this Phi process. " +
			"Values can contain JSON card data. Supports list, get, upsert, and delete. " +
			"Read or update messages on Phi Syncboard. " +
			"Use list before acting when the current board state is not known. " +
			"Do not retry an upsert or delete when its outcome is unknown. Read the key after reconnection first.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"type": "string", "enum": []string{"list", "get", "upsert", "delete"}},
				"key":       map[string]any{"type": "string", "description": "Message key for get, upsert, and delete"},
				"value":     map[string]any{"description": "JSON value for upsert"},
			},
			"required":             []string{"operation"},
			"additionalProperties": false,
		},
	}
}

// validateSyncboardCall rejects operation/input mismatches before any bridge
// work. Keys use the same rules as Phi's own validation.
func validateSyncboardCall(args map[string]any) (syncboardCall, error) {
	op, _ := args["operation"].(string)
	_, hasKey := args["key"]
	_, hasValue := args["value"]
	switch op {
	case "list":
		if hasKey || hasValue {
			return syncboardCall{}, fmt.Errorf("List accepts no key or value.")
		}
		return syncboardCall{opName: "list", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_LIST}, nil
	case "get", "upsert", "delete":
	default:
		return syncboardCall{}, fmt.Errorf("Invalid Syncboard operation.")
	}
	key, _ := args["key"].(string)
	if !hasKey || args["key"] == nil {
		return syncboardCall{}, fmt.Errorf("Operation requires a key.")
	}
	if _, ok := args["key"].(string); !ok {
		return syncboardCall{}, fmt.Errorf("Operation requires a key.")
	}
	if op != "upsert" && hasValue {
		return syncboardCall{}, fmt.Errorf("Get and delete accept no value.")
	}
	if op == "upsert" && !hasValue {
		return syncboardCall{}, fmt.Errorf("Upsert requires a JSON value.")
	}
	key = strings.TrimSpace(key)
	if !validSyncboardKey(key) {
		return syncboardCall{}, fmt.Errorf("Invalid Syncboard key.")
	}
	switch op {
	case "get":
		return syncboardCall{opName: "get", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET, key: key}, nil
	case "delete":
		return syncboardCall{opName: "delete", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_DELETE, key: key}, nil
	}
	if !isSyncboardJSONValue(args["value"]) {
		return syncboardCall{}, fmt.Errorf("Value must be valid JSON.")
	}
	text, err := json.Marshal(args["value"])
	if err != nil {
		return syncboardCall{}, fmt.Errorf("Value must be valid JSON.")
	}
	if len(text) == 0 || len(text) > syncboardMaxValueBytes {
		return syncboardCall{}, fmt.Errorf("Syncboard value exceeds the 64 KiB limit.")
	}
	return syncboardCall{opName: "upsert", op: pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_UPSERT, key: key, value: text}, nil
}

func validSyncboardKey(key string) bool {
	if key == "" || len(key) > 256 {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment != "" && strings.Trim(segment, ".") == "" {
			return false
		}
	}
	return true
}

// isSyncboardJSONValue mirrors the reference check: finite numbers, valid
// UTF-8 strings, and plain JSON structure. Decoded provider arguments can
// only hold these shapes, so this is a guard, not a conversion.
func isSyncboardJSONValue(value any) bool {
	switch v := value.(type) {
	case nil, bool:
		return true
	case string:
		return utf8.ValidString(v)
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	case float32:
		f := float64(v)
		return !math.IsNaN(f) && !math.IsInf(f, 0)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case []any:
		for _, item := range v {
			if !isSyncboardJSONValue(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for key, item := range v {
			if !utf8.ValidString(key) || !isSyncboardJSONValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// parseSyncboardResult maps one coordinator answer to the model-facing
// value. Errors carry the uncertainty rule for mutations, because a failed
// upsert or delete may still have run.
func parseSyncboardResult(res pibridge.SyncboardResult, call syncboardCall) (any, error) {
	switch res.Outcome {
	case pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_NOT_SENT:
		return nil, fmt.Errorf("Syncboard request not sent (%s).", syncboardSafeReason(res.Reason))
	case pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_UNKNOWN:
		return nil, fmt.Errorf("Syncboard result unavailable (%s). %s", syncboardSafeReason(res.Reason), syncboardUncertaintyMsg)
	case pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_HTTP_RESPONSE:
	default:
		return nil, fmt.Errorf("Syncboard result unavailable. %s", syncboardUncertaintyMsg)
	}
	mutation := call.op == pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_UPSERT ||
		call.op == pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_DELETE
	if res.HTTPStatus == 404 && call.op == pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET {
		return map[string]any{"found": false, "key": call.key}, nil
	}
	if res.HTTPStatus == 404 && call.op == pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_DELETE {
		return map[string]any{"deleted": true, "key": call.key, "already_absent": true}, nil
	}
	if res.HTTPStatus != 200 {
		if mutation {
			return nil, fmt.Errorf("Syncboard returned HTTP %d (%s). %s", res.HTTPStatus, syncboardSafeReason(res.Reason), syncboardUncertaintyMsg)
		}
		return nil, fmt.Errorf("Syncboard returned HTTP %d (%s).", res.HTTPStatus, syncboardSafeReason(res.Reason))
	}
	if call.op == pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_DELETE {
		if len(res.Body) != 0 {
			return nil, fmt.Errorf("Syncboard response was invalid. %s", syncboardUncertaintyMsg)
		}
		return map[string]any{"deleted": true, "key": call.key, "already_absent": false}, nil
	}
	var body any
	if err := json.Unmarshal(res.Body, &body); err != nil {
		return nil, fmt.Errorf("Syncboard response was invalid. %s", syncboardUncertaintyMsg)
	}
	if call.op == pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_LIST {
		rows, ok := body.([]any)
		if !ok {
			return nil, fmt.Errorf("Syncboard response was invalid. %s", syncboardUncertaintyMsg)
		}
		for _, row := range rows {
			if !isSyncboardMessage(row, "") {
				return nil, fmt.Errorf("Syncboard response was invalid. %s", syncboardUncertaintyMsg)
			}
		}
		return rows, nil
	}
	if !isSyncboardMessage(body, call.key) {
		return nil, fmt.Errorf("Syncboard response was invalid. %s", syncboardUncertaintyMsg)
	}
	return body, nil
}

func isSyncboardMessage(value any, expectedKey string) bool {
	row, ok := value.(map[string]any)
	if !ok {
		return false
	}
	key, _ := row["key"].(string)
	content, _ := row["value"].(string)
	created, _ := row["created_at"].(string)
	updated, _ := row["updated_at"].(string)
	if key == "" || row["key"] == nil || row["value"] == nil || row["created_at"] == nil || row["updated_at"] == nil {
		return false
	}
	if expectedKey != "" && key != expectedKey {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, updated); err != nil {
		return false
	}
	_ = content
	return true
}

// syncboardCallErrorText formats a failed bridge call the way the reference
// tool does: unknown outcomes carry the no-retry rule.
func syncboardCallErrorText(err error) string {
	se, ok := err.(*pibridge.SyncboardError)
	if !ok {
		return "Syncboard request failed."
	}
	if se.Outcome == "unknown" {
		return fmt.Sprintf("Syncboard result unavailable (%s). %s", syncboardSafeReason(se.Reason), syncboardUncertaintyMsg)
	}
	return fmt.Sprintf("Syncboard request not sent (%s).", syncboardSafeReason(se.Reason))
}

func syncboardSafeReason(reason string) string {
	switch reason {
	case "unavailable", "invalid-request", "invalid-config", "not-ready", "busy",
		"timeout", "cancelled", "too-large", "invalid-response", "http-error", "transport-error":
		return reason
	default:
		return "invalid-response"
	}
}

// syncboardAvailable reports whether the tool may be advertised. The bridge
// may be absent (plain pane) or connected without the capability (a Phi
// without a coordinator handler): both hide the tool.
func (r *runtime) syncboardAvailable() bool {
	return r != nil && r.bridge != nil && r.bridge.SyncboardAccepted()
}

// runSyncboard executes one validated tool call. Validation runs before the
// bridge check so argument errors read the same with or without Phi.
func (r *runtime) runSyncboard(ctx context.Context, args map[string]any) tools.Result {
	call, err := validateSyncboardCall(args)
	if err != nil {
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: err.Error()}}}
	}
	if r.bridge == nil {
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: "Syncboard is unavailable because the Phi bridge is not connected."}}}
	}
	res, err := r.bridge.CallSyncboard(ctx, call.op, call.key, call.value)
	if err != nil {
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: syncboardCallErrorText(err)}}}
	}
	value, err := parseSyncboardResult(res, call)
	if err != nil {
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: err.Error()}}, Details: map[string]any{"operation": call.opName}}
	}
	text, merr := json.MarshalIndent(value, "", "  ")
	if merr != nil {
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: "Syncboard response was invalid. " + syncboardUncertaintyMsg}}}
	}
	return tools.Result{
		Content: []tools.Block{{Type: "text", Text: string(text)}},
		Details: map[string]any{"operation": call.opName, "result": value},
	}
}

// syncboardInternalHandler adapts the tool to the agent's internal-tool
// table. The error text distinguishes a failed call from a failed bridge.
func (r *runtime) syncboardInternalHandler(ctx context.Context, invocation tools.Invocation) tools.Result {
	return r.runSyncboard(ctx, invocation.Arguments)
}
