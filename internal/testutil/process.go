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
	// Windows sockets fail without SystemRoot, and child tool lookup
	// needs PATHEXT. Keep both out of the cleared set on Windows.
	keep := map[string]string{}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "PATHEXT"} {
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
	// Windows has no fixed coreutils path; shell tests there use the
	// runner-provided bash, so PATH stays inherited on Windows.
	isolatedPath := "/usr/local/bin:/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		isolatedPath = os.Getenv("PATH")
	}
	for key, value := range map[string]string{
		"PATH": isolatedPath,
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
