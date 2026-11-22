package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
	_ "modernc.org/sqlite"
)

const (
	maxSecretStoreValueBytes = 65536
	secretStoreQueryTimeout  = 5 * time.Second
)

// storeToken reads one team-scoped secret value from an explicitly selected
// existing SQLite database. It never creates or modifies the file. The returned
// immutable string is required by discordgo; the owned database value is wiped.
func (s *fileSettings) storeToken(ref *secretRef, databasePath string) (string, error) {
	_, provider, err := s.secretProviderForRef("store", ref.provider)
	if err != nil {
		return "", err
	}
	if provider != nil && provider.Source != "store" {
		return "", fmt.Errorf("store secret provider is unavailable")
	}
	if !secretEnvName.MatchString(ref.store) || databasePath == "" {
		return "", fmt.Errorf("store secret is unavailable")
	}
	path, err := filepath.Abs(databasePath)
	if err != nil {
		return "", fmt.Errorf("store secret is unavailable")
	}
	before, err := os.Lstat(path)
	if err != nil || !secretStoreDatabasePrivate(path, before) {
		return "", fmt.Errorf("store secret is unavailable")
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	query := url.Values{"mode": []string{"ro"}}
	database, err := sql.Open("sqlite", uri+"?"+query.Encode())
	if err != nil {
		return "", fmt.Errorf("store secret is unavailable")
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), secretStoreQueryTimeout)
	defer cancel()
	connection, err := database.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("store secret is unavailable")
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return "", fmt.Errorf("store secret is unavailable")
	}
	var value []byte
	var kind string
	err = connection.QueryRowContext(ctx, `SELECT value, kind FROM secret_store_entries WHERE scope_kind = ? AND scope_id = ? AND name = ? AND deleted_at_ms IS NULL`, "team", "", ref.store).Scan(&value, &kind)
	defer secretmem.Wipe(value[:cap(value)])
	current, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(before, current) || !secretStoreDatabasePrivate(path, current) || len(value) > maxSecretStoreValueBytes {
		return "", fmt.Errorf("store secret is unavailable")
	}
	_ = kind // Both supported value kinds return their stored value.
	if len(bytes.TrimSpace(value)) == 0 {
		return "", fmt.Errorf("store secret is unavailable")
	}
	return string(value), nil
}
