//go:build unix

package tools

// Phase D regression for PLAN3 F13: ordinary new tool files and directories
// follow the conventional creation defaults (0666 files, 0777 directories)
// with the process umask applied. Existing targets keep their mode.
//
// The umask is process-wide, so each case runs in a child process under
// `sh -c 'umask ...'`. The parent process umask is never changed.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUmaskChild(t *testing.T) {
	ws := os.Getenv("PSI_UMASK_PROBE")
	if ws == "" {
		t.Skip("umask probe child only")
	}
	reg := NewRegistry(ws)
	shapes := []struct{ path, content string }{
		{"new.txt", "new"},
		{"nested/deep/file.txt", "deep"},
	}
	for _, s := range shapes {
		if res := reg.Dispatch(Invocation{ID: "w", Name: "write",
			Arguments: map[string]any{"path": s.path, "content": s.content}}); res.IsError {
			fmt.Printf("FAIL write %s: %s\n", s.path, res.Content[0].Text)
			os.Exit(1)
		}
	}
	// Existing targets keep their mode.
	keep0600 := filepath.Join(ws, "keep0600.txt")
	keep0755 := filepath.Join(ws, "keep0755.txt")
	if res := reg.Dispatch(Invocation{ID: "w", Name: "write",
		Arguments: map[string]any{"path": "keep0600.txt", "content": "k"}}); res.IsError {
		fmt.Printf("FAIL keep0600: %s\n", res.Content[0].Text)
		os.Exit(1)
	}
	if res := reg.Dispatch(Invocation{ID: "w", Name: "write",
		Arguments: map[string]any{"path": "keep0755.txt", "content": "k"}}); res.IsError {
		fmt.Printf("FAIL keep0755: %s\n", res.Content[0].Text)
		os.Exit(1)
	}
	for _, p := range []string{filepath.Join(ws, "new.txt"), filepath.Join(ws, "nested", "deep", "file.txt"),
		filepath.Join(ws, "nested"), keep0600, keep0755} {
		fi, err := os.Lstat(p)
		if err != nil {
			fmt.Printf("FAIL stat %s: %v\n", p, err)
			os.Exit(1)
		}
		rel, _ := filepath.Rel(ws, p)
		fmt.Printf("%s %04o\n", rel, fi.Mode().Perm())
	}
	// Symlink writes follow the link: report the target mode.
	if fi, err := os.Stat(filepath.Join(ws, "link.txt")); err != nil {
		fmt.Printf("FAIL stat link.txt: %v\n", err)
		os.Exit(1)
	} else {
		fmt.Printf("link.txt %04o\n", fi.Mode().Perm())
	}
}

func TestF13DefaultModes(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		umask     string
		file, dir os.FileMode
	}{
		{"0002", 0o664, 0o775},
		{"0022", 0o644, 0o755},
		{"0077", 0o600, 0o700},
	}
	for _, c := range cases {
		t.Run("umask-"+c.umask, func(t *testing.T) {
			ws := t.TempDir()
			// Existing-mode fixtures.
			if err := os.WriteFile(filepath.Join(ws, "keep0600.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ws, "keep0755.txt"), []byte("x"), 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(ws, "target.txt")
			if err := os.WriteFile(target, []byte("t"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(ws, "link.txt")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "-c",
				"umask "+c.umask+"; exec \"$0\" -test.run 'TestUmaskChild$' -test.v", self)
			cmd.Env = append(os.Environ(), "PSI_UMASK_PROBE="+ws)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child: %v\n%s", err, out)
			}
			modes := map[string]os.FileMode{}
			for _, line := range strings.Split(string(out), "\n") {
				f := strings.Fields(line)
				if len(f) != 2 {
					continue
				}
				n, perr := strconv.ParseUint(f[1], 8, 32)
				if perr != nil {
					continue
				}
				modes[f[0]] = os.FileMode(n)
			}
			check := func(path string, want os.FileMode) {
				t.Helper()
				got, ok := modes[path]
				if !ok {
					t.Fatalf("missing shape %s in %q", path, out)
				}
				if got != want {
					t.Errorf("%s: got %04o want %04o", path, got, want)
				}
			}
			check("new.txt", c.file)
			check(filepath.Join("nested", "deep", "file.txt"), c.file)
			check("nested", c.dir)
			check("keep0600.txt", 0o600)
			check("keep0755.txt", 0o755)
			check("link.txt", 0o640) // write through symlink preserves target
		})
	}
}
