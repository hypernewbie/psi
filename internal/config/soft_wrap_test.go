package config

import (
	"path/filepath"
	"testing"
)

func loadSoftWrap(t *testing.T, body string) (Settings, error) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".pi", "agent", "settings.json")
	writeJSON(t, user, body)
	loaded, err := LoadSettings(user, "", true)
	return loaded.Settings, err
}

func TestSoftWrapDefaultsToOn(t *testing.T) {
	settings, err := loadSoftWrap(t, `{"outputPad":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.SoftWrap {
		t.Fatal("softWrap must default to true")
	}
}

func TestSoftWrapLoadsExplicitFalse(t *testing.T) {
	settings, err := loadSoftWrap(t, `{"softWrap":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if settings.SoftWrap {
		t.Fatal("softWrap=false was not loaded")
	}
}

func TestSoftWrapRejectsNullAndNonBoolean(t *testing.T) {
	for _, body := range []string{`{"softWrap":null}`, `{"softWrap":"yes"}`, `{"softWrap":1}`} {
		if _, err := loadSoftWrap(t, body); err == nil {
			t.Fatalf("expected an error for %s", body)
		}
	}
}
