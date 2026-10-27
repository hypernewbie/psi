package app

import (
	"os"
	"path/filepath"
	"testing"

	"psi/internal/config"
	"psi/internal/skills"
	"psi/internal/testutil"
)

// P2-051, P2-082: declined project resources cannot enter prompt via later activation.
// Project consent is per-run; activation must not reload project when declined.
func TestDeclinedProjectStaysOut(t *testing.T) {
	_, work, agent, _ := testutil.IsolatedEnv(t)
	// Project resources.
	os.MkdirAll(filepath.Join(work, ".pi", "skills", "proj"), 0o755)
	os.WriteFile(filepath.Join(work, ".pi", "settings.json"), []byte(`{"defaultTools":["read"]}`), 0o644)
	os.WriteFile(filepath.Join(work, ".pi", "skills", "proj", "SKILL.md"), []byte("---\nname: proj\ndescription: p\n---\nbody"), 0o644)
	os.WriteFile(filepath.Join(work, ".pi", "SYSTEM.md"), []byte("project instructions"), 0o644)
	// Declined consent.
	loaded, err := config.LoadSettings(filepath.Join(agent, "settings.json"), filepath.Join(work, ".pi", "settings.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Settings.DefaultTools) != 4 { // defaults, not project
		t.Fatalf("declined project leaked settings: %v", loaded.Settings.DefaultTools)
	}
	instr, err := config.DiscoverInstructions(work, agent, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range instr {
		if containsStrTest(in.Content, "project instructions") {
			t.Fatal("declined project instructions leaked")
		}
	}
	sk, _ := skills.Discover(work, agent, nil, false)
	for _, s := range sk {
		if s.Name == "proj" {
			t.Fatal("declined project skill leaked")
		}
	}
	// Allowed consent loads them.
	loaded2, _ := config.LoadSettings(filepath.Join(agent, "settings.json"), filepath.Join(work, ".pi", "settings.json"), true)
	if len(loaded2.Settings.DefaultTools) != 1 {
		t.Fatalf("allowed should load project tools: %v", loaded2.Settings.DefaultTools)
	}
	sk2, _ := skills.Discover(work, agent, nil, true)
	found := false
	for _, s := range sk2 {
		if s.Name == "proj" {
			found = true
		}
	}
	if !found {
		t.Fatal("allowed should load project skill")
	}
}

func containsStrTest(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// P2-052, P2-083: portable skill works through discovery, invoke, restart, resume.
// No package manager, extension runtime, or private fixtures.
func TestSkillPortableJourney(t *testing.T) {
	_, work, agent, _ := testutil.IsolatedEnv(t)
	os.MkdirAll(filepath.Join(work, ".pi", "skills", "doer"), 0o755)
	body := "SKILL_BODY fix it"
	os.WriteFile(filepath.Join(work, ".pi", "skills", "doer", "SKILL.md"), []byte("---\nname: doer\ndescription: d\n---\n"+body), 0o644)
	sk, diags := skills.Discover(work, agent, nil, true)
	if len(diags) != 0 {
		t.Fatalf("diags: %v", diags)
	}
	if len(sk) != 1 || sk[0].Name != "doer" {
		t.Fatalf("discover: %+v", sk)
	}
	expanded, err := skills.Invoke(sk, "doer", "run-it")
	if err != nil {
		t.Fatal(err)
	}
	if !containsStrTest(expanded, "SKILL_BODY") || !containsStrTest(expanded, "run-it") {
		t.Fatalf("invoke: %q", expanded)
	}
	// Restart + resume: rediscover, same result.
	sk2, _ := skills.Discover(work, agent, nil, true)
	expanded2, err := skills.Invoke(sk2, "doer", "run-it")
	if err != nil {
		t.Fatal(err)
	}
	if expanded != expanded2 {
		t.Fatal("skill not stable across restart")
	}
}
