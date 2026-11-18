package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GetOrCreateUserDeviceID returns the stable installation UUID stored as
// deviceId in the user settings file. Pi identifies each agent host to
// OpenAI as urn:uuid:<deviceId>, so psi keeps the same per-installation
// identity for its own ChatGPT sign-in.
//
// It uses the same Pi-compatible lease and atomic writer as other settings
// updates, preserving other keys, their order, and
// the file mode. A present ID is reused as-is; only the host-ID value sent
// to OpenAI is lowercased, never the stored text. A present but invalid ID
// is refused, never silently replaced, so a corrupted identity cannot fork
// into a second registered host. Project settings never supply this key.
func GetOrCreateUserDeviceID(ctx context.Context, userSettingsPath string) (string, error) {
	if userSettingsPath == "" {
		return "", fmt.Errorf("settings path is empty")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(userSettingsPath)
	if err != nil {
		return "", fmt.Errorf("resolve settings path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return "", fmt.Errorf("create settings directory: %w", err)
	}
	release, valid, err := lockUserSettings(ctx, absolute, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer release()
	target, err := resolveSettingsPath(absolute)
	if err != nil {
		return "", err
	}

	values, order, err := readSettingsObject(target)
	if err != nil {
		return "", err
	}
	if raw, ok := values["deviceId"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil || !validDeviceID(id) {
			return "", fmt.Errorf("settings deviceId is invalid; fix or remove it and retry the login")
		}
		return id, nil
	}
	id, err := randomDeviceID()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("encode settings: %w", err)
	}
	values["deviceId"] = encoded
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, encodeSettingsObject(values, order, appendKey(nil, order, "deviceId")), "", "  "); err != nil {
		return "", fmt.Errorf("encode settings: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := writeSettingsFile(target, pretty.Bytes(), valid); err != nil {
		return "", err
	}
	return id, nil
}

// validDeviceID accepts Pi's UUID shape: 36 characters of hexadecimal
// digits and hyphens, case-insensitively. Pi imposes no version
// restriction on a stored ID, so neither does this check.
func validDeviceID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := id[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

// randomDeviceID generates a cryptographically random UUIDv4. Byte 6 keeps
// its low four random bits and sets the high four to 0100 (version 4);
// byte 8 keeps its low six random bits and sets the high two to 10 (the
// RFC variant). The remaining 122 bits stay random.
func randomDeviceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate device ID: %w", err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
