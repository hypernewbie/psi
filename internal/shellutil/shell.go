// Package shellutil resolves shell executables and invocation arguments.
// Windows discovery follows Pi; Unix deliberately keeps the inherited login
// shell instead of preferring Bash.
// Reference: temp/pi-mono/packages/coding-agent/src/utils/shell.ts
package shellutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// CommandTransport controls how a shell receives the command text.
type CommandTransport string

const (
	// TransportArgv passes the command text as the final command-line argument.
	TransportArgv CommandTransport = "argv"
	// TransportStdin writes the command text to standard input (used for the
	// legacy WSL bash.exe launcher to avoid Windows command-line quoting bugs).
	TransportStdin CommandTransport = "stdin"
)

// Config describes the shell executable, base flags, and command transport.
type Config struct {
	Shell            string
	Args             []string
	CommandTransport CommandTransport
}

// PowerShellArgs are the default PowerShell flags used by the powershell tool.
var PowerShellArgs = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}

type resolver struct {
	goos       string
	cwd        string
	getenv     func(string) string
	pathExists func(string) bool
	lookPath   func(string) string
	// lookupErr is the error-aware lookup used for explicit selectors. Nil
	// falls back to lookPath; production uses exec.LookPath on Unix so
	// exec.ErrDot stays a failed lookup.
	lookupErr func(string) (string, error)
}

func defaultResolver() resolver {
	r := resolver{
		goos:       runtime.GOOS,
		getenv:     os.Getenv,
		pathExists: platformPathExists,
		lookPath:   platformLookPath,
	}
	if runtime.GOOS != "windows" {
		r.lookupErr = exec.LookPath
	}
	return r
}

// GetShellConfig resolves the shell configuration for the host platform and
// an optional explicit shell path.
//
// Explicit shellPath or PSI_SHELL wins on every platform. On Windows the
// automatic order is Git Bash in ProgramFiles / ProgramFiles(x86), then
// bash.exe on PATH. On Unix the automatic order is the inherited SHELL, then
// sh; this is a deliberate psi difference from Pi's Bash-first order.
func GetShellConfig(customShellPath string) (Config, error) {
	return GetShellConfigForDir(customShellPath, "")
}

// GetShellConfigForDir resolves the shell for a command that will run in cwd.
// Relative explicit selectors anchor to cwd without lexical cleaning, so
// link/../shell keeps its native meaning.
func GetShellConfigForDir(customShellPath, cwd string) (Config, error) {
	r := defaultResolver()
	r.cwd = cwd
	return r.getShellConfig(customShellPath)
}

// GetPowerShellConfig resolves PowerShell on Windows, preferring PowerShell 7
// over Windows PowerShell.
func GetPowerShellConfig() (Config, error) {
	return defaultResolver().getPowerShellConfig()
}

// IsLegacyWslBashPath reports whether path names the legacy Windows System32
// or Sysnative WSL bash.exe launcher under the active Windows directory.
func IsLegacyWslBashPath(path string) bool {
	return isLegacyWslBashPath(path, os.Getenv("SystemRoot"))
}

// Win32Base returns the final path element using Windows path separators.
func Win32Base(path string) string {
	trimmed := strings.TrimRight(path, `\/`)
	if trimmed == "" {
		return ""
	}
	if idx := strings.LastIndexAny(trimmed, `\/`); idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}

// TaskkillPath returns the trusted System32 taskkill.exe path so process-tree
// cleanup on Windows does not depend on PATH.
func TaskkillPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return win32Join(root, "System32", "taskkill.exe")
}

// PathExists reports whether path exists on the host filesystem, accepting
// runnable Windows Store execution aliases on Windows.
func PathExists(path string) bool {
	return platformPathExists(path)
}

