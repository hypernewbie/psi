// Package buildinfo reports the identity of the running psi binary.
//
// Go stamps a binary built inside a Git checkout with vcs.revision, vcs.time,
// and vcs.modified, and records the toolchain and target platform. /version
// therefore needs no ldflags and runs no git subprocess at runtime.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Info is the build identity behind /version. An empty Commit means the build
// carries no VCS stamp, for example `-buildvcs=false` or a module fetch.
type Info struct {
	Version    string
	Commit     string
	CommitTime string
	Modified   bool
	Toolchain  string
	OS         string
	Arch       string
}

// Read collects the stamps embedded in the running binary. The version string
// is the one main owns, because Go's module version is a pseudo-version when
// the binary is built from a checkout.
func Read(version string) Info {
	info := Info{Version: version, Toolchain: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	return stamp(info, bi.GoVersion, bi.Settings)
}

// stamp overlays the toolchain, platform, and VCS stamps Go recorded at build
// time. Read supplies the runtime defaults; tests supply synthetic settings.
func stamp(info Info, goVersion string, settings []debug.BuildSetting) Info {
	if goVersion != "" {
		info.Toolchain = goVersion
	}
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
			if len(info.Commit) > 7 {
				info.Commit = info.Commit[:7]
			}
		case "vcs.time":
			info.CommitTime = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		case "GOOS":
			info.OS = s.Value
		case "GOARCH":
			info.Arch = s.Value
		}
	}
	return info
}

// Lines renders the three-line report. Missing stamps degrade to readable
// text instead of empty values.
func (i Info) Lines() []string {
	version := i.Version
	if version == "" {
		version = "unknown"
	}
	commit := "unknown (no VCS stamp)"
	if i.Commit != "" {
		commit = i.Commit
		if i.CommitTime != "" {
			commit += " " + i.CommitTime
		}
		if i.Modified {
			commit += " (dirty)"
		}
	}
	platform := ""
	if i.OS != "" && i.Arch != "" {
		platform = i.OS + "/" + i.Arch
	}
	build := strings.TrimSpace(i.Toolchain + " " + platform)
	return []string{"psi " + version, "commit " + commit, "build " + build}
}
