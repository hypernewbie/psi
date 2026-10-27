package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunIsolated shields direct shell-tool regression tests from BASH_ENV, real
// credentials, proxies, provider opt-ins, and the user's configuration paths.
// Use from TestMain in packages whose tests run real local tools.
func RunIsolated(run func() int) int {
	root, err := os.MkdirTemp("/tmp", "psi-regression-*")
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
	os.Clearenv()
	for _, dir := range []string{"home", "tmp", "agent", "sessions"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			fmt.Fprintln(os.Stderr, "cannot create regression directories")
			return 1
		}
	}
	for key, value := range map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin",
		"HOME": filepath.Join(root, "home"), "TMPDIR": filepath.Join(root, "tmp"),
		"PI_CODING_AGENT_DIR": filepath.Join(root, "agent"), "PI_CODING_AGENT_SESSION_DIR": filepath.Join(root, "sessions"),
		"LANG": "C.UTF-8", "TERM": "xterm",
	} {
		os.Setenv(key, value)
	}
	// Probe markers for subprocess helpers survive isolation (P3-004).
	for _, entry := range saved {
		if strings.HasPrefix(entry, "PSI_UMASK_PROBE=") {
			os.Setenv("PSI_UMASK_PROBE", strings.TrimPrefix(entry, "PSI_UMASK_PROBE="))
		}
	}
	return run()
}
