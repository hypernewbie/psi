package session

// Pi reads session records with JSON.parse and typed property access: a
// property exists only when its key matches exactly, and an alias-case key
// ("ID", "ROLE") is invisible to the canonical lookup. This decoder keeps
// that rule and parses every object once: each level looks its fields up by
// exact key instead of re-serializing the record to hide alias keys.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// decodeExact decodes one JSON object into v with exact property names.
func decodeExact(data []byte, v any) error {
	obj, err := exactObject(data)
	if err != nil {
		return err
	}
	return decodeExactObject(obj, v)
}

// decodeExactObject fills v from an already parsed object.
func decodeExactObject(obj jsonView, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("decode target must be a non-nil pointer")
	}
	return exactFields(obj, rv.Elem())
}

// exactFields fills the exported fields of target from the parsed object.
// Every field is read by its exact JSON name; a missing key leaves the
// field at its zero value, exactly like Pi's typed property access.
func exactFields(obj jsonView, target reflect.Value) error {
	t := target.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue // unexported
		}
		key := field.Name
		if tag, ok := field.Tag.Lookup("json"); ok {
			name, _, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name != "" {
				key = name
			}
		}
		raw, ok := obj[key]
		if !ok {
			continue
		}
		value := target.Field(i)
		if !value.CanSet() {
			continue
		}
		if err := exactValue(raw, value); err != nil {
			return err
		}
	}
	return nil
}

// exactValue decodes one JSON value into target.
func exactValue(raw []byte, target reflect.Value) error {
	if target.Type() == reflect.TypeFor[json.RawMessage]() {
		target.SetBytes(append([]byte(nil), raw...))
		return nil
	}
	// Types with their own decoder keep their own exact rules.
	if target.CanAddr() {
		if u, ok := target.Addr().Interface().(json.Unmarshaler); ok {
			return u.UnmarshalJSON(raw)
		}
	}
	switch target.Kind() {
	case reflect.Pointer:
		if isJSONNull(raw) {
			return nil
		}
		elem := reflect.New(target.Type().Elem())
		if err := exactValue(raw, elem.Elem()); err != nil {
			return err
		}
		target.Set(elem)
		return nil
	case reflect.Struct:
		obj, err := splitObject(raw)
		if err != nil {
			return err
		}
		return exactFields(obj, target)
	case reflect.Slice:
		if isJSONNull(raw) {
			return nil
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		out := reflect.MakeSlice(target.Type(), len(items), len(items))
		for i, item := range items {
			if err := exactValue(item, out.Index(i)); err != nil {
				return err
			}
		}
		target.Set(out)
		return nil
	case reflect.Map:
		if isJSONNull(raw) {
			return nil
		}
		var items map[string]json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		out := reflect.MakeMap(target.Type())
		for key, item := range items {
			elem := reflect.New(target.Type().Elem()).Elem()
			if err := exactValue(item, elem); err != nil {
				return err
			}
			out.SetMapIndex(reflect.ValueOf(key).Convert(target.Type().Key()), elem)
		}
		target.Set(out)
		return nil
	case reflect.Interface:
		if target.NumMethod() != 0 {
			return fmt.Errorf("cannot decode into %s", target.Type())
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value == nil {
			return nil
		}
		target.Set(reflect.ValueOf(value))
		return nil
	default:
		return json.Unmarshal(raw, target.Addr().Interface())
	}
}

// isJSONNull reports whether a value is an explicit null.
func isJSONNull(raw []byte) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}
