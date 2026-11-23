package tools

import (
	"context"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/shellutil"
)

// Transcribed from Pi's packages/coding-agent/src/core/tools/powershell.ts.
const (
	utf8OutputPrefix = "try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}\n"

	// Windows PowerShell 5.1 turns redirected native stderr into error records and
	// sets $? to false, so `git push 2>&1 | Select-Object -Last 3` exits 1 even
	// though git exited 0 (Pi #8842). PowerShell 7 does not. Exit 0 when the last
	// failure is only such a record and the native command exited 0; otherwise
	// preserve the -Command rule: 0 when the last statement succeeded, 1 when it
	// failed. The lone `;` ensures an unfinished last statement stays a parse
	// error instead of continuing into the suffix and running.
	windowsPowerShellExitSuffix = "\n;\nif ($?) { exit 0 }\nif (($LASTEXITCODE -eq 0) -and (\"$($Error[0].FullyQualifiedErrorId)\" -like \"NativeCommandError*\")) { exit 0 }\nexit 1"
)

// PowerShellTool executes PowerShell commands on Windows with the same bounded
// memory capture, timeout, and cancellation behaviour as BashTool.
type PowerShellTool struct {
	Cwd string

	// resolveShell overrides PowerShell discovery in tests.
	resolveShell func() (shellutil.Config, error)
	// now overrides the idle-drain clock in tests.
	now func() time.Time
}

func (t *PowerShellTool) resolve() (shellutil.Config, error) {
	if t.resolveShell != nil {
		return t.resolveShell()
	}
	return shellutil.GetPowerShellConfig()
}

func wrapPowerShellCommand(cfg shellutil.Config, command string) string {
	// A trailing backtick would continue the user's final line into the suffix,
	// so leave such commands unchanged.
	suffix := ""
	if strings.EqualFold(shellutil.Win32Base(cfg.Shell), "powershell.exe") && !strings.HasSuffix(command, "`") {
		suffix = windowsPowerShellExitSuffix
	}
	return utf8OutputPrefix + command + suffix
}

func (t *PowerShellTool) ExecuteCtx(ctx context.Context, inv Invocation) Result {
	cmdStr, timeout, errRes := parseShellInvocation("powershell", inv)
	if errRes != nil {
		return *errRes
	}
	return executeShellCommand(ctx, t.Cwd, "PowerShell", "psi-powershell-*", t.resolve, wrapPowerShellCommand, cmdStr, timeout, t.now)
}
