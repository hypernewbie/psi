package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultSessionDirEncoding(t *testing.T) {
	// Unix fixtures use Unix paths; Windows fixtures use Windows paths.
	// A POSIX path on Windows is drive-relative, so it cannot encode
	// the same way.
	cases := []struct{ cwd, wantSuffix string }{
		{"/home/user/proj", "--home-user-proj--"},
		{"/", "----"},
		{"/tmp/a b", "--tmp-a b--"},
		{"/tmp/uni-caf\u00e9", "--tmp-uni-caf\u00e9--"},
	}
	if runtime.GOOS == "windows" {
		cases = []struct{ cwd, wantSuffix string }{
			{`C:\Users\u\proj`, "--C--Users-u-proj--"},
			{`C:\`, "--C---"},
			{`C:\tmp\a b`, "--C--tmp-a b--"},
		}
	}
	for _, tc := range cases {
		got := DefaultSessionDir(tc.cwd, "/tmp/agent")
		base := filepath.Base(got)
		if base != tc.wantSuffix {
			t.Errorf("cwd %q: got base %q want %q (full %q)", tc.cwd, base, tc.wantSuffix, got)
		}
		// Root must be under sessions.
		if !strings.Contains(got, "sessions") {
			t.Errorf("cwd %q: missing sessions in %q", tc.cwd, got)
		}
	}
	// Separators: backslash and colon become dashes.
	got := DefaultSessionDir(`/a\b:c`, "/tmp/agent")
	if runtime.GOOS != "windows" && (strings.Contains(filepath.Base(got), `\`) || strings.Contains(filepath.Base(got), ":")) {
		t.Errorf("separators not encoded: %q", got)
	}
}

func TestSessionDirOverridePrecedence(t *testing.T) {
	// Absolute fixtures come from temp dirs so the test runs on every
	// platform. Hardcoded POSIX paths are drive-relative on Windows.
	root := t.TempDir()
	cwd := filepath.Join(root, "w")
	agent := filepath.Join(root, ".pi", "agent")
	flagDir := filepath.Join(root, "flag", "dir")
	envDir := filepath.Join(root, "env", "dir")
	def := DefaultSessionDir(cwd, agent)
	if got := SessionDirOverride(flagDir, envDir, "set/dir", cwd, agent); got != flagDir {
		t.Errorf("flag precedence: %q", got)
	}
	if got := SessionDirOverride("", envDir, "set/dir", cwd, agent); got != envDir {
		t.Errorf("env precedence: %q", got)
	}
	// Relative settings dir resolves from agent dir.
	if got := SessionDirOverride("", "", "custom", cwd, agent); got != filepath.Join(agent, "custom") {
		t.Errorf("settings relative: %q", got)
	}
	if got := SessionDirOverride("", "", "", cwd, agent); got != def {
		t.Errorf("default fallback: %q vs %q", got, def)
	}
}

func TestAssertValidSessionID(t *testing.T) {
	valid := []string{"a", "ab1", "my.session-1_2", "A0-._9z"}
	for _, id := range valid {
		if err := AssertValidSessionID(id); err != nil {
			t.Errorf("valid %q rejected: %v", id, err)
		}
	}
	invalid := []string{"", "-abc", "abc-", ".abc", "/abc", "a b", "a/b"}
	for _, id := range invalid {
		if err := AssertValidSessionID(id); err == nil {
			t.Errorf("invalid %q accepted", id)
		}
	}
}

func TestAgentDirEnv(t *testing.T) {
	t.Setenv("PSI_CODING_AGENT_DIR", "/tmp/psi-agent")
	if got := AgentDir(); got != "/tmp/psi-agent" {
		t.Errorf("PSI env: %q", got)
	}
	os.Unsetenv("PSI_CODING_AGENT_DIR")
	t.Setenv("PI_CODING_AGENT_DIR", "/tmp/pi-agent")
	if got := AgentDir(); got != "/tmp/pi-agent" {
		t.Errorf("PI env: %q", got)
	}
}
