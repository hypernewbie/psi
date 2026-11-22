//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Token information classes for the app-container flag and its SID, as
// defined by the Windows TOKEN_INFORMATION_CLASS enumeration.
const (
	tokenIsAppContainer  = 29
	tokenAppContainerSID = 31
)

var windowsAppContainerSID = regexp.MustCompile(`(?i)^S-1-15-2(?:-[0-9]+){7,}$`)

func windowsTokenAppContainer(token windows.Token) (string, error) {
	var inContainer uint32
	var size uint32
	err := windows.GetTokenInformation(token, tokenIsAppContainer, (*byte)(unsafe.Pointer(&inContainer)), uint32(unsafe.Sizeof(inContainer)), &size)
	if err != nil || size != uint32(unsafe.Sizeof(inContainer)) {
		return "", fmt.Errorf("token information unavailable")
	}
	if inContainer == 0 {
		return "", nil
	}
	var required uint32
	err = windows.GetTokenInformation(token, tokenAppContainerSID, nil, 0, &required)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || required < 8 || required > 1<<20 {
		return "", fmt.Errorf("token information unavailable")
	}
	buffer := make([]byte, required)
	if err := windows.GetTokenInformation(token, tokenAppContainerSID, &buffer[0], required, &size); err != nil || size < 8 || size > required {
		return "", fmt.Errorf("token information unavailable")
	}
	// TOKEN_APPCONTAINER_INFORMATION contains a SID pointer into the returned
	// buffer. Check its extent before asking Windows to inspect the SID.
	sid := *(**windows.SID)(unsafe.Pointer(&buffer[0]))
	ptr := uintptr(unsafe.Pointer(sid))
	base := uintptr(unsafe.Pointer(&buffer[0]))
	if sid == nil || ptr < base || ptr-base > uintptr(size)-8 {
		return "", fmt.Errorf("token information unavailable")
	}
	remaining := uintptr(size) - (ptr - base)
	if uintptr(8+4*int(sid.SubAuthorityCount())) > remaining || !sid.IsValid() {
		return "", fmt.Errorf("token information unavailable")
	}
	value := sid.String()
	goruntime.KeepAlive(buffer)
	if !windowsAppContainerSID.MatchString(value) {
		return "", fmt.Errorf("token information unavailable")
	}
	return value, nil
}

func windowsDescriptorFacts(handle windows.Handle) (windowsSecretACL, error) {
	var facts windowsSecretACL
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return facts, fmt.Errorf("security descriptor unavailable")
	}
	facts.user = user.User.Sid.String()
	facts.appContainer, err = windowsTokenAppContainer(token)
	if err != nil {
		return facts, err
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return facts, fmt.Errorf("security descriptor unavailable")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return facts, fmt.Errorf("security descriptor unavailable")
	}
	facts.owner = owner.String()
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return facts, fmt.Errorf("security descriptor unavailable")
	}
	facts.local, facts.present, facts.complete = true, true, true
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil || ace == nil {
			return windowsSecretACL{}, fmt.Errorf("security descriptor unavailable")
		}
		if ace.Header.AceType != windowsACEAllow && ace.Header.AceType != windowsACEDeny {
			return windowsSecretACL{}, fmt.Errorf("security descriptor unavailable")
		}
		sidOffset := unsafe.Offsetof(ace.SidStart)
		if uintptr(ace.Header.AceSize) < sidOffset+8 {
			return windowsSecretACL{}, fmt.Errorf("security descriptor unavailable")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if uintptr(8+4*int(sid.SubAuthorityCount())) > uintptr(ace.Header.AceSize)-sidOffset || !sid.IsValid() {
			return windowsSecretACL{}, fmt.Errorf("security descriptor unavailable")
		}
		facts.entries = append(facts.entries, windowsSecretACE{sid: sid.String(), kind: ace.Header.AceType, flags: ace.Header.AceFlags, mask: uint32(ace.Mask)})
	}
	goruntime.KeepAlive(descriptor)
	return facts, nil
}

// windowsSecretPathVolume rejects network paths, device namespaces, and
// alternate data streams before any filesystem call can contact a host. An
// extended-length local drive path (\\?\C:\...) is permitted.
func windowsSecretPathVolume(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	spelling := strings.ReplaceAll(path, "/", `\`)
	if strings.HasPrefix(spelling, `\\?\`) {
		spelling = spelling[4:]
	}
	if len(spelling) < 3 || spelling[1] != ':' || spelling[2] != '\\' || strings.Contains(spelling[2:], ":") {
		return "", false
	}
	drive := spelling[0]
	if !(drive >= 'A' && drive <= 'Z') && !(drive >= 'a' && drive <= 'z') {
		return "", false
	}
	return spelling[:2], true
}

func windowsLocalSecretPath(path string, handle windows.Handle) bool {
	volume, ok := windowsSecretPathVolume(path)
	if !ok {
		return false
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return false
	}
	switch windows.GetDriveType(root) {
	case windows.DRIVE_UNKNOWN, windows.DRIVE_NO_ROOT_DIR, windows.DRIVE_REMOTE:
		return false
	}
	buffer := make([]uint16, windows.MAX_PATH)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return false
	}
	if length >= uint32(len(buffer)) {
		buffer = make([]uint16, length+1)
		length, err = windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil || length >= uint32(len(buffer)) {
			return false
		}
	}
	return !strings.HasPrefix(strings.ToUpper(windows.UTF16ToString(buffer[:length])), `\\?\UNC\`)
}

func windowsSecretFileInfo(handle windows.Handle) (windows.ByHandleFileInformation, bool) {
	var info windows.ByHandleFileInformation
	kind, err := windows.GetFileType(handle)
	if err != nil || kind != windows.FILE_TYPE_DISK || windows.GetFileInformationByHandle(handle, &info) != nil {
		return info, false
	}
	return info, info.NumberOfLinks == 1 && info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) == 0
}

func sameWindowsSecretFile(a, b windows.ByHandleFileInformation) bool {
	return a.VolumeSerialNumber == b.VolumeSerialNumber && a.FileIndexHigh == b.FileIndexHigh && a.FileIndexLow == b.FileIndexLow
}

func readPrivateSecretFile(path string, maxBytes int64, timeout time.Duration) ([]byte, error) {
	if _, ok := windowsSecretPathVolume(path); !ok {
		return nil, fmt.Errorf("secret file is unavailable")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("secret file is unavailable")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("secret file is unavailable")
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("secret file is unavailable")
	}
	file := os.NewFile(uintptr(handle), "secret file")
	if file == nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("secret file is unavailable")
	}
	opened, err := file.Stat()
	info, valid := windowsSecretFileInfo(handle)
	if err != nil || !valid || !os.SameFile(before, opened) || opened.Size() > maxBytes || !windowsLocalSecretPath(path, handle) {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil || !os.SameFile(opened, resolvedInfo) {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	acl, err := windowsDescriptorFacts(handle)
	if err != nil || !windowsSecretACLPrivate(acl) {
		file.Close()
		return nil, fmt.Errorf("secret file is unavailable")
	}
	return readSecretHandle(file, maxBytes, timeout, func() bool {
		final, valid := windowsSecretFileInfo(handle)
		return valid && sameWindowsSecretFile(info, final)
	})
}
