package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A cancelled device-ID creation while a settings writer holds the shared
// lease must leave the existing settings intact.
func TestUserDeviceIDLockContentionFailsWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeSettingsFileForTest(t, path, `{"theme":"dark"}`, 0o600)
	release, _, err := lockUserSettings(context.Background(), path, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GetOrCreateUserDeviceID(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled device-ID creation = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"theme":"dark"}` {
		t.Fatalf("settings changed under contention: %q", data)
	}
}

// After release, creation writes one ID, reuse returns the same value, and
// an unrelated key survives the locked merge.
func TestUserDeviceIDCreateThenReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeSettingsFileForTest(t, path, `{"theme":"dark"}`, 0o600)
	first, err := GetOrCreateUserDeviceID(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !validDeviceID(first) {
		t.Fatalf("created ID has the wrong shape: %q", first)
	}
	second, err := GetOrCreateUserDeviceID(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("reuse returned %q, want %q", second, first)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"theme": "dark"`) || !strings.Contains(string(data), `"deviceId": "`+first+`"`) {
		t.Fatalf("unrelated key lost or ID missing: %s", data)
	}
}

// A present but invalid identity is refused, never silently replaced.
func TestUserDeviceIDRejectsInvalidStoredID(t *testing.T) {
	for _, stored := range []string{`"not-a-uuid"`, `""`, `null`, `42`} {
		path := filepath.Join(t.TempDir(), "settings.json")
		writeSettingsFileForTest(t, path, `{"deviceId":`+stored+`}`, 0o600)
		if id, err := GetOrCreateUserDeviceID(context.Background(), path); err == nil {
			t.Fatalf("stored %s returned %q", stored, id)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"deviceId":`+stored+`}` {
			t.Fatalf("invalid identity was rewritten: %q", data)
		}
	}
}

// A valid stored ID, including uppercase hex, is reused verbatim.
func TestUserDeviceIDReusesValidStoredID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	const stored = "12345678-9ABC-DEF0-1234-56789ABCDEF0"
	writeSettingsFileForTest(t, path, `{"deviceId":"`+stored+`"}`, 0o600)
	id, err := GetOrCreateUserDeviceID(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if id != stored {
		t.Fatalf("stored ID normalized to %q", id)
	}
}
