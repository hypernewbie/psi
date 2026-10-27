package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectToolProcessCannotInheritParentCredentialsOrStartupScript(t *testing.T) {
	script := filepath.Join(t.TempDir(), "bash-env")
	if err := os.WriteFile(script, []byte("printf SYNTHETIC_STARTUP_SCRIPT_RAN"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", script)
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-parent-key")
	t.Setenv("OPENAI_API_KEY", "synthetic-parent-key")
	t.Setenv("PI_CODING_AGENT_DIR", "/synthetic-parent-agent")
	var childHome string
	code := RunIsolated(func() int {
		childHome = os.Getenv("HOME")
		cmd := exec.Command("bash", "-c", `printf '%s|%s|%s' "$ANTHROPIC_API_KEY" "$OPENAI_API_KEY" "$BASH_ENV"`)
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "||" {
			return 1
		}
		if strings.Contains(os.Getenv("PI_CODING_AGENT_DIR"), "synthetic-parent") {
			return 1
		}
		return 0
	})
	if code != 0 {
		t.Fatal("tool process inherited forbidden parent state")
	}
	if _, err := os.Stat(childHome); !os.IsNotExist(err) {
		t.Fatal("isolated process directory leaked")
	}
	if os.Getenv("BASH_ENV") != script {
		t.Fatal("parent environment not restored")
	}
}
