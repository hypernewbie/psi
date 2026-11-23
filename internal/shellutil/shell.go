// Package shellutil resolves shell executables and invocation arguments
// following Pi's cross-platform shell rules.
// Reference: temp/pi-mono/packages/coding-agent/src/utils/shell.ts
package shellutil

import (
	"errors"
	"fmt"
	"os"
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
	getenv     func(string) string
	pathExists func(string) bool
	lookPath   func(string) string
}

func defaultResolver() resolver {
	return resolver{
		goos:       runtime.GOOS,
		getenv:     os.Getenv,
		pathExists: platformPathExists,
		lookPath:   platformLookPath,
	}
}

// GetShellConfig resolves the bash shell configuration for the host platform
// and an optional explicit shell path.
//
// Resolution order matches Pi:
//   - User-specified shellPath (or PSI_SHELL fallback supplied by callers)
//   - On Windows: Git Bash in ProgramFiles / ProgramFiles(x86), then bash.exe on PATH
//   - On Unix: /bin/bash, then bash on PATH, then sh
func GetShellConfig(customShellPath string) (Config, error) {
	return defaultResolver().getShellConfig(customShellPath)
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
		// Resolve a bare executable name (such as PSI_SHELL=bash) on PATH first
		// so a same-named entry in the working directory cannot shadow it.
		if !strings.ContainsAny(customShellPath, `/\`) {
			if found := r.findExecutable(customShellPath); found != "" {
				return r.bashShellConfig(found), nil
			}
		}
		if r.pathExists(customShellPath) {
			return r.bashShellConfig(customShellPath), nil
		}
		return Config{}, fmt.Errorf("Custom shell path not found: %s", customShellPath)
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

	if r.pathExists("/bin/bash") {
		return r.bashShellConfig("/bin/bash"), nil
	}
	if bashOnPath := r.findExecutable("bash"); bashOnPath != "" {
		return r.bashShellConfig(bashOnPath), nil
	}
	return Config{Shell: "sh", Args: []string{"-c"}}, nil
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