func (r resolver) getShellConfig(customShellPath string) (Config, error) {
	if customShellPath != "" {
		shell, err := r.resolveExplicit(customShellPath)
		if err != nil {
			return Config{}, fmt.Errorf("Custom shell path not found: %s: %w", customShellPath, err)
		}
		return r.bashShellConfig(shell), nil
	}

	if r.goos == "windows" {
		var candidates []string
		if programFiles := r.getenv("ProgramFiles"); programFiles != "" {
			candidates = append(candidates, programFiles+`\Git\bin\bash.exe`)
		}
		if programFilesX86 := r.getenv("ProgramFiles(x86)"); programFilesX86 != "" {
			candidates = append(candidates, programFilesX86+`\Git\bin\bash.exe`)
		}
		for _, candidate := range candidates {
			if r.pathExists(candidate) {
				return r.bashShellConfig(candidate), nil
			}
		}

		if bashOnPath := r.findExecutable("bash.exe"); bashOnPath != "" {
			return r.bashShellConfig(bashOnPath), nil
		}

		var searched strings.Builder
		for i, candidate := range candidates {
			if i > 0 {
				searched.WriteByte('\n')
			}
			searched.WriteString("  ")
			searched.WriteString(candidate)
		}
		return Config{}, fmt.Errorf(
			"No bash shell found. Options:\n"+
				"  1. Install Git for Windows: https://git-scm.com/download/win\n"+
				"  2. Add your bash to PATH (Cygwin, MSYS2, etc.)\n"+
				"  3. Set shellPath in settings.json\n\n"+
				"Searched Git Bash in:\n%s",
			searched.String(),
		)
	}

	// psi deliberately follows the inherited login-shell hint, then sh. It
	// does not prefer Bash; shellPath or PSI_SHELL selects Bash explicitly.
	// This is the approved Unix difference from Pi's Bash-first order.
	if inherited := r.getenv("SHELL"); inherited != "" {
		if shell, err := r.resolveExplicit(inherited); err == nil {
			return r.bashShellConfig(shell), nil
		}
	}
	shell, err := r.findExecutableChecked("sh")
	if err != nil {
		return Config{}, fmt.Errorf("no usable shell found: SHELL is unset or unusable and sh is unavailable: %w", err)
	}
	return Config{Shell: shell, Args: []string{"-c"}}, nil
}

// resolveExplicit resolves one executable selector. A selector with a
// separator anchors to the immutable command cwd without lexical cleaning;
// bare names use the platform PATH and never fall back to the working
// directory (exec.ErrDot is a failed lookup).
func (r resolver) resolveExplicit(selector string) (string, error) {
	if selector == "" {
		return "", errors.New("empty executable")
	}
	if r.goos == "windows" {
		switch {
		case isWindowsAbsolute(selector):
			if !r.pathExists(selector) {
				return "", fmt.Errorf("%s does not exist", selector)
			}
			return selector, nil
		case isWindowsRooted(selector), isWindowsDriveRelative(selector):
			// Native start keeps these forms; a bad one becomes an
			// infrastructure result there.
			return selector, nil
		case hasWindowsSeparator(selector):
			if r.cwd == "" {
				return "", errors.New("a relative shell path requires a working directory")
			}
			candidate := joinWindowsSelector(r.cwd, selector)
			if !r.pathExists(candidate) {
				return "", fmt.Errorf("%s does not exist", candidate)
			}
			return candidate, nil
		default:
			return r.findExecutableChecked(selector)
		}
	}
	if strings.ContainsRune(selector, os.PathSeparator) {
		path := selector
		if !filepath.IsAbs(path) {
			if r.cwd == "" {
				return "", errors.New("a relative shell path requires a working directory")
			}
			path = r.cwd + string(os.PathSeparator) + selector
		}
		if !r.pathExists(path) {
			return "", fmt.Errorf("%s does not exist", path)
		}
		return path, nil
	}
	return r.findExecutableChecked(selector)
}

