package session

// Metadata reads share the eager reader's Go schemas, but never decode hidden
// payload strings. The standard JSON scanner owns syntax and depth checks.
// Object views keep raw slices only for this record, use exact keys, and apply
// last-key-wins before schema checks or extraction. No view escapes into Entry.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

type metadataObjectView map[string][]byte

var metadataTypes = map[string]reflect.Type{
	"session":               reflect.TypeFor[SessionHeader](),
	"message":               reflect.TypeFor[SessionMessageEntry](),
	"thinking_level_change": reflect.TypeFor[ThinkingLevelChangeEntry](),
	"model_change":          reflect.TypeFor[ModelChangeEntry](),
	"usage":                 reflect.TypeFor[UsageEntry](),
	"compaction":            reflect.TypeFor[CompactionEntry](),
	"branch_summary":        reflect.TypeFor[BranchSummaryEntry](),
	"custom":                reflect.TypeFor[CustomEntry](),
	"custom_message":        reflect.TypeFor[CustomMessageEntry](),
	"context_edit":          reflect.TypeFor[ContextEditEntry](),
	"label":                 reflect.TypeFor[LabelEntry](),
	"session_info":          reflect.TypeFor[SessionInfoEntry](),
}

var metadataRoles = map[string]reflect.Type{
	"system":            reflect.TypeFor[SystemMessage](),
	"user":              reflect.TypeFor[UserMessage](),
	"assistant":         reflect.TypeFor[AssistantMessage](),
	"toolResult":        reflect.TypeFor[ToolResultMessage](),
	"bashExecution":     reflect.TypeFor[BashExecutionMessage](),
	"custom":            reflect.TypeFor[CustomMessage](),
	"branchSummary":     reflect.TypeFor[BranchSummaryMessage](),
	"compactionSummary": reflect.TypeFor[CompactionSummaryMessage](),
}

type metadataField struct {
	index  int
	typeOf reflect.Type
}

var metadataSchemaCache sync.Map // reflect.Type -> map[string]metadataField

func metadataSchema(t reflect.Type) map[string]metadataField {
	if cached, ok := metadataSchemaCache.Load(t); ok {
		return cached.(map[string]metadataField)
	}
	fields := make(map[string]metadataField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		// Raw caches and internal fields are not protocol fields.
		if f.PkgPath != "" || name == "" || name == "-" {
			continue
		}
		fields[name] = metadataField{index: i, typeOf: f.Type}
	}
	cached, _ := metadataSchemaCache.LoadOrStore(t, fields)
	return cached.(map[string]metadataField)
}

// These view functions only receive values whose syntax json.Valid checked.
// They locate boundaries iteratively, without recursive descent or unescaping
// a large skipped string. Kept scalar values use encoding/json below.
func metadataStringEnd(b []byte, start int) int {
	for i := start + 1; i < len(b); {
		j := i + bytes.IndexByte(b[i:], '"')
		slashes := 0
		for k := j - 1; k > start && b[k] == '\\'; k-- {
			slashes++
		}
		if slashes%2 == 0 {
			return j + 1
		}
		i = j + 1
	}
	panic("metadata view received invalid JSON string")
}

