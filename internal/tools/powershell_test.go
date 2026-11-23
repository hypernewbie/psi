package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/shellutil"
)

func TestPowerShellArgsMatchPi(t *testing.T) {
	want := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}
	if !slices.Equal(shellutil.PowerShellArgs, want) {
		t.Fatalf("PowerShellArgs = %v, want %v", shellutil.PowerShellArgs, want)
	}
}

func TestPowerShellWrapCommand(t *testing.T) {
	pwshCfg := shellutil.Config{Shell: `C:\Program Files\PowerShell\7\pwsh.exe`, Args: shellutil.PowerShellArgs}
	winPSCfg := shellutil.Config{Shell: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Args: shellutil.PowerShellArgs}

	if got := wrapPowerShellCommand(pwshCfg, "Write-Output ok"); strings.Contains(got, "NativeCommandError") || !strings.HasPrefix(got, utf8OutputPrefix) {
		t.Fatalf("pwsh command wrapped unexpectedly: %q", got)
	}
	if got := wrapPowerShellCommand(winPSCfg, "Write-Output ok"); !strings.HasSuffix(got, windowsPowerShellExitSuffix) {
		t.Fatalf("Windows PowerShell command missing exit suffix: %q", got)
	}
	if got := wrapPowerShellCommand(winPSCfg, "Write-Output ok `"); strings.Contains(got, "NativeCommandError") {
		t.Fatalf("trailing-backtick command must not receive exit suffix: %q", got)
	}
}

func TestPowerShellNonWindowsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows availability check")
	}
	reg := NewRegistry(t.TempDir())
	res := reg.Dispatch(Invocation{
		ID:        "ps-unix",
		Name:      PowerShell,
		Arguments: map[string]any{"command": "Write-Output hi"},
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "only available on Windows") {
		t.Fatalf("expected Windows-only error, got %+v", res)
	}
}

func TestPowerShellExecutionUTF8AndPolicy(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only PowerShell execution test")
	}
	reg := NewRegistry(t.TempDir())
	res := reg.Dispatch(Invocation{
		ID:   "ps-utf8",
		Name: PowerShell,
		Arguments: map[string]any{
			"command": "Write-Output 'héllo €'; Get-ExecutionPolicy -Scope Process",
		},
	})
	if res.IsError {
		t.Fatalf("powershell command failed: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "héllo €") || !strings.Contains(text, "Bypass") {
		t.Fatalf("unexpected powershell output: %q", text)
	}
}

func findNativePowerShell(fileName string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	var candidates []string
	if cfg, err := shellutil.GetPowerShellConfig(); err == nil && strings.EqualFold(shellutil.Win32Base(cfg.Shell), fileName) {
		candidates = append(candidates, cfg.Shell)
	}
	if strings.EqualFold(fileName, "powershell.exe") {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		candidates = append(candidates, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", fileName))
	} else {
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		candidates = append(candidates, filepath.Join(pf, "PowerShell", "7", fileName))
	}
	for _, candidate := range candidates {
		if shellutil.PathExists(candidate) {
			return candidate
		}
	}
	return ""
}

// Transcribed from Pi's test/powershell-tool.test.ts (#8842): Windows PowerShell
// 5.1 must report the same exit codes as PowerShell 7 when native stderr is
// redirected, and unfinished statements must fail without running earlier code.
func TestPowerShellExitCodesMatchPi(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only PowerShell exit-code parity test")
	}

	const stderrExit0 = `cmd /c "echo progress 1>&2 & exit 0"`
	const stderrExit7 = `cmd /c "echo progress 1>&2 & exit 7"`
	cases := []struct {
		command string
		want    int
	}{
		{stderrExit0 + ` 2>&1 | Select-Object -Last 3`, 0},
		{stderrExit0 + ` 2>&1`, 0},
		{stderrExit0 + ` 2>$null`, 0},
		{`$output = ` + stderrExit0 + ` 2>&1`, 0},
		{stderrExit0, 0},
		{`Get-Item C:\pi-missing-8842; ` + stderrExit0 + ` 2>&1`, 0},
		{stderrExit7 + ` 2>&1 | Select-Object -Last 3`, 1},
		{stderrExit7, 1},
		{`cmd /c "exit 3"`, 1},
		{`Get-Item C:\pi-missing-8842`, 1},
		{stderrExit0 + ` 2>&1; Get-Item C:\pi-missing-8842`, 1},
		{`Write-Error "failed"`, 1},
		{`throw "failed"`, 1},
		{stderrExit7 + `; Write-Output done`, 0},
		{`Write-Output done # comment`, 0},
		{`Set-StrictMode -Version Latest; ` + stderrExit0 + ` 2>&1`, 0},
		{`exit 5`, 5},
	}
	unfinished := []string{
		"Write-Output done `",
		"Get-ChildItem |",
		"$true -and",
		"Write-Output a,",
		"$x =",
		"Write-Output (1 +",
		`Write-Output "unclosed`,
		"if ($true) {",
	}

	for _, fileName := range []string{"powershell.exe", "pwsh.exe"} {
		shellPath := findNativePowerShell(fileName)
		if shellPath == "" {
			continue
		}
		t.Run(fileName, func(t *testing.T) {
			t.Parallel()
			cwd := t.TempDir()
			newTool := func() *PowerShellTool {
				return &PowerShellTool{
					Cwd: cwd,
					resolveShell: func() (shellutil.Config, error) {
						return shellutil.Config{Shell: shellPath, Args: slices.Clone(shellutil.PowerShellArgs)}, nil
					},
				}
			}
			// Run independent exit-code probes with bounded concurrency so the
			// matrix stays fast on Windows CI runners.
			type check func()
			var checks []check
			for _, tc := range cases {
				tc := tc
				checks = append(checks, func() {
					res := newTool().ExecuteCtx(context.Background(), Invocation{
						ID:        "ps-exit",
						Name:      PowerShell,
						Arguments: map[string]any{"command": tc.command},
					})
					gotCode := 0
					if res.IsError {
						if details, ok := res.Details.(map[string]any); ok {
							if code, ok := details["exitCode"].(int); ok {
								gotCode = code
							} else {
								gotCode = 1
							}
						} else {
							gotCode = 1
						}
					}
					if gotCode != tc.want {
						t.Errorf("%s command %q: exit=%d, want %d (res=%+v)", fileName, tc.command, gotCode, tc.want, res)
					}
				})
			}
			for _, tail := range unfinished {
				tail := tail
				checks = append(checks, func() {
					cmd := `Write-Output ("pi-" + "ran"); ` + tail
					res := newTool().ExecuteCtx(context.Background(), Invocation{
						ID:        "ps-parse",
						Name:      PowerShell,
						Arguments: map[string]any{"command": cmd},
					})
					if !res.IsError || strings.Contains(res.Content[0].Text, "pi-ran") {
						t.Errorf("%s unfinished %q should fail without executing prefix: %+v", fileName, tail, res)
					}
				})
			}
			sem := make(chan struct{}, 6)
			done := make(chan struct{}, len(checks))
			for _, fn := range checks {
				sem <- struct{}{}
				go func(run check) {
					defer func() {
						<-sem
						done <- struct{}{}
					}()
					run()
				}(fn)
			}
			for range checks {
				<-done
			}
		})
	}
}
