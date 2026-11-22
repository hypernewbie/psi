package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxStoreDatabaseCandidates = 64

// resolveSecretStoreDatabase uses an explicit path first. Otherwise it looks
// only in the state directory beside the supplied settings file and accepts a
// unique private SQLite database with the store table. It does not search the
// home directory or inspect unrelated application state.
func resolveSecretStoreDatabase(explicit, settingsPath string, lookupEnv func(string) (string, bool)) (string, error) {
	if explicit == "" {
		if value, ok := lookupEnv("PSIONIC_SECRET_STORE_DB"); ok && strings.TrimSpace(value) != "" {
			explicit = strings.TrimSpace(value)
		}
	}
	if explicit != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("store secret database is unavailable")
		}
		return path, nil
	}
	configPath, err := filepath.Abs(settingsPath)
	if err != nil {
		return "", fmt.Errorf("store secret database is unavailable")
	}
	if resolved, err := filepath.EvalSymlinks(configPath); err == nil {
		configPath = resolved
	}
	stateDir := filepath.Join(filepath.Dir(configPath), "state")
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("store secret database is unavailable")
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) > maxStoreDatabaseCandidates {
		return "", fmt.Errorf("store secret database is unavailable")
	}
	matches := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir() || !isSQLiteDatabaseName(entry.Name()) {
			continue
		}
		path := filepath.Join(stateDir, entry.Name())
		fileInfo, err := os.Lstat(path)
		if err != nil || !secretStoreDatabasePrivate(path, fileInfo) {
			continue
		}
		if hasSecretStoreTable(path) {
			matches = append(matches, path)
			if len(matches) > 1 {
				return "", fmt.Errorf("store secret database is ambiguous")
			}
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("store secret database is unavailable")
	}
	return matches[0], nil
}

func isSQLiteDatabaseName(name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	return extension == ".sqlite" || extension == ".sqlite3" || extension == ".db"
}

func hasSecretStoreTable(path string) bool {
	uri := storeDatabaseURI(path)
	database, err := sql.Open("sqlite", uri+"?mode=ro")
	if err != nil {
		return false
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, err := database.Conn(ctx)
	if err != nil {
		return false
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return false
	}
	var exists int
	err = connection.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = ? AND name = ? LIMIT 1`, "table", "secret_store_entries").Scan(&exists)
	return err == nil && exists == 1
}
