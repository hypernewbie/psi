package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	routesBucket = []byte("routes")
	eventsBucket = []byte("events")
)

type routeState struct {
	SessionID       string `json:"sessionId"`
	StartedAt       int64  `json:"startedAt"`
	LastInteraction int64  `json:"lastInteraction"`
	Private         bool   `json:"private,omitempty"`
	Workspace       string `json:"workspace,omitempty"`
	Scope           string `json:"scope,omitempty"`
}

// stateStore keeps only small routing and deduplication records. Conversation
// content remains in the JSONL transcripts.
type stateStore struct {
	db        *bolt.DB
	session   string
	pruneMu   sync.Mutex
	lastPrune time.Time
}

const (
	stateMarker         = ".claw-state"
	stateMarkerContents = "psi-claw state\n"
	maxStateMarkerBytes = 64
)

func markStateDir(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("state directory is not a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	marker := filepath.Join(dir, stateMarker)
	if len(entries) > 0 {
		info, err := os.Lstat(marker)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("state directory is not a Claw state directory")
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxStateMarkerBytes {
			return fmt.Errorf("state directory marker is invalid")
		}
		existing, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if string(existing) != stateMarkerContents {
			return fmt.Errorf("state directory marker is invalid")
		}
		return nil
	}
	return os.WriteFile(marker, []byte(stateMarkerContents), 0o600)
}

func openState(dir string) (*stateStore, error) {
	if err := markStateDir(dir); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(dir, "state.db"), 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &stateStore{db: db, session: filepath.Join(dir, "sessions")}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(routesBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(eventsBucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureRouteDir(s.session); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *stateStore) close() error {
	return s.db.Close()
}

func ensureRouteDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("session directory is not a directory")
	}
	return nil
}

// routeDir is a stable, non-reversible directory for one conversation. The
// route key never becomes a path component.
func (s *stateStore) routeDir(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.session, hex.EncodeToString(sum[:8]))
}

func (s *stateStore) route(key string) (routeState, bool, error) {
	var state routeState
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(routesBucket).Get([]byte(key))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &state)
	})
	return state, found, err
}

func (s *stateStore) deleteRoute(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(routesBucket).Delete([]byte(key))
	})
}

func (s *stateStore) saveRoute(key string, state routeState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(routesBucket).Put([]byte(key), raw)
	})
}

// admitEvent reserves one source event before any model or tool work. It
// returns false when the event was already seen, including an interrupted
// earlier attempt that deliberately is not replayed.
func (s *stateStore) admitEvent(eventKey string, now time.Time) (bool, error) {
	s.pruneMu.Lock()
	prune := s.lastPrune.IsZero() || now.Sub(s.lastPrune) >= time.Hour
	if prune {
		s.lastPrune = now
	}
	s.pruneMu.Unlock()
	if prune {
		if err := s.pruneEvents(now.Add(-eventRetention)); err != nil {
			return false, err
		}
	}
	admitted := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(eventsBucket)
		if bucket.Get([]byte(eventKey)) != nil {
			return nil
		}
		admitted = true
		return bucket.Put([]byte(eventKey), []byte(fmt.Sprintf("%020d", now.UnixNano())))
	})
	return admitted, err
}

// pruneEvents bounds the deduplication window.
func (s *stateStore) pruneEvents(before time.Time) error {
	cutoff := fmt.Sprintf("%020d", before.UnixNano())
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(eventsBucket)
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if string(value) < cutoff {
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// checkSessionPath refuses a pointer that escapes this state directory or is
// not a regular file.
func (s *stateStore) checkSessionPath(dir, id string) (string, error) {
	if id == "" || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid stored session ID")
	}
	path := filepath.Join(dir, id+".jsonl")
	if !inside(s.session, path) {
		return "", fmt.Errorf("stored session path escapes the state directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("stored session is not a regular file")
	}
	return path, nil
}
