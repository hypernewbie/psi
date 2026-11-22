//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func setTestSecretACL(t *testing.T, path, extraACE string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// Restrict the file to its current user, SYSTEM and Administrators. The
	// protected DACL prevents a parent directory from adding inherited ACEs.
	sddl := fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;SY)(A;;FA;;;BA)%s", user.User.Sid.String(), extraACE)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	goruntime.KeepAlive(descriptor)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowsSecretPathRejectsNetworkAndAliases(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{`C:\directory\secret`, true},
		{`\\?\C:\directory\secret`, true},
		{`\\server\share\secret`, false},
		{`\\?\UNC\server\share\secret`, false},
		{`\\.\C:\directory\secret`, false},
		{`C:\directory\secret:stream`, false},
		{`C:relative`, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			_, got := windowsSecretPathVolume(tc.path)
			if got != tc.want {
				t.Fatalf("admitted = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestWindowsFileTokenChecksDescriptorACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	setTestSecretACL(t, path, "")
	settings := &fileSettings{
		Channels: &channelsSettings{Discord: &discordSettings{Token: &secretRef{file: "value", provider: "local"}}},
		Secrets:  &secretsSettings{Providers: map[string]*secretProvider{"local": {Source: "file", Path: path, Mode: "singleValue"}}},
	}
	lookup := func(string) (string, bool) { return "", false }
	got, err := settings.resolveConfiguredToken(lookup)
	if err != nil || got != "synthetic-token" {
		t.Fatalf("private token resolution failed: %v", err)
	}
	// Grant Everyone read access; the same file must now be rejected.
	setTestSecretACL(t, path, "(A;;FR;;;WD)")
	if _, err := settings.resolveConfiguredToken(lookup); err == nil {
		t.Fatal("accepted a file readable by Everyone")
	}
}
