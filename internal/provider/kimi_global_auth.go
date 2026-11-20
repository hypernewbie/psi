package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/providerregistry"
)

// Static Global Kimi credential failures. The message always names the one
// explicit recovery command; there is deliberately no API-key fallback.
var (
	errKimiGlobalNoCredential      = errors.New("no Kimi Code (Global) credential: run /login kimi-coding-global")
	errKimiGlobalBadCredential     = errors.New("invalid Kimi Code (Global) credential: run /login kimi-coding-global")
	errKimiGlobalExpiredCredential = errors.New("Kimi Code (Global) login expired: run /login kimi-coding-global again")
)

// KimiGlobalAuth consumes the Pi-shaped OAuth entry for the Global trial. A
// stored entry owns the provider: a missing, invalid, or expired entry never
// falls back to an API key or models.json key. This trial does not refresh;
// an expired access token requires another explicit login.
type KimiGlobalAuth struct {
	Path string
	// now selects the clock for expiry boundaries. Nil means time.Now.
	now func() time.Time
}

func (a *KimiGlobalAuth) nowOrNow() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// Check is local-only. A fresh OAuth entry is usable; the bearer is resolved
// again for each HTTP attempt. Checking or listing models makes no request.
func (a *KimiGlobalAuth) Check() error {
	entries, err := AuthEntries(a.Path)
	if err != nil {
		return err
	}
	defer ReleaseAuthEntries(entries)
	state := kimiGlobalCredentialState(entries, a.nowOrNow())
	if !state.Usable {
		return state.Err
	}
	return nil
}

// Get resolves the stored bearer for one Completions request through the
// bounded shared auth read. It never refreshes, never uses an API key, and
// never retrieves a credential after the entry is removed or expired.
func (a *KimiGlobalAuth) Get(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	entries, err := AuthEntries(a.Path)
	if err != nil {
		return "", err
	}
	defer ReleaseAuthEntries(entries)
	raw, present := entries[providerregistry.KimiGlobalProviderID]
	if !present {
		return "", errKimiGlobalNoCredential
	}
	entry, err := parseKimiGlobalOAuthEntry(raw)
	if err != nil {
		return "", err
	}
	if entry.expires <= a.nowOrNow().UnixMilli() {
		return "", errKimiGlobalExpiredCredential
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return entry.access, nil
}

// kimiGlobalCredentialState resolves the Global credential from one shared
// auth snapshot without a network request or an API-key fallback.
func kimiGlobalCredentialState(entries map[string]json.RawMessage, now time.Time) CredentialState {
	raw, present := entries[providerregistry.KimiGlobalProviderID]
	if !present {
		return CredentialState{Err: errKimiGlobalNoCredential}
	}
	entry, err := parseKimiGlobalOAuthEntry(raw)
	if err != nil {
		return CredentialState{Err: err}
	}
	if entry.expires <= now.UnixMilli() {
		return CredentialState{Err: errKimiGlobalExpiredCredential}
	}
	return CredentialState{Usable: true, Source: "auth.json:" + providerregistry.KimiGlobalProviderID, OAuth: true}
}

type kimiGlobalOAuthEntry struct {
	access  string
	refresh string
	expires int64
}

// parseKimiGlobalOAuthEntry validates the stored Pi OAuth shape. The refresh
// token is kept only for storage compatibility; this trial never uses it.
func parseKimiGlobalOAuthEntry(raw json.RawMessage) (kimiGlobalOAuthEntry, error) {
	var fields struct {
		Type    string      `json:"type"`
		Access  string      `json:"access"`
		Refresh string      `json:"refresh"`
		Expires json.Number `json:"expires"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&fields) != nil || fields.Type != "oauth" {
		return kimiGlobalOAuthEntry{}, errKimiGlobalBadCredential
	}
	if !kimiGlobalPrintable(fields.Access, 0) || strings.TrimSpace(fields.Refresh) == "" {
		return kimiGlobalOAuthEntry{}, errKimiGlobalBadCredential
	}
	expires, err := strconv.ParseInt(fields.Expires.String(), 10, 64)
	if err != nil || expires <= 0 {
		return kimiGlobalOAuthEntry{}, errKimiGlobalBadCredential
	}
	return kimiGlobalOAuthEntry{access: fields.Access, refresh: fields.Refresh, expires: expires}, nil
}
