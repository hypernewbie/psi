package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"psi/internal/terminal"
)

// Project trust follows pi's project-trust contract:
//
//   - Trust-requiring project resources are detected exactly like pi does.
//   - The decision for a folder is stored in <agentDir>/trust.json and the
//     nearest ancestor entry decides, so a folder is asked about once.
//   - The startup prompt offers pi's option set, and only saved options
//     write to the trust store.
//
// A folder without trust-requiring resources never asks. The user skills
// directory in the home directory is a user resource and never needs trust.

// trustRequiringProjectResources lists the project entries that trust gates.
// It mirrors pi's TRUST_REQUIRING_PROJECT_CONFIG_RESOURCES.
var trustRequiringProjectResources = []string{
	"settings.json",
	"mcp.json",
	"extensions",
	"skills",
	"prompts",
	"themes",
	"SYSTEM.md",
	"APPEND_SYSTEM.md",
}

// hasTrustRequiringProjectResources reports whether cwd holds project-local
// resources that trust gates: trust-requiring entries under cwd/.pi, or
// .agents/skills in cwd or one of its ancestors. The user/global
// ~/.agents/skills directory is always a trusted user resource and is
// ignored here, even when cwd is $HOME.
func hasTrustRequiringProjectResources(cwd string) bool {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	userAgentsSkills := filepath.Join(canonicalTrustPath(home), ".agents", "skills")
	current := canonicalTrustPath(cwd)
	for _, name := range trustRequiringProjectResources {
		if _, err := os.Stat(filepath.Join(current, ".pi", name)); err == nil {
			return true
		}
	}
	for {
		agentsSkills := filepath.Join(current, ".agents", "skills")
		if !trustPathsEqual(agentsSkills, userAgentsSkills) {
			if _, err := os.Stat(agentsSkills); err == nil {
				return true
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

// canonicalTrustPath resolves a path to its canonical form, following
// symlinks. It falls back to the absolute path when resolution fails, so
// callers never fail on a missing entry.
func canonicalTrustPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// trustPathsEqual reports whether both paths name the same location. Windows
// paths compare case-insensitively.
func trustPathsEqual(left, right string) bool {
	l, r := filepath.Clean(left), filepath.Clean(right)
	if goruntime.GOOS == "windows" {
		return strings.EqualFold(l, r)
	}
	return l == r
}

// projectTrustUpdate is one write to the trust store. A nil decision removes
// the entry for the path.
type projectTrustUpdate struct {
	path     string
	decision *bool
}

// projectTrustStore is pi's ProjectTrustStore: one JSON object in
// <agentDir>/trust.json that maps folder paths to a trust decision.
type projectTrustStore struct {
	path string
}

func newProjectTrustStore(agentDir string) *projectTrustStore {
	return &projectTrustStore{path: filepath.Join(agentDir, "trust.json")}
}

// get returns the stored decision for cwd or its nearest ancestor.
func (s *projectTrustStore) get(cwd string) (bool, bool) {
	data, err := s.read()
	if err != nil {
		return false, false
	}
	decision, ok := findNearestTrustEntry(data, cwd)
	if !ok {
		return false, false
	}
	return decision, true
}

// setMany applies the updates and rewrites the trust store.
func (s *projectTrustStore) setMany(updates []projectTrustUpdate) error {
	return s.withLock(func() error {
		data, err := s.read()
		if err != nil {
			return err
		}
		for _, update := range updates {
			key := canonicalTrustPath(update.path)
			for existing := range data {
				if trustPathsEqual(existing, key) {
					delete(data, existing)
				}
			}
			if update.decision != nil {
				decision := *update.decision
				data[key] = &decision
			}
		}
		return s.write(data)
	})
}

// read loads the trust store. A missing file is an empty store.
func (s *projectTrustStore) read() (map[string]*bool, error) {
	data := map[string]*bool{}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	decoder := json.NewDecoder(bytes.NewReader(stripTrustBOM(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to read trust store %s: %w", s.path, err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("invalid trust store %s: expected an object", s.path)
	}
	for key, value := range parsed {
		switch typed := value.(type) {
		case bool:
			decision := typed
			data[key] = &decision
		case nil:
			data[key] = nil
		default:
			return nil, fmt.Errorf("invalid trust store %s: value for %q must be true, false, or null", s.path, key)
		}
	}
	return data, nil
}

// write stores the decisions with sorted keys, matching pi's file format.
func (s *projectTrustStore) write(data map[string]*bool) error {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sorted := make(map[string]*bool, len(data))
	for _, key := range keys {
		sorted[key] = data[key]
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(sorted); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, s.path)
}

// withLock guards the read-modify-write cycle with pi's lock file so
// concurrent psi runs cannot lose a decision.
func (s *projectTrustStore) withLock(fn func() error) error {
	lockPath := s.path + ".lock"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	release, err := acquireTrustLock(lockPath)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// acquireTrustLock creates the lock file exclusively. A lock left behind by a
// crashed run is stale after ten seconds and gets removed.
func acquireTrustLock(lockPath string) (func(), error) {
	const maxAttempts = 10
	const delay = 20 * time.Millisecond
	const staleAfter = 10 * time.Second
	for attempt := 0; attempt < maxAttempts; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file.Close()
			return func() { os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleAfter {
			os.Remove(lockPath)
			continue
		}
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("failed to acquire trust store lock")
}

// findNearestTrustEntry returns the decision of the nearest ancestor of cwd
// that has one. Entries without a decision are skipped.
func findNearestTrustEntry(data map[string]*bool, cwd string) (bool, bool) {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	current := canonicalTrustPath(cwd)
	for {
		for _, key := range keys {
			if !trustPathsEqual(key, current) {
				continue
			}
			if decision := data[key]; decision != nil {
				return *decision, true
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, false
		}
		current = parent
	}
}

// projectTrustOption is one choice of the trust prompt.
type projectTrustOption struct {
	label   string
	trusted bool
	updates []projectTrustUpdate
}

// projectTrustOptions returns pi's trust options for cwd: trust, trust the
// parent folder, and the session-only variants.
func projectTrustOptions(cwd string) []projectTrustOption {
	trustPath := canonicalTrustPath(cwd)
	trusted := true
	untrusted := false
	options := []projectTrustOption{
		{label: "Trust", trusted: true, updates: []projectTrustUpdate{{path: trustPath, decision: &trusted}}},
	}
	if parent := filepath.Dir(trustPath); parent != trustPath {
		options = append(options, projectTrustOption{
			label:   "Trust parent folder (" + parent + ")",
			trusted: true,
			updates: []projectTrustUpdate{{path: parent, decision: &trusted}, {path: trustPath, decision: nil}},
		})
	}
	options = append(options,
		projectTrustOption{label: "Trust (this session only)", trusted: true},
		projectTrustOption{label: "Do not trust", trusted: false, updates: []projectTrustUpdate{{path: trustPath, decision: &untrusted}}},
		projectTrustOption{label: "Do not trust (this session only)", trusted: false},
	)
	return options
}

// resolveProjectTrust decides whether project resources may be loaded. It
// mirrors pi's resolveProjectTrusted: an explicit override wins, then the
// stored decision, then the defaultProjectTrust setting, then the prompt.
func resolveProjectTrust(o Options, cwd, agentDir, defaultTrust string, console *terminal.Console, lifecycle *terminalLifecycle) (bool, error) {
	if o.DenyProj {
		return false, nil
	}
	if o.AllowProj {
		return true, nil
	}
	store := newProjectTrustStore(agentDir)
	if decision, ok := store.get(cwd); ok {
		return decision, nil
	}
	if !hasTrustRequiringProjectResources(cwd) {
		return true, nil
	}
	switch defaultTrust {
	case "always":
		return true, nil
	case "never":
		return false, nil
	}
	// Pi has no UI outside the interactive mode, so the resources stay
	// untrusted instead of asking.
	if console == nil || lifecycle == nil {
		return false, nil
	}
	options := projectTrustOptions(cwd)
	choices := make([]terminal.ChoiceOption, 0, len(options))
	for _, option := range options {
		choices = append(choices, terminal.ChoiceOption{Label: option.label})
	}
	picker := &terminal.ChoicePicker{
		Title: "Trust project folder?",
		Context: cwd + "\n\nThis allows psi to load .pi settings and resources, install missing project packages, " +
			"and execute project extensions.",
		Options:  choices,
		Selected: 0,
	}
	selected, cancelled, err := runChoicePicker(console, lifecycle, picker)
	if err != nil {
		return false, err
	}
	if cancelled || selected < 0 || selected >= len(options) {
		return false, nil
	}
	option := options[selected]
	if len(option.updates) > 0 {
		if err := store.setMany(option.updates); err != nil {
			return false, err
		}
	}
	return option.trusted, nil
}

// stripTrustBOM removes a leading byte order mark.
func stripTrustBOM(raw []byte) []byte {
	return bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
}
