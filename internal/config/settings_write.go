package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsUpdate sets one settings.json value at a 1- or 2-element path.
// Path forms: {"outputPad"} or {"terminal", "trueColor"}.
type SettingsUpdate struct {
	Path  []string
	Value json.RawMessage
}

// UpdateUserSettings merges updates into the settings file at path.
//
// It preserves unknown keys, nested siblings, top-level key order, and the
// existing file mode. A malformed file is never overwritten. A no-op update
// does not touch the file. Writes go through a temp file in the same
// directory followed by a rename.
func UpdateUserSettings(path string, updates []SettingsUpdate) error {
	if path == "" {
		return fmt.Errorf("settings path is empty")
	}
	if len(updates) == 0 {
		return nil
	}
	for _, update := range updates {
		if len(update.Path) < 1 || len(update.Path) > 2 {
			return fmt.Errorf("settings path must have one or two elements")
		}
		for _, part := range update.Path {
			if part == "" {
				return fmt.Errorf("settings path element is empty")
			}
		}
		if !json.Valid(update.Value) {
			return fmt.Errorf("invalid value for %s", update.Path[len(update.Path)-1])
		}
	}

	target, err := resolveSettingsPath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	release, err := lockSettings(target)
	if err != nil {
		return err
	}
	defer release()

	values, order, err := readSettingsObject(target)
	if err != nil {
		return err
	}

	changed := false
	var extras []string
	for _, update := range updates {
		ok, err := applySettingsUpdate(values, update)
		if err != nil {
			return err
		}
		if ok {
			changed = true
			extras = appendKey(extras, order, update.Path[0])
		}
	}
	if !changed {
		return nil
	}

	compact := encodeSettingsObject(values, order, extras)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	return writeSettingsFile(target, pretty.Bytes())
}

// resolveSettingsPath resolves parent-directory symlinks and then the file
// itself. A missing file keeps the resolved parent path. A broken symlink is
// an error, so a rename never replaces a link with a regular file.
func resolveSettingsPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve settings path: %w", err)
	}
	dir, base := filepath.Split(absolute)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		absolute = filepath.Join(resolvedDir, base)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("resolve settings directory: %w", err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("resolve settings file: %w", err)
		}
		return resolved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("resolve settings file: %w", err)
	}
	return absolute, nil
}

func readSettingsObject(path string) (map[string]json.RawMessage, []string, error) {
	values := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return values, nil, nil
		}
		return nil, nil, fmt.Errorf("read settings: %w", err)
	}
	data = stripBOM(data)
	if !json.Valid(data) {
		return nil, nil, fmt.Errorf("%s: malformed JSON", path)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: malformed JSON", path)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("%s: settings must be a JSON object", path)
	}
	var order []string
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: malformed JSON", path)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, nil, fmt.Errorf("%s: malformed JSON", path)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, fmt.Errorf("%s: malformed JSON", path)
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, fmt.Errorf("%s: malformed JSON", path)
	}
	return values, order, nil
}

// applySettingsUpdate reports whether the stored value changed. The parent
// object of a nested update keeps its other children and their order.
func applySettingsUpdate(values map[string]json.RawMessage, update SettingsUpdate) (bool, error) {
	if len(update.Path) == 1 {
		key := update.Path[0]
		if current, ok := values[key]; ok && jsonEquivalent(current, update.Value) {
			return false, nil
		}
		values[key] = append(json.RawMessage(nil), update.Value...)
		return true, nil
	}
	parent, child := update.Path[0], update.Path[1]
	nested := map[string]json.RawMessage{}
	var nestedOrder []string
	if current, ok := values[parent]; ok && !jsonNull(current) {
		decoded, order, err := decodeSettingsObject(current)
		if err != nil {
			return false, fmt.Errorf("%s must be an object", parent)
		}
		nested, nestedOrder = decoded, order
	}
	if current, ok := nested[child]; ok && jsonEquivalent(current, update.Value) {
		return false, nil
	}
	nested[child] = append(json.RawMessage(nil), update.Value...)
	nestedOrder = appendKey(nestedOrder, nil, child)
	values[parent] = encodeSettingsObject(nested, nestedOrder, nil)
	return true, nil
}

func decodeSettingsObject(raw []byte) (map[string]json.RawMessage, []string, error) {
	values := map[string]json.RawMessage{}
	if !json.Valid(raw) {
		return nil, nil, fmt.Errorf("malformed JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("not an object")
	}
	var order []string
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, nil, fmt.Errorf("invalid key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, nil, err
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	return values, order, nil
}

// encodeSettingsObject writes keys in the recorded order, then any extras in
// update order. Values stay as compact JSON.
func encodeSettingsObject(values map[string]json.RawMessage, order, extras []string) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	seen := map[string]bool{}
	write := func(key string) {
		if seen[key] {
			return
		}
		value, ok := values[key]
		if !ok {
			return
		}
		seen[key] = true
		if !first {
			buf.WriteByte(',')
		}
		first = false
		encodedKey, _ := json.Marshal(key)
		buf.Write(encodedKey)
		buf.WriteByte(':')
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, value); err == nil {
			buf.Write(compacted.Bytes())
		} else {
			buf.Write(value)
		}
	}
	for _, key := range order {
		write(key)
	}
	for _, key := range extras {
		write(key)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func appendKey(target, other []string, key string) []string {
	for _, candidate := range target {
		if candidate == key {
			return target
		}
	}
	for _, candidate := range other {
		if candidate == key {
			return target
		}
	}
	return append(target, key)
}

func jsonEquivalent(a, b json.RawMessage) bool {
	var left, right bytes.Buffer
	if err := json.Compact(&left, a); err != nil {
		return false
	}
	if err := json.Compact(&right, b); err != nil {
		return false
	}
	return bytes.Equal(left.Bytes(), right.Bytes())
}

func jsonNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.Equal(trimmed, []byte("null"))
}

func writeSettingsFile(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".psi-settings-*")
	if err != nil {
		return fmt.Errorf("create settings temp file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("set settings permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