// findExecutableChecked resolves a bare executable name on PATH. Any lookup
// error, including exec.ErrDot, is a failed lookup; the current directory is
// never admitted implicitly.
func (r resolver) findExecutableChecked(name string) (string, error) {
	var found string
	if r.lookupErr != nil {
		resolved, err := r.lookupErr(name)
		if err != nil {
			return "", err
		}
		found = resolved
	} else if r.lookPath != nil {
		found = r.lookPath(name)
	}
	if found == "" {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	if r.goos == "windows" && !r.pathExists(found) {
		return "", fmt.Errorf("%s is not executable", found)
	}
	return found, nil
}

func hasWindowsSeparator(path string) bool {
	return strings.ContainsAny(path, `\/`)
}

func isWindowsAbsolute(path string) bool {
	return isDriveRooted(path) || strings.HasPrefix(path, `\\`)
}

func isWindowsRooted(path string) bool {
	return len(path) > 0 && (path[0] == '\\' || path[0] == '/') && !strings.HasPrefix(path, `\\`)
}

func isWindowsDriveRelative(path string) bool {
	return len(path) >= 2 && path[1] == ':' && !isDriveRooted(path)
}

// joinWindowsSelector joins a relative selector to cwd without cleaning its
// components, so native path interpretation stays intact.
func joinWindowsSelector(cwd, selector string) string {
	trimmed := strings.TrimRight(cwd, `\/`)
	if trimmed == "" {
		return selector
	}
	return trimmed + `\` + selector
}
func (r resolver) getPowerShellConfig() (Config, error) {
	if r.goos != "windows" {
		return Config{}, errors.New("The powershell tool is only available on Windows.")
	}

	shell := r.findExecutable("pwsh.exe")
	// Installation directories may live on any drive and need not be on PATH.
	if shell == "" {
		if programFiles := r.getenv("ProgramFiles"); programFiles != "" {
			pwsh7 := win32Join(programFiles, "PowerShell", "7", "pwsh.exe")
			if r.pathExists(pwsh7) {
				shell = pwsh7
			}
		}
	}

	if shell == "" {
		shell = r.findExecutable("powershell.exe")
	}
	if shell == "" {
		if systemRoot := r.getenv("SystemRoot"); systemRoot != "" {
			windowsPowerShell := win32Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			if r.pathExists(windowsPowerShell) {
				shell = windowsPowerShell
			}
		}
	}

	if shell == "" {
		return Config{}, errors.New("No PowerShell executable found. Install PowerShell or add powershell.exe/pwsh.exe to PATH.")
	}

	return Config{Shell: shell, Args: slices.Clone(PowerShellArgs)}, nil
}

func (r resolver) findExecutable(name string) string {
	found := r.lookPath(name)
	if found == "" {
		return ""
	}
	if r.goos == "windows" && !r.pathExists(found) {
		return ""
	}
	return found
}

func (r resolver) bashShellConfig(shell string) Config {
	if isLegacyWslBashPath(shell, r.getenv("SystemRoot")) {
		return Config{Shell: shell, Args: []string{"-s"}, CommandTransport: TransportStdin}
	}
	return Config{Shell: shell, Args: []string{"-c"}}
}

func isLegacyWslBashPath(path, systemRoot string) bool {
	normalized := strings.ToLower(win32Normalize(path))
	if systemRoot != "" {
		if !isDriveRooted(systemRoot) {
			return false
		}
		for _, directory := range []string{"System32", "Sysnative"} {
			if normalized == strings.ToLower(win32Join(systemRoot, directory, "bash.exe")) {
				return true
			}
		}
		return false
	}
	// Fall back to conventional launcher paths only when the Windows directory is unknown.
	if len(normalized) < 3 || normalized[0] < 'a' || normalized[0] > 'z' || normalized[1] != ':' {
		return false
	}
	rest := normalized[2:]
	return rest == `\windows\system32\bash.exe` || rest == `\windows\sysnative\bash.exe`
}

func isDriveRooted(path string) bool {
	if len(path) < 3 {
		return false
	}
	drive := path[0]
	isLetter := (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
	return isLetter && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func win32Join(parts ...string) string {
	var nonEmpty []string
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	if len(nonEmpty) == 0 {
		return "."
	}
	return win32Normalize(strings.Join(nonEmpty, `\`))
}

func win32Normalize(path string) string {
	if path == "" {
		return "."
	}
	p := strings.ReplaceAll(path, "/", `\`)
	trailingSlash := strings.HasSuffix(p, `\`)

	var prefix string
	rooted := false
	if len(p) >= 2 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':' {
		prefix = p[:2]
		p = p[2:]
		if strings.HasPrefix(p, `\`) {
			rooted = true
			p = strings.TrimLeft(p, `\`)
		}
	} else if strings.HasPrefix(p, `\\`) {
		prefix = `\\`
		rooted = true
		p = strings.TrimLeft(p, `\`)
	} else if strings.HasPrefix(p, `\`) {
		rooted = true
		p = strings.TrimLeft(p, `\`)
	}

	var stack []string
	for _, seg := range strings.Split(p, `\`) {
		switch seg {
		case "", ".":
			continue
		case "..":
			if len(stack) > 0 && stack[len(stack)-1] != ".." {
				stack = stack[:len(stack)-1]
			} else if !rooted {
				stack = append(stack, "..")
			}
		default:
			stack = append(stack, seg)
		}
	}

	var b strings.Builder
	b.WriteString(prefix)
	if rooted && prefix != `\\` {
		b.WriteByte('\\')
	}
	b.WriteString(strings.Join(stack, `\`))
	if b.Len() == 0 {
		if trailingSlash {
			return `.\`
		}
		return "."
	}
	if trailingSlash && len(stack) > 0 {
		b.WriteByte('\\')
	}
	return b.String()
}
