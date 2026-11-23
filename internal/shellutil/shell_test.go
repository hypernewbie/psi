package shellutil

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestIsLegacyWslBashPath(t *testing.T) {
	// Transcribed from Pi's test/windows-shell-config.test.ts (#9490).
	cases := []struct {
		systemRoot string
		shell      string
		wantStdin  bool
	}{
		{`D:\WinNT\`, `d:/WINNT/System32/BASH.EXE`, true},
		{`D:\WinNT`, `D:\WinNT\Sysnative\bash.exe`, true},
		{"", `E:\Windows\System32\bash.exe`, true},
		{`D:\WinNT`, `D:\Git\bin\bash.exe`, false},
		{`D:\WinNT`, `E:\Windows\System32\bash.exe`, false},
		{`WinNT`, `C:\Windows\System32\bash.exe`, false},
	}
	for _, tc := range cases {
		r := resolver{
			goos: "windows",
			getenv: func(key string) string {
				if key == "SystemRoot" {
					return tc.systemRoot
				}
				return ""
			},
			pathExists: func(string) bool { return true },
			lookPath:   func(string) string { return "" },
		}
		got, err := r.getShellConfig(tc.shell)
		if err != nil {
			t.Fatalf("getShellConfig(%q) with SystemRoot=%q failed: %v", tc.shell, tc.systemRoot, err)
		}
		want := Config{Shell: tc.shell, Args: []string{"-c"}}
		if tc.wantStdin {
			want = Config{Shell: tc.shell, Args: []string{"-s"}, CommandTransport: TransportStdin}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("getShellConfig(%q) with SystemRoot=%q = %+v, want %+v", tc.shell, tc.systemRoot, got, want)
		}
	}
}

func TestWindowsShellResolutionPrefersGitBashBeforePath(t *testing.T) {
	gitBash := `C:\Program Files\Git\bin\bash.exe`
	wslBash := `C:\Windows\System32\bash.exe`
	r := resolver{
		goos: "windows",
		getenv: func(key string) string {
			switch key {
			case "ProgramFiles":
				return `C:\Program Files`
			case "SystemRoot":
				return `C:\Windows`
			default:
				return ""
			}
		},
		pathExists: func(p string) bool { return p == gitBash || p == wslBash },
		lookPath: func(name string) string {
			if name == "bash.exe" {
				return wslBash
			}
			return ""
		},
	}
	got, err := r.getShellConfig("")
	if err != nil {
		t.Fatalf("getShellConfig failed: %v", err)
	}
	want := Config{Shell: gitBash, Args: []string{"-c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("getShellConfig = %+v, want %+v", got, want)
	}
}

func TestWindowsShellResolutionFallsBackToPathAndErrorsWhenMissing(t *testing.T) {
	wslBash := `C:\Windows\System32\bash.exe`
	r := resolver{
		goos: "windows",
		getenv: func(key string) string {
			switch key {
			case "ProgramFiles":
				return `C:\Program Files`
			case "ProgramFiles(x86)":
				return `C:\Program Files (x86)`
			case "SystemRoot":
				return `C:\Windows`
			default:
				return ""
			}
		},
		pathExists: func(p string) bool { return p == wslBash },
		lookPath: func(name string) string {
			if name == "bash.exe" {
				return wslBash
			}
			return ""
		},
	}
	got, err := r.getShellConfig("")
	if err != nil {
		t.Fatalf("fallback to legacy WSL bash failed: %v", err)
	}
	want := Config{Shell: wslBash, Args: []string{"-s"}, CommandTransport: TransportStdin}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("getShellConfig = %+v, want %+v", got, want)
	}

	r.pathExists = func(string) bool { return false }
	r.lookPath = func(string) string { return "" }
	_, err = r.getShellConfig("")
	if err == nil || !strings.Contains(err.Error(), "No bash shell found") || !strings.Contains(err.Error(), `C:\Program Files\Git\bin\bash.exe`) {
		t.Fatalf("missing bash error = %v, want searchable Git Bash guidance", err)
	}
}

func TestWindowsStoreAliasesAndUnixChecks(t *testing.T) {
	// Transcribed from Pi's test/shell-path-exists.test.ts.
	aliasesDir := `D:\Users\test\AppData\Local\Microsoft\WindowsApps`

	for _, exe := range []string{"bash.exe", "pwsh.exe"} {
		alias := aliasesDir + `\` + exe
		r := resolver{
			goos:       "windows",
			getenv:     func(string) string { return "" },
			pathExists: func(p string) bool { return p == alias },
			lookPath: func(name string) string {
				if name == exe {
					return alias
				}
				return ""
			},
		}
		var cfg Config
		var err error
		if exe == "pwsh.exe" {
			cfg, err = r.getPowerShellConfig()
		} else {
			cfg, err = r.getShellConfig("")
		}
		if err != nil || cfg.Shell != alias {
			t.Fatalf("%s alias resolution = (%+v, %v), want shell %q", exe, cfg, err, alias)
		}
	}

	// Explicitly configured alias.
	explicitAlias := aliasesDir + `\bash.exe`
	rExplicit := resolver{
		goos:       "windows",
		getenv:     func(string) string { return "" },
		pathExists: func(p string) bool { return p == explicitAlias },
		lookPath:   func(string) string { return "" },
	}
	if cfg, err := rExplicit.getShellConfig(explicitAlias); err != nil || cfg.Shell != explicitAlias {
		t.Fatalf("explicit alias = (%+v, %v), want %q", cfg, err, explicitAlias)
	}

	// Rejects PATH results that fail existence check.
	windowsPS := `D:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	rReject := resolver{
		goos:   "windows",
		getenv: func(string) string { return "" },
		pathExists: func(p string) bool {
			return p == windowsPS
		},
		lookPath: func(name string) string {
			switch name {
			case "pwsh.exe":
				return aliasesDir + `\pwsh.exe`
			case "powershell.exe":
				return windowsPS
			default:
				return ""
			}
		},
	}
	if cfg, err := rReject.getPowerShellConfig(); err != nil || cfg.Shell != windowsPS {
		t.Fatalf("rejected pwsh alias fallback = (%+v, %v), want %q", cfg, err, windowsPS)
	}

	// Unix resolution: /bin/bash -> bash on PATH -> sh, and missing explicit path fails.
	rUnix := resolver{
		goos:       "linux",
		getenv:     func(string) string { return "" },
		pathExists: func(p string) bool { return p == "/bin/bash" },
		lookPath:   func(string) string { return "" },
	}
	if cfg, err := rUnix.getShellConfig(""); err != nil || cfg.Shell != "/bin/bash" {
		t.Fatalf("unix /bin/bash = (%+v, %v)", cfg, err)
	}
	rUnix.pathExists = func(string) bool { return false }
	rUnix.lookPath = func(name string) string {
		if name == "bash" {
			return "/usr/local/bin/bash"
		}
		return ""
	}
	if cfg, err := rUnix.getShellConfig(""); err != nil || cfg.Shell != "/usr/local/bin/bash" {
		t.Fatalf("unix PATH bash = (%+v, %v)", cfg, err)
	}
	rUnix.lookPath = func(string) string { return "" }
	if cfg, err := rUnix.getShellConfig(""); err != nil || cfg.Shell != "sh" {
		t.Fatalf("unix sh fallback = (%+v, %v)", cfg, err)
	}
	if _, err := rUnix.getShellConfig("/missing/bash"); err == nil || !strings.Contains(err.Error(), "Custom shell path not found: /missing/bash") {
		t.Fatalf("missing custom shell error = %v", err)
	}
}

func TestGetPowerShellConfig(t *testing.T) {
	// Transcribed from Pi's test/powershell-config.test.ts (#9490).
	pwsh7 := `D:\Applications\PowerShell\7\pwsh.exe`
	windowsPS := `E:\WinNT\System32\WindowsPowerShell\v1.0\powershell.exe`
	baseEnv := func(key string) string {
		switch key {
		case "ProgramFiles":
			return `D:\Applications`
		case "SystemRoot":
			return `E:\WinNT`
		default:
			return ""
		}
	}

	t.Run("non-windows rejected", func(t *testing.T) {
		r := resolver{goos: "linux", getenv: baseEnv, pathExists: func(string) bool { return true }, lookPath: func(string) string { return "" }}
		if _, err := r.getPowerShellConfig(); err == nil || !strings.Contains(err.Error(), "only available on Windows") {
			t.Fatalf("non-windows error = %v", err)
		}
	})

	t.Run("prefers pwsh on PATH over known install", func(t *testing.T) {
		shell := `E:\Tools\pwsh.exe`
		r := resolver{
			goos:       "windows",
			getenv:     baseEnv,
			pathExists: func(string) bool { return true },
			lookPath: func(name string) string {
				if name == "pwsh.exe" {
					return shell
				}
				return ""
			},
		}
		got, err := r.getPowerShellConfig()
		if err != nil || got.Shell != shell || !reflect.DeepEqual(got.Args, PowerShellArgs) {
			t.Fatalf("got (%+v, %v), want shell %q", got, err, shell)
		}
	})

	t.Run("finds pwsh 7 under ProgramFiles before Windows PowerShell on PATH", func(t *testing.T) {
		r := resolver{
			goos:   "windows",
			getenv: baseEnv,
			pathExists: func(p string) bool {
				return p == pwsh7 || p == windowsPS
			},
			lookPath: func(name string) string {
				if name == "powershell.exe" {
					return windowsPS
				}
				return ""
			},
		}
		got, err := r.getPowerShellConfig()
		if err != nil || got.Shell != pwsh7 {
			t.Fatalf("got (%+v, %v), want %q", got, err, pwsh7)
		}
	})

	t.Run("prefers Windows PowerShell on PATH over system install", func(t *testing.T) {
		shell := `E:\Tools\powershell.exe`
		r := resolver{
			goos:   "windows",
			getenv: baseEnv,
			pathExists: func(p string) bool {
				return p == shell || p == windowsPS
			},
			lookPath: func(name string) string {
				if name == "powershell.exe" {
					return shell
				}
				return ""
			},
		}
		got, err := r.getPowerShellConfig()
		if err != nil || got.Shell != shell {
			t.Fatalf("got (%+v, %v), want %q", got, err, shell)
		}
	})

	t.Run("finds Windows PowerShell under SystemRoot when PATH lookup misses", func(t *testing.T) {
		r := resolver{
			goos:       "windows",
			getenv:     baseEnv,
			pathExists: func(p string) bool { return p == windowsPS },
			lookPath:   func(string) string { return "" },
		}
		got, err := r.getPowerShellConfig()
		if err != nil || got.Shell != windowsPS {
			t.Fatalf("got (%+v, %v), want %q", got, err, windowsPS)
		}
	})

	t.Run("reports error when no PowerShell exists", func(t *testing.T) {
		r := resolver{
			goos:       "windows",
			getenv:     baseEnv,
			pathExists: func(string) bool { return false },
			lookPath:   func(string) string { return "" },
		}
		if _, err := r.getPowerShellConfig(); err == nil || !strings.Contains(err.Error(), "No PowerShell executable found") {
			t.Fatalf("error = %v, want missing PowerShell message", err)
		}
	})
}

func TestNativeWindowsPrefersGitBashWhenInstalled(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows check")
	}
	pf := os.Getenv("ProgramFiles")
	gitBash := filepath.Join(pf, "Git", "bin", "bash.exe")
	if !platformPathExists(gitBash) {
		t.Skip("Git Bash not installed under ProgramFiles")
	}
	cfg, err := GetShellConfig("")
	if err != nil {
		t.Fatalf("GetShellConfig failed: %v", err)
	}
	if !strings.EqualFold(cfg.Shell, gitBash) {
		t.Fatalf("GetShellConfig = %q, want Git Bash %q (must not fall through to WSL bash.exe)", cfg.Shell, gitBash)
	}
	if cfg.CommandTransport == TransportStdin {
		t.Fatalf("Git Bash should use argv transport, got %+v", cfg)
	}
}
