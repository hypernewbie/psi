package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeSecretStoreDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE secret_store_entries (
		scope_kind TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		kind TEXT NOT NULL,
		deleted_at_ms INTEGER,
		PRIMARY KEY(scope_kind, scope_id, name)
	) STRICT`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO secret_store_entries(scope_kind, scope_id, name, value, kind, deleted_at_ms) VALUES
		('team', '', 'BOT_TOKEN', 'synthetic-token', 'secret', NULL),
		('identity', 'profile', 'BOT_TOKEN', 'wrong-scope', 'secret', NULL),
		('team', '', 'DELETED_TOKEN', 'deleted-token', 'secret', 123)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	applyPrivateStoreACL(t, path)
	return path
}

func TestStoreSecretReadsOnlyLiveTeamValue(t *testing.T) {
	path := makeSecretStoreDB(t)
	settings := &fileSettings{
		Channels: &channelsSettings{Discord: &discordSettings{Token: &secretRef{store: "BOT_TOKEN", provider: "local"}}},
		Secrets:  &secretsSettings{Providers: map[string]*secretProvider{"local": {Source: "store"}}},
	}
	got, err := settings.storeToken(settings.Channels.Discord.Token, path)
	if err != nil || got != "synthetic-token" {
		t.Fatalf("store lookup failed: %v", err)
	}
	settings.Channels.Discord.Token.store = "DELETED_TOKEN"
	if _, err := settings.storeToken(settings.Channels.Discord.Token, path); err == nil {
		t.Fatal("resolved a deleted secret")
	}
	settings.Channels.Discord.Token.store = "MISSING_TOKEN"
	if _, err := settings.storeToken(settings.Channels.Discord.Token, path); err == nil {
		t.Fatal("resolved a missing secret")
	}
}

func TestStoreSecretIsReadOnlyAndBounded(t *testing.T) {
	path := makeSecretStoreDB(t)
	settings := &fileSettings{
		Channels: &channelsSettings{Discord: &discordSettings{Token: &secretRef{store: "BOT_TOKEN", provider: "local"}}},
		Secrets:  &secretsSettings{Providers: map[string]*secretProvider{"local": {Source: "store"}}},
	}
	if _, err := settings.storeToken(settings.Channels.Discord.Token, path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM secret_store_entries WHERE name = 'BOT_TOKEN'`).Scan(&value); err != nil || value != "synthetic-token" {
		t.Fatalf("store value changed: %q, %v", value, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	settings.Channels.Discord.Token.store = "TOO_LARGE"
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO secret_store_entries(scope_kind, scope_id, name, value, kind) VALUES('team', '', 'TOO_LARGE', ?, 'secret')`, strings.Repeat("x", maxSecretStoreValueBytes+1)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if _, err := settings.storeToken(settings.Channels.Discord.Token, path); err == nil {
		t.Fatal("accepted a value above the provider limit")
	}
}

func TestStoreDatabaseDiscoveryUsesOnlySiblingStateDirectory(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json5")
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	database := makeSecretStoreDB(t)
	want := filepath.Join(stateDir, "secrets.sqlite")
	if err := os.Rename(database, want); err != nil {
		t.Fatal(err)
	}
	got, err := resolveSecretStoreDatabase("", settingsPath, func(string) (string, bool) { return "", false })
	// The discovered path is resolved, so a temporary directory is compared
	// in its real form.
	if err != nil || got != resolvePath(want) {
		t.Fatalf("database discovery = %q, %v", got, err)
	}
	outside := makeSecretStoreDB(t)
	got, err = resolveSecretStoreDatabase("", settingsPath, func(name string) (string, bool) {
		return outside, name == "PSIONIC_SECRET_STORE_DB"
	})
	// An explicit path is used as given, without resolving it.
	if err != nil || got != outside {
		t.Fatalf("explicit environment path was not honored: %q, %v", got, err)
	}
}

func TestStoreDatabaseDiscoveryRejectsAmbiguousMatches(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json5")
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.sqlite", "second.db"} {
		database := makeSecretStoreDB(t)
		if err := os.Rename(database, filepath.Join(stateDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := resolveSecretStoreDatabase("", settingsPath, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("guessed between multiple secret databases")
	}
}

func TestCheckResolvesStoreTokenWithoutExposingItOrWritingState(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	workspace := t.TempDir()
	storePath := makeSecretStoreDB(t)
	configPath := filepath.Join(t.TempDir(), "settings.json5")
	body := `{
		agents: { defaults: { workspace: ` + fmt.Sprintf("%q", workspace) + ` } },
		channels: { discord: { token: { source: "store", provider: "default", id: "BOT_TOKEN" } } }
	}`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--config", configPath, "--agent-dir", agentDir, "--state-dir", stateDir, "--secret-store-db", storePath, "--check"}, &stdout, &stderr); err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if strings.Contains(stdout.String(), "synthetic-token") || strings.Contains(stderr.String(), "synthetic-token") {
		t.Fatal("check exposed the token")
	}
	if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
		t.Fatalf("check changed state: %v", err)
	}
}

func TestStoreSecretRejectsMissingDatabaseWithoutCreatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	settings := &fileSettings{
		Channels: &channelsSettings{Discord: &discordSettings{Token: &secretRef{store: "BOT_TOKEN", provider: "default"}}},
	}
	if _, err := settings.storeToken(settings.Channels.Discord.Token, path); err == nil {
		t.Fatal("accepted a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created database unexpectedly: %v", err)
	}
}

// TestStoreDatabaseURIPutsTheDriveBehindTheRoot proves the SQLite URI names
// the file on every platform. A Windows drive letter behind "file://" would
// be read as a host, so the driver would open the wrong file.
func TestStoreDatabaseURIPutsTheDriveBehindTheRoot(t *testing.T) {
	cases := []struct {
		path string
		goos string
		want string
	}{
		{`C:\Users\test\AppData\Local\Temp\store.sqlite`, "windows", "file:///C:/Users/test/AppData/Local/Temp/store.sqlite"},
		{"/tmp/store.sqlite", "linux", "file:///tmp/store.sqlite"},
	}
	for _, tc := range cases {
		got := storeDatabaseURIFor(tc.path, tc.goos)
		if got != tc.want {
			t.Fatalf("uri for %q = %q, want %q", tc.path, got, tc.want)
		}
	}
}
