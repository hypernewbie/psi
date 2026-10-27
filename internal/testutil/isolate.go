package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// IsolatedEnv sets a synthetic HOME and returns isolated dirs.
// It allowlists a minimal env for regression subprocesses: no parent credential
// inheritance, no proxy, no live-test flags, no parent Pi paths.
func IsolatedEnv(t *testing.T) (home, work, agent, sessions string) {
	t.Helper()
	home = t.TempDir()
	work = t.TempDir()
	agent = filepath.Join(home, ".pi", "agent")
	sessions = filepath.Join(agent, "sessions")
	for _, d := range []string{agent, sessions, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PSI_CODING_AGENT_DIR", agent)
	// Strip parent secrets/proxy/live flags from child env allowlist.
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "PSI_LIVE_PROVIDER",
		"PSI_PI_SESSION_MODULE", "PI_CODING_AGENT_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
		"ALL_PROXY", "all_proxy",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	// Invented canaries: prove children do not inherit them.
	t.Setenv("PSI_TEST_CANARY_PARENT_SECRET", "canary-parent-secret-0000")
	t.Setenv("PSI_TEST_CANARY_PI_PATH", filepath.Join(home, "CANARY-PI-PATH"))
	return home, work, agent, sessions
}

// ChildEnv returns the allowlisted env for a regression subprocess.
// Only PATH, HOME, and explicit synthetic vars pass through.
func ChildEnv(home string, extra map[string]string) []string {
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// AssertNoArtifactLeak fails if tracked artifacts contain private data markers.
// It never prints matched secret values.
func AssertNoArtifactLeak(t *testing.T, artifacts []string) {
	t.Helper()
	for _, p := range artifacts {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(b)
		for _, marker := range []string{
			"sk-ant-", "sk-proj-", "github_pat_", "xoxb-",
			"/home/", "/Users/",
		} {
			if strings.Contains(s, marker) {
				t.Errorf("artifact %s contains forbidden marker class", p)
			}
		}
		// Canary parent values must never appear in artifacts.
		if strings.Contains(s, "canary-parent-secret-0000") || strings.Contains(s, "CANARY-PI-PATH") {
			t.Errorf("artifact %s leaks parent canary", p)
		}
	}
}
