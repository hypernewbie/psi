package main

import "testing"

func TestWindowsSecretACLPolicy(t *testing.T) {
	const (
		user         = "S-1-5-21-100-200-300-1000"
		foreign      = "S-1-5-21-100-200-300-1001"
		world        = "S-1-1-0"
		appContainer = "S-1-15-2-1-2-3-4-5-6-7"
		// The 0x20 ACE flag bit is reserved and must fail closed.
		reservedACEFlag = 0x20
	)
	base := windowsSecretACL{owner: user, user: user, local: true, present: true, complete: true}
	for _, tc := range []struct {
		name   string
		change func(*windowsSecretACL)
		want   bool
	}{
		{"owner access", func(a *windowsSecretACL) {
			a.entries = []windowsSecretACE{{sid: user, kind: windowsACEAllow, mask: windowsGenericAll}}
		}, true},
		{"system owner", func(a *windowsSecretACL) { a.owner = windowsSystemSID }, true},
		{"foreign owner", func(a *windowsSecretACL) { a.owner = foreign }, false},
		{"world read", func(a *windowsSecretACL) {
			a.entries = []windowsSecretACE{{sid: world, kind: windowsACEAllow, mask: windowsReadData}}
		}, false},
		{"foreign permission change", func(a *windowsSecretACL) {
			a.entries = []windowsSecretACE{{sid: foreign, kind: windowsACEAllow, mask: windowsWritePermissions}}
		}, false},
		{"inherit-only access", func(a *windowsSecretACL) {
			a.entries = []windowsSecretACE{{sid: world, kind: windowsACEAllow, flags: windowsACEInheritOnly, mask: windowsGenericAll}}
		}, true},
		{"deny entry", func(a *windowsSecretACL) {
			a.entries = []windowsSecretACE{{sid: world, kind: windowsACEDeny, mask: windowsGenericAll}}
		}, true},
		{"unsupported entry", func(a *windowsSecretACL) { a.entries = []windowsSecretACE{{sid: user, kind: 2}} }, false},
		{"unknown flags", func(a *windowsSecretACL) {
			a.entries = []windowsSecretACE{{sid: user, kind: windowsACEAllow, flags: reservedACEFlag}}
		}, false},
		{"missing descriptor", func(a *windowsSecretACL) { a.present = false }, false},
		{"incomplete descriptor", func(a *windowsSecretACL) { a.complete = false }, false},
		{"network path", func(a *windowsSecretACL) { a.local = false }, false},
		{"app container", func(a *windowsSecretACL) {
			a.appContainer = appContainer
			a.entries = []windowsSecretACE{{sid: appContainer, kind: windowsACEAllow, mask: windowsGenericAll}}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acl := base
			tc.change(&acl)
			if got := windowsSecretACLPrivate(acl); got != tc.want {
				t.Fatalf("private = %v, want %v", got, tc.want)
			}
		})
	}
}
