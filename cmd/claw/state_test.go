package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateAdmitsEachEventOnce(t *testing.T) {
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	now := time.Now()
	if ok, err := state.admitEvent("bot\x00message", now); err != nil || !ok {
		t.Fatalf("first = %v, %v", ok, err)
	}
	if ok, err := state.admitEvent("bot\x00message", now); err != nil || ok {
		t.Fatalf("second = %v, %v", ok, err)
	}
}

func TestStatePrunesOldEvents(t *testing.T) {
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	old := time.Now().Add(-48 * time.Hour)
	if ok, _ := state.admitEvent("old", old); !ok {
		t.Fatal("seed")
	}
	if err := state.pruneEvents(time.Now().Add(-24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := state.admitEvent("old", time.Now()); !ok {
		t.Fatal("old event was not pruned")
	}
}

func TestStateRouteRoundTripAndPathChecks(t *testing.T) {
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	want := routeState{SessionID: "abc123", StartedAt: 10, LastInteraction: 20}
	if err := state.saveRoute("dm:1", want); err != nil {
		t.Fatal(err)
	}
	got, found, err := state.route("dm:1")
	if err != nil || !found || got != want {
		t.Fatalf("route = %+v %v %v", got, found, err)
	}
	if err := state.deleteRoute("dm:1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := state.route("dm:1"); found {
		t.Fatal("route was not deleted")
	}
	dir := state.routeDir("dm:1")
	if err := ensureRouteDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "abc123.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.checkSessionPath(dir, "abc123"); err != nil {
		t.Fatalf("valid path: %v", err)
	}
	if _, err := state.checkSessionPath(dir, "../abc123"); err == nil {
		t.Fatal("escaping ID was accepted")
	}
}

func TestStateDirectoryRequiresMarkerOrEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	store, err := openState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, stateMarker)); err != nil || string(data) != stateMarkerContents {
		t.Fatalf("marker = %q, %v", data, err)
	}
	store.close()
	reopened, err := openState(dir)
	if err != nil {
		t.Fatalf("reopen marked state: %v", err)
	}
	reopened.close()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "foreign"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openState(other); err == nil {
		t.Fatal("unmarked non-empty state directory accepted")
	}
}

func TestStateRejectsLinkedTranscript(t *testing.T) {
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	dir := state.routeDir("dm:1")
	if err := ensureRouteDir(dir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked.jsonl")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := state.checkSessionPath(dir, "linked"); err == nil {
		t.Fatal("linked transcript accepted")
	}
}
