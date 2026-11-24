package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/mcp"
	"github.com/hypernewbie/psi/internal/session"
)

func TestMCPCommandStatusIsSnapshotOnly(t *testing.T) {
	var out bytes.Buffer
	r := &runtime{options: Options{NoMCP: true}}
	if _, err := r.handleMCPCommand("", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "disabled by --no-mcp") {
		t.Fatalf("disabled status = %q", out.String())
	}
	out.Reset()
	r = &runtime{}
	if _, err := r.handleMCPCommand("", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not started") {
		t.Fatalf("unstarted status = %q", out.String())
	}
	out.Reset()
	if _, err := r.handleMCPCommand("reconnect", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "usage: /mcp") {
		t.Fatalf("argument status = %q", out.String())
	}
}

func TestMCPSectionsSuppressedForCustomPrompt(t *testing.T) {
	toolsAtRequest := []mcp.Tool{{Name: "mcp__s__t"}}
	r := &runtime{sections: []config.PromptSection{{Name: "docs", Body: "docs"}}}
	sections := r.mcpSections(r.sections, toolsAtRequest)
	if len(sections) != 2 || sections[1].Name != "mcp" {
		t.Fatalf("automatic prompt sections = %+v", sections)
	}
	r = &runtime{sections: []config.PromptSection{{Name: "preamble", Body: "custom"}}}
	sections = r.mcpSections(r.sections, toolsAtRequest)
	if len(sections) != 1 {
		t.Fatalf("custom prompt gained MCP guidance: %+v", sections)
	}
	sections = r.mcpSections(r.sections, nil)
	if len(sections) != 1 {
		t.Fatalf("unavailable MCP gained guidance: %+v", sections)
	}
}

func TestSessionReplacementOwnsFreshMCPManager(t *testing.T) {
	r := testRuntime(t)
	r.mcpAccount = &mcpAccount{}
	r.startMCP()
	old := r.mcpManager()
	if old == nil {
		t.Fatal("startup manager missing")
	}
	next, err := session.New(session.OpenOptions{Cwd: r.store.Cwd(), SessionDir: t.TempDir(), AgentDir: r.agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := next.TryLock(); err != nil {
		t.Fatal(err)
	}
	if err := r.replace(next); err == nil {
		t.Fatal("replace accepted a store that was already locked")
	}
	if r.mcpManager() != old {
		t.Fatal("failed replacement changed the active manager")
	}
	next.Unlock()
	if err := r.replace(next); err != nil {
		t.Fatal(err)
	}
	fresh := r.mcpManager()
	if fresh == nil || fresh == old {
		t.Fatal("committed replacement did not install a fresh manager")
	}
	candidate := *r
	r.adoptConfiguration(&candidate)
	if r.mcpManager() != fresh {
		t.Fatal("model/configuration install replaced the MCP manager")
	}
	r.stopMCP()
	waitManager(t, fresh, 5*time.Second)
	waitManager(t, old, 5*time.Second)
}

func TestMCPReloadOfFirstProjectConfigRequiresTrust(t *testing.T) {
	for _, tc := range []struct {
		name         string
		defaultTrust string
		wantProject  bool
	}{
		{name: "undecided", defaultTrust: "ask"},
		{name: "never trust", defaultTrust: "never"},
		{name: "always trust", defaultTrust: "always", wantProject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			options := Options{NoSkills: true, NoContextFiles: true}
			agentDir := t.TempDir()
			trust, err := resolveProjectTrust(options, work, agentDir, tc.defaultTrust, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			store, err := session.New(session.OpenOptions{Cwd: work, AgentDir: agentDir})
			if err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				r := &runtime{store: store, agentDir: agentDir, options: options, projectTrust: trust}
				r.startMCP()
				manager := r.mcpManager()
				defer func() { r.stopMCP(); manager.Wait() }()
				synctest.Wait()
				before := manager.Snapshot().Revision
				// Disabled entries show which configs were accepted without starting a launcher.
				writeAppFile(t, filepath.Join(work, ".pi", "mcp.json"),
					`{"mcpServers":{"project":{"command":"fixture","enabled":false}}}`)
				writeAppFile(t, filepath.Join(agentDir, "mcp.json"),
					`{"mcpServers":{"user":{"command":"fixture","enabled":false}}}`)
				if err := r.reloadResources(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				after := manager.Snapshot()
				var user, project bool
				for _, server := range after.Servers {
					user = user || server.Name == "user"
					project = project || server.Name == "project"
				}
				if after.Revision <= before || !user || project != tc.wantProject {
					t.Fatalf("reloaded MCP servers = %+v, want user=true, project=%t", after.Servers, tc.wantProject)
				}
			})
		})
	}
}

func waitManager(t *testing.T, manager *mcp.Manager, budget time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		manager.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatal("manager cleanup did not finish")
	}
}
