package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverHeadersDoesNotValidateTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy-name.jsonl")
	header := `{"type":"session","version":3,"id":"head-id","cwd":"/ws"}`
	body := `{"type":"message","id":"entry","message":{"role":false}}`
	if err := os.WriteFile(path, []byte(header+"\n"+body+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	infos, err := DiscoverHeaders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "head-id" || infos[0].Cwd != "/ws" || infos[0].Unavailable != "" {
		t.Fatalf("header listing validated a damaged body: %+v", infos)
	}
}
