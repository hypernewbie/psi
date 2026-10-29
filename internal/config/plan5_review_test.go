package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReviewConfiguredBareHomeThemePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	actualHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "!", "+", "-"} {
		got := resolveConfiguredResourcePath(filepath.Join(home, "agent"), prefix+"~")
		if got != prefix+actualHome {
			t.Errorf("%q resolved to %q, want %q", prefix+"~", got, prefix+actualHome)
		}
	}
}
