package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// RunIsolated shields direct shell-tool regression tests from BASH_ENV, real
// credentials, proxies, provider opt-ins, and the user's configuration paths.
// Use from TestMain in packages whose tests run real local tools.
func RunIsolated(run func() int) int {
	// Empty dir means the platform default temp location. /tmp does not
	// exist on Windows.
	root, err := os.MkdirTemp("", "psi-regression-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create isolated regression environment")
		return 1
	}
	defer os.RemoveAll(root)
	saved := os.Environ()
	defer func() {
		os.Clearenv()
		for _, entry := range saved {
			for i := range entry {
				if entry[i] == '=' {
					os.Setenv(entry[:i], entry[i+1:])
					break
				}
			}
		}
	}()
	// Windows sockets fail without SystemRoot, child tool lookup needs
	// PATHEXT, and shell tests need the parent PATH (no fixed coreutils
	// path exists). Keep them out of the cleared set on Windows. This
	// must read the parent environment before Clearenv below.
	keep := map[string]string{}
	// Unix tests use a fixed minimal PATH.
	keep["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "PATHEXT", "PATH"} {
			if v := os.Getenv(k); v != "" {
				keep[k] = v
			}
		}
	}
	os.Clearenv()
	for k, v := range keep {
		os.Setenv(k, v)
	}
	for _, dir := range []string{"home", "tmp", "agent", "sessions"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			fmt.Fprintln(os.Stderr, "cannot create regression directories")
			return 1
		}
	}
	for key, value := range map[string]string{
		"PATH": keep["PATH"],
		"HOME": filepath.Join(root, "home"), "TMPDIR": filepath.Join(root, "tmp"),
		"PI_CODING_AGENT_DIR": filepath.Join(root, "agent"), "PI_CODING_AGENT_SESSION_DIR": filepath.Join(root, "sessions"),
		"LANG": "C.UTF-8", "TERM": "xterm",
	} {
		os.Setenv(key, value)
	}
	if runtime.GOOS == "windows" {
		// Go and most Windows tools read TEMP/TMP, not TMPDIR.
		os.Setenv("TEMP", filepath.Join(root, "tmp"))
		os.Setenv("TMP", filepath.Join(root, "tmp"))
	}
	// Probe markers for subprocess helpers survive isolation (P3-004).
	for _, entry := range saved {
		if strings.HasPrefix(entry, "PSI_UMASK_PROBE=") {
			os.Setenv("PSI_UMASK_PROBE", strings.TrimPrefix(entry, "PSI_UMASK_PROBE="))
		}
	}
	return run()
}