func metadataValueEnd(b []byte, start int) int {
	switch b[start] {
	case '"':
		return metadataStringEnd(b, start)
	case '{', '[':
		depth := 1
		for i := start + 1; i < len(b); {
			switch b[i] {
			case '"':
				i = metadataStringEnd(b, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
	default:
		for i := start; i < len(b); i++ {
			switch b[i] {
			case ' ', '\t', '\r', '\n', ',', '}', ']':
				return i
			}
		}
		return len(b)
	}
	panic("metadata view received invalid JSON container")
}

func metadataSkipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	return i
}

func metadataObject(b []byte) (metadataObjectView, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return nil, fmt.Errorf("expected JSON object")
	}
	out := make(metadataObjectView)
	for i := metadataSkipSpace(b, 1); b[i] != '}'; {
		end := metadataStringEnd(b, i)
		var key string
		if err := json.Unmarshal(b[i:end], &key); err != nil {
			return nil, err
		}
		i = metadataSkipSpace(b, end)
		i = metadataSkipSpace(b, i+1) // ':'
		end = metadataValueEnd(b, i)
		out[key] = b[i:end]
		i = metadataSkipSpace(b, end)
		if b[i] == ',' {
			i = metadataSkipSpace(b, i+1)
		}
	}
	return out, nil
}

func metadataEach(b []byte, visit func([]byte) error) error {
	b = bytes.TrimSpace(b)
	for i := metadataSkipSpace(b, 1); b[i] != ']'; {
		end := metadataValueEnd(b, i)
		if err := visit(b[i:end]); err != nil {
			return err
		}
		i = metadataSkipSpace(b, end)
		if b[i] == ',' {
			i = metadataSkipSpace(b, i+1)
		}
	}
	return nil
}

func metadataNull(b []byte) bool { return bytes.Equal(b, []byte("null")) }

func metadataString(obj metadataObjectView, key string) string {
	var value string
	_ = json.Unmarshal(obj[key], &value)
	return value
}

// validateMetadata follows the same field types and custom union rules as the
// eager reader. Unknown fields stay opaque. Only interface{} numbers require
// float validation; RawMessage and UseNumber tool arguments remain opaque.
func validateMetadata(b []byte, typ reflect.Type) error {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if typ == reflect.TypeFor[AgentMessage]() {
		obj, err := metadataObject(b)
		if err != nil {
			return err
		}
		if roleType := metadataRoles[metadataString(obj, "role")]; roleType != nil {
			return validateMetadata(b, roleType)
		}
		return nil // unknown roles are preserved, then refused for continuation
	}
	if typ == reflect.TypeFor[ContentBlock]() || typ == reflect.TypeFor[UserContentBlock]() {
		obj, err := metadataObject(b)
		if err != nil {
			return err
		}
		var blockType reflect.Type
		switch metadataString(obj, "type") {
		case "text":
			blockType = reflect.TypeFor[TextContent]()
		case "thinking":
			if typ == reflect.TypeFor[ContentBlock]() {
				blockType = reflect.TypeFor[ThinkingContent]()
			}
		case "toolCall":
			if typ == reflect.TypeFor[ContentBlock]() {
				blockType = reflect.TypeFor[ToolCall]()
			}
		case "image":
			if typ == reflect.TypeFor[UserContentBlock]() {
				blockType = reflect.TypeFor[ImageContent]()
			}
		}
		if blockType != nil {
			return validateMetadata(b, blockType)
		}
		return nil
	}
	if typ == reflect.TypeFor[ToolCall]() {
		obj, err := metadataObject(b)
		if err != nil {
			return err
		}
		if args := obj["arguments"]; len(args) != 0 && !metadataNull(args) && args[0] != '{' {
			return fmt.Errorf("tool arguments must be an object")
		}
		return nil // identity and optional signatures use tolerant exact lookups
	}
	if metadataNull(b) {
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		return validateMetadata(b, typ.Elem())
	}
	if len(b) == 0 {
		return fmt.Errorf("missing metadata value")
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, err := metadataObject(b)
		if err != nil {
			return err
		}
		for key, field := range metadataSchema(typ) {
			if value, ok := obj[key]; ok {
				if err := validateMetadata(value, field.typeOf); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			}
		}
	case reflect.Slice:
		if b[0] != '[' {
			return fmt.Errorf("expected JSON array")
		}
		return metadataEach(b, func(value []byte) error { return validateMetadata(value, typ.Elem()) })
	case reflect.Map:
		obj, err := metadataObject(b)
		if err != nil {
			return err
		}
		for _, value := range obj {
			if err := validateMetadata(value, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Interface:
		return validateMetadataAny(b)
	case reflect.String:
		if b[0] != '"' {
			return fmt.Errorf("expected JSON string")
		}
	case reflect.Bool:
		if b[0] != 't' && b[0] != 'f' {
			return fmt.Errorf("expected JSON boolean")
		}
	case reflect.Int, reflect.Int64:
		if _, err := strconv.ParseInt(string(b), 10, typ.Bits()); err != nil {
			return err
		}
	case reflect.Float64:
		if _, err := strconv.ParseFloat(string(b), 64); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported metadata schema %s", typ)
	}
	return nil
}

func validateMetadataAny(b []byte) error {
	switch b[0] {
	case '{':
		obj, _ := metadataObject(b)
		for _, value := range obj {
			if err := validateMetadataAny(value); err != nil {
				return err
			}
		}
	case '[':
		return metadataEach(b, validateMetadataAny)
	case '"', 'n', 't', 'f':
	default:
		_, err := strconv.ParseFloat(string(b), 64)
		return err
	}
	return nil
}

// decodeMetadataScalars copies only canonical fields into the small structures
// retained by the index. In particular it does not copy unknown usage fields.
func decodeMetadataScalars(b []byte, dst any) {
	value := reflect.ValueOf(dst).Elem()
	if metadataNull(b) || len(b) == 0 {
		return
	}
	obj, _ := metadataObject(b)
	for key, field := range metadataSchema(value.Type()) {
		if raw, ok := obj[key]; ok {
			v := value.Field(field.index)
			if v.Kind() == reflect.Struct {
				decodeMetadataScalars(raw, v.Addr().Interface())
			} else {
				_ = json.Unmarshal(raw, v.Addr().Interface())
			}
		}
	}
}

func metadataUsage(b []byte) Usage {
	var usage Usage
	decodeMetadataScalars(b, &usage)
	return usage
}

func handScanRecord(data []byte) (*Entry, *SessionHeader, error) {
	// This also enforces the eager decoder's depth bound before any view
	// function operates, including for unknown or hidden payload values.
	if !json.Valid(data) {
		return nil, nil, fmt.Errorf("invalid JSON record or nesting exceeds limit")
	}
	obj, err := metadataObject(data)
	if err != nil {
		return nil, nil, err
	}
	kind, id := metadataString(obj, "type"), metadataString(obj, "id")
	if kind == "" || id == "" {
		return nil, nil, fmt.Errorf("entry missing type or id")
	}
	typ := metadataTypes[kind]
	if typ == nil {
		return nil, nil, fmt.Errorf("unknown entry type %q", kind)
	}
	if err := validateMetadata(bytes.TrimSpace(data), typ); err != nil {
		return nil, nil, err
	}
	if kind == "session" {
		var header SessionHeader
		decodeMetadataScalars(data, &header)
		return nil, &header, nil
	}
	var parent *string
	if p := metadataString(obj, "parentId"); p != "" {
		parent = &p
	}
	e := &Entry{Kind: kind, Base: entryBase{Type: kind, ID: id, ParentID: parent, Timestamp: metadataString(obj, "timestamp")}}
	switch kind {
	case "message":
		message, _ := metadataObject(obj["message"])
		role := metadataString(message, "role")
		m := AgentMessage{Role: role}
		switch role {
		case "user":
			m.User = &UserMessage{Role: role}
		case "system":
			m.System = &SystemMessage{Role: role}
		case "assistant":
			a := &AssistantMessage{Role: role, Provider: metadataString(message, "provider"), Model: metadataString(message, "model"), API: metadataString(message, "api"), Usage: metadataUsage(message["usage"])}
			_ = json.Unmarshal(message["responseModel"], &a.ResponseModel)
			content := message["content"]
			if len(content) != 0 && !metadataNull(content) {
				_ = metadataEach(content, func(raw []byte) error {
					block, _ := metadataObject(raw)
					switch metadataString(block, "type") {
					case "text":
						a.Content = append(a.Content, ContentBlock{Type: "text", Text: &TextContent{Type: "text"}})
					case "toolCall":
						a.Content = append(a.Content, ContentBlock{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: metadataString(block, "id"), Name: metadataString(block, "name")}})
					}
					return nil
				})
			}
			m.Assistant = a
		case "toolResult":
			details, _ := metadataObject(message["details"])
			encoded, _ := json.Marshal(map[string]string{"psiOutcome": metadataString(details, "psiOutcome")})
			r := &ToolResultMessage{Role: role, ToolCallID: metadataString(message, "toolCallId"), ToolName: metadataString(message, "toolName"), Details: encoded}
			_ = json.Unmarshal(message["isError"], &r.IsError)
			m.ToolResult = r
		case "bashExecution":
			b := &BashExecutionMessage{Role: role}
			_ = json.Unmarshal(message["cancelled"], &b.Cancelled)
			_ = json.Unmarshal(message["exitCode"], &b.ExitCode)
			m.BashExecution = b
		case "custom":
			m.Custom = &CustomMessage{Role: role, CustomType: metadataString(message, "customType")}
		case "branchSummary":
			m.BranchSummary = &BranchSummaryMessage{Role: role}
		case "compactionSummary":
			m.CompactionSummary = &CompactionSummaryMessage{Role: role}
		}
		e.Message = &SessionMessageEntry{Type: kind, ID: id, ParentID: parent, Timestamp: e.Base.Timestamp, Message: m}
	case "model_change":
		e.ModelChange = &ModelChangeEntry{Provider: metadataString(obj, "provider"), ModelID: metadataString(obj, "modelId")}
	case "thinking_level_change":
		e.ThinkingLevelChange = &ThinkingLevelChangeEntry{ThinkingLevel: metadataString(obj, "thinkingLevel")}
	case "compaction":
		e.Compaction = &CompactionEntry{FirstKeptEntryID: metadataString(obj, "firstKeptEntryId")}
		_ = json.Unmarshal(obj["tokensBefore"], &e.Compaction.TokensBefore)
		if usage := obj["usage"]; len(usage) != 0 && !metadataNull(usage) {
			u := metadataUsage(usage)
			e.Compaction.Usage = &u
		}
	case "branch_summary":
		e.BranchSummary = &BranchSummaryEntry{}
	case "context_edit":
		e.ContextEdit = &ContextEditEntry{TargetID: metadataString(obj, "targetId")}
	case "custom":
		e.Custom = &CustomEntry{CustomType: metadataString(obj, "customType")}
		if e.Custom.CustomType == "psi.input" {
			data, _ := metadataObject(obj["data"])
			e.provenanceTarget = metadataString(data, "targetId")
		}
	case "custom_message":
		c := &CustomMessageEntry{CustomType: metadataString(obj, "customType")}
		_ = json.Unmarshal(obj["display"], &c.Display)
		e.CustomMessage = c
		if c.CustomType == "psi.input" {
			details, _ := metadataObject(obj["details"])
			e.provenanceTarget = metadataString(details, "targetId")
		}
	case "usage":
		e.Usage = &UsageEntry{Usage: metadataUsage(obj["usage"])}
	case "label":
		e.Label = &LabelEntry{}
	case "session_info":
		e.SessionInfo = &SessionInfoEntry{}
		_ = json.Unmarshal(obj["name"], &e.SessionInfo.Name)
	}
	if kind == "message" {
		e.toolSummaries = metadataToolSummaries(data)
	}
	return e, nil, nil
}
