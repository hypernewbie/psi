package provider

import (
	"fmt"
	"os"
	"path/filepath"
)

// authFileHooks carries the filesystem seams for deterministic fault tests.
// A nil function selects the os default.
type authFileHooks struct {
	Chmod  func(string, os.FileMode) error
	Rename func(string, string) error
}

// persistAuthDoc atomically replaces authPath with encoded JSON. The caller
// supplies the already marshaled document without its trailing newline; the
// helper appends one newline, preserves the existing permission bits, and
// writes through a same-directory temporary file with sync and rename.
//
// validate runs after the temporary file is written, synced, and closed,
// immediately before rename. It must recheck lease ownership and any
// compare-before-write baseline. A validation failure refuses the overwrite
// and leaves the original document and no temporary file behind. The helper
// takes no caller context or OAuth fields; cancellation never skips a commit
// that validation accepts.
func persistAuthDoc(encoded []byte, authPath string, validate func() error, hooks authFileHooks) error {
	var keepMode os.FileMode
	if st, err := os.Stat(authPath); err == nil && !st.Mode().IsDir() {
		keepMode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(authPath), ".psi-auth-*")
	if err != nil {
		return fmt.Errorf("cannot persist refreshed credentials")
	}
	defer os.Remove(f.Name())
	if keepMode != 0 {
		chmod := hooks.Chmod
		if chmod == nil {
			chmod = os.Chmod
		}
		if err := chmod(f.Name(), keepMode); err != nil {
			f.Close()
			return fmt.Errorf("cannot preserve auth.json permissions")
		}
	}
	if _, err = f.Write(append(encoded, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("cannot safely persist refreshed credentials")
	}
	if validate != nil {
		if err := validate(); err != nil {
			return err
		}
	}
	rename := hooks.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(f.Name(), authPath); err != nil {
		return fmt.Errorf("cannot replace refreshed auth.json")
	}
	return nil
}
