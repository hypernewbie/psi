package main

import "strings"

// These SID strings identify the operating system and the local administrators
// group. Both are trusted file owners and trustees alongside the current user.
const (
	windowsSystemSID         = "S-1-5-18"
	windowsAdministratorsSID = "S-1-5-32-544"
)

// Windows file access masks: generic all/read/write, data and extended
// attribute access, file attribute access, and deletion or ACL/owner changes.
const (
	windowsGenericAll       uint32 = 0x10000000
	windowsGenericRead      uint32 = 0x80000000
	windowsGenericWrite     uint32 = 0x40000000
	windowsReadData         uint32 = 0x00000001
	windowsWriteData        uint32 = 0x00000002
	windowsAppendData       uint32 = 0x00000004
	windowsReadExtended     uint32 = 0x00000008
	windowsWriteExtended    uint32 = 0x00000010
	windowsDeleteChild      uint32 = 0x00000040
	windowsReadAttributes   uint32 = 0x00000080
	windowsWriteAttributes  uint32 = 0x00000100
	windowsDelete           uint32 = 0x00010000
	windowsWritePermissions uint32 = 0x00040000
	windowsWriteOwner       uint32 = 0x00080000
)

const (
	windowsSecretReadRights  = windowsGenericAll | windowsGenericRead | windowsReadData | windowsReadExtended | windowsReadAttributes
	windowsSecretWriteRights = windowsGenericAll | windowsGenericWrite | windowsWriteData | windowsAppendData |
		windowsWriteExtended | windowsDeleteChild | windowsWriteAttributes | windowsDelete | windowsWritePermissions | windowsWriteOwner
)

// ACE flag bits 0x01 through 0x10 describe inheritance, and 0x40 and 0x80
// describe audit results. The remaining bit is reserved and rejected.
const (
	windowsACEKnownFlags  = 0xdf
	windowsACEInheritOnly = 0x08
	windowsACEAllow       = 0
	windowsACEDeny        = 1
)

type windowsSecretACE struct {
	sid   string
	kind  uint8
	flags uint8
	mask  uint32
}

type windowsSecretACL struct {
	owner, user, appContainer string
	local, present, complete  bool
	entries                   []windowsSecretACE
}

// windowsSecretACLPrivate classifies already-verified descriptor facts. The
// native collector must verify the facts belong to the opened file handle.
func windowsSecretACLPrivate(acl windowsSecretACL) bool {
	if !acl.local || !acl.present || !acl.complete || acl.user == "" || acl.owner == "" {
		return false
	}
	trusted := func(sid string) bool {
		return strings.EqualFold(sid, acl.user) || strings.EqualFold(sid, windowsSystemSID) ||
			strings.EqualFold(sid, windowsAdministratorsSID) || (acl.appContainer != "" && strings.EqualFold(sid, acl.appContainer))
	}
	if !strings.EqualFold(acl.owner, acl.user) && !strings.EqualFold(acl.owner, windowsSystemSID) && !strings.EqualFold(acl.owner, windowsAdministratorsSID) {
		return false
	}
	for _, entry := range acl.entries {
		if entry.sid == "" || entry.flags & ^uint8(windowsACEKnownFlags) != 0 || (entry.kind != windowsACEAllow && entry.kind != windowsACEDeny) {
			return false
		}
		if entry.kind == windowsACEDeny || entry.flags&windowsACEInheritOnly != 0 || trusted(entry.sid) {
			continue
		}
		if entry.mask&(windowsSecretReadRights|windowsSecretWriteRights) != 0 {
			return false
		}
	}
	return true
}
