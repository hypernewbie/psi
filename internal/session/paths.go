package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AgentDir returns the agent config directory.
// Precedence: PSI_CODING_AGENT_DIR, PI_CODING_AGENT_DIR (compat), ~/.pi/agent, ~/.config fallback via HOME.
func AgentDir() string {
	if v := os.Getenv("PSI_CODING_AGENT_DIR"); v != "" {
		return expandHome(v)
	}
	if v := os.Getenv("PI_CODING_AGENT_DIR"); v != "" {
		return expandHome(v)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".pi", "agent")
}

// SessionsRoot is <agentDir>/sessions.
func SessionsRoot(agentDir string) string {
	if agentDir == "" {
		agentDir = AgentDir()
	}
	return filepath.Join(agentDir, "sessions")
}

// SessionDirOverride resolves the effective session directory.
// Precedence: --session-dir flag > PSI/PI_CODING_AGENT_SESSION_DIR env > settings sessionDir > default per-cwd.
func SessionDirOverride(flagDir, envDir, settingsDir, cwd, agentDir string) string {
	if flagDir != "" {
		return resolveDir(flagDir, cwd)
	}
	if envDir != "" {
		return resolveDir(envDir, cwd)
	}
	if settingsDir != "" {
		// Relative session dirs resolve from the documented base (agent dir).
		if filepath.IsAbs(expandHome(settingsDir)) {
			return expandHome(settingsDir)
		}
		return filepath.Join(resolveDir(agentDir, cwd), expandHome(settingsDir))
	}
	return DefaultSessionDir(cwd, agentDir)
}

// DefaultSessionDir matches Pi workspace grouping:
// `<sessionsRoot>/--<cwd without leading slash, /:\ -> ->--`.
// Example: /home/user/proj -> .../sessions/--home-user-proj--
func DefaultSessionDir(cwd, agentDir string) string {
	root := SessionsRoot(agentDir)
	abs := cwd
	if !filepath.IsAbs(abs) {
		if joined, err := filepath.Abs(abs); err == nil {
			abs = joined
		}
	}
	// Pi resolves symlinks via resolvePath; filepath.EvalSymlinks where possible.
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		abs = eval
	}
	trimmed := strings.TrimPrefix(abs, "/")
	trimmed = strings.TrimPrefix(trimmed, "\\")
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-")
	safe := replacer.Replace(trimmed)
	return filepath.Join(root, "--"+safe+"--")
}

// SameDir reports whether two paths name one directory. Temporary directories
// on macOS sit behind a /var to /private/var symlink, so textual comparison
// alone can hide an existing session.
func SameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

func resolveDir(dir, base string) string {
	e := expandHome(dir)
	// Slash-rooted paths are absolute on Windows too, where filepath.IsAbs
	// would treat them as drive-relative and join them onto the base.
	if strings.HasPrefix(e, "/") || filepath.IsAbs(e) {
		return filepath.Clean(e)
	}
	if base == "" {
		if abs, err := filepath.Abs(e); err == nil {
			return abs
		}
		return e
	}
	return filepath.Join(base, e)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = os.Getenv("HOME")
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	if strings.HasPrefix(p, "file://") {
		return strings.TrimPrefix(p, "file://")
	}
	return p
}

// windowsDeviceNames are Win32 device names. psi names a session file after
// its session ID, and on Windows a file called CON.jsonl or NUL.jsonl
// resolves to a device instead of a file.
var windowsDeviceNames = map[string]bool{
	"CON": true,
	"PRN": true,
	"AUX": true,
	"NUL": true,
}

// isWindowsDeviceName reports whether an ID names a Win32 device. The check
// ignores case, so con and CON both qualify.
func isWindowsDeviceName(id string) bool {
	upper := strings.ToUpper(id)
	if windowsDeviceNames[upper] {
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) {
		return upper[3] >= '1' && upper[3] <= '9'
	}
	return false
}

// reservedSessionID reports whether an ID cannot be used as a session file
// name on this platform. Only Windows has device names; elsewhere every
// valid ID is a usable file name.
func reservedSessionID(id string) bool {
	return runtime.GOOS == "windows" && isWindowsDeviceName(id)
}
