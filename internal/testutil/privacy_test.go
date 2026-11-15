package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P2-096: privacy audit — no real config/credentials/paths/transcripts in tracked artifacts.
// No excluded runtime service enters product.
func TestPrivacyAudit(t *testing.T) {
	// Tracked docs + code must not contain private markers.
	roots := []string{"docs", "internal", "cmd", "scripts"}
	for _, root := range roots {
		filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return nil
			}
			if strings.Contains(p, "testdata") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			s := string(b)
			for _, marker := range []string{"sk-ant-", "sk-proj-", "github_pat_", "/home/hypernewbie", "/Users/"} {
				if strings.Contains(s, marker) {
					t.Errorf("%s contains private marker class", p)
				}
			}
			return nil
		})
	}
	// No excluded runtime imports (extensions, MCP, telemetry, etc.).
	banned := []string{"telemetry", "subagent", "rpc-server", "print-mode", "json-event"}
	for _, root := range []string{"internal", "cmd"} {
		filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			b, _ := os.ReadFile(p)
			s := strings.ToLower(string(b))
			for _, ban := range banned {
				// Allow mentions in comments about exclusions, but not imports.
				if strings.Contains(s, "\"") && strings.Contains(s, ban) {
					// crude: check import lines
					for _, line := range strings.Split(s, "\n") {
						if strings.Contains(line, "import") && strings.Contains(line, ban) {
							t.Errorf("%s imports excluded %s", p, ban)
						}
					}
				}
			}
			return nil
		})
	}
	// The MCP SDK stays behind internal/mcp in production code. Test fixtures
	// may use the SDK server directly.
	for _, root := range []string{"internal", "cmd"} {
		filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "github.com/modelcontextprotocol/go-sdk") && !strings.HasPrefix(filepath.ToSlash(p), "internal/mcp/") {
				t.Errorf("%s imports the MCP SDK outside internal/mcp", p)
			}
			return nil
		})
	}
}
