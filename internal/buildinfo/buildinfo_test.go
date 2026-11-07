package buildinfo

import (
	"reflect"
	"runtime/debug"
	"testing"
)

// CLI version output prints three lines: release, commit (with build-time tree state),
// and toolchain. Missing stamps must stay readable instead of empty.
func TestInfoLines(t *testing.T) {
	cases := []struct {
		name string
		info Info
		want []string
	}{
		{
			name: "stamped dirty build",
			info: Info{Version: "0.2.0", Commit: "bd2371b", CommitTime: "2026-11-03T13:25:16Z", Modified: true, Toolchain: "go1.26.5", OS: "darwin", Arch: "arm64"},
			want: []string{"psi 0.2.0", "commit bd2371b 2026-11-03T13:25:16Z (dirty)", "build go1.26.5 darwin/arm64"},
		},
		{
			name: "stamped clean build",
			info: Info{Version: "0.2.0", Commit: "bd2371b", CommitTime: "2026-11-03T13:25:16Z", Toolchain: "go1.26.5", OS: "linux", Arch: "amd64"},
			want: []string{"psi 0.2.0", "commit bd2371b 2026-11-03T13:25:16Z", "build go1.26.5 linux/amd64"},
		},
		{
			name: "no VCS stamp",
			info: Info{Version: "0.2.0", Toolchain: "go1.26.5", OS: "windows", Arch: "arm64"},
			want: []string{"psi 0.2.0", "commit unknown (no VCS stamp)", "build go1.26.5 windows/arm64"},
		},
		{
			name: "commit without time",
			info: Info{Version: "0.2.0", Commit: "bd2371b", Toolchain: "go1.26.5", OS: "darwin", Arch: "arm64"},
			want: []string{"psi 0.2.0", "commit bd2371b", "build go1.26.5 darwin/arm64"},
		},
		{
			name: "missing version and platform",
			info: Info{Toolchain: "go1.26.5"},
			want: []string{"psi unknown", "commit unknown (no VCS stamp)", "build go1.26.5"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.info.Lines(); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Lines() = %q, want %q", got, c.want)
			}
		})
	}
}

// Go's settings carry the full revision, the tree state as a string, and the
// target platform. The report keeps a short revision and must follow the
// stamped platform rather than the platform running the tests.
func TestStampReadsBuildSettings(t *testing.T) {
	info := stamp(Info{Version: "0.2.0"}, "go1.26.5", []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "bd2371bb894b3955c5961b9eb5fd51d5e4557b85"},
		{Key: "vcs.time", Value: "2026-11-03T13:25:16Z"},
		{Key: "vcs.modified", Value: "true"},
		{Key: "GOOS", Value: "linux"},
		{Key: "GOARCH", Value: "amd64"},
	})
	want := Info{Version: "0.2.0", Commit: "bd2371b", CommitTime: "2026-11-03T13:25:16Z", Modified: true, Toolchain: "go1.26.5", OS: "linux", Arch: "amd64"}
	if !reflect.DeepEqual(info, want) {
		t.Fatalf("stamp() = %+v, want %+v", info, want)
	}

	// A clean tree and a revision shorter than the display width survive as-is.
	info = stamp(Info{}, "", []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc1234"},
		{Key: "vcs.modified", Value: "false"},
	})
	if info.Commit != "abc1234" || info.Modified {
		t.Errorf("stamp() = %+v, want the full short revision and a clean tree", info)
	}
}
