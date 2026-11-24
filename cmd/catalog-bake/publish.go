package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// checkDestination enforces the published output contract: one path whose
// parent already exists, and no pre-existing target of any kind.
func checkDestination(path string) error {
	if path == "" {
		return fmt.Errorf("output path is empty")
	}
	parent := filepath.Dir(path)
	if parent == path {
		return fmt.Errorf("output path %q has no parent directory", path)
	}
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("output parent %q: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("output parent %q is not a directory", parent)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("output path %q already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("output path %q: %w", path, err)
	}
	return nil
}

// publish writes both documents into a sibling staging directory and moves
// that directory to the requested path in one rename. A failure removes only
// this invocation's staging directory.
func publish(ctx context.Context, path string, files map[string][]byte) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	staging, err := os.MkdirTemp(parent, ".catalog-bake-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(staging, name), files[name], 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkDestination(path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(staging, path); err != nil {
		return fmt.Errorf("publish %q: %w", path, err)
	}
	committed = true
	return nil
}

type bakeDeps struct {
	fetch    func(context.Context) (fetchedInputs, error)
	now      func() time.Time
	warnings io.Writer
}

// bake is the tested end-to-end entry point used by main and by the private
// failure-path tests. It checks the destination before fetching.
func bake(ctx context.Context, out string, deps bakeDeps) error {
	if deps.fetch == nil {
		deps.fetch = fetchCatalogInputs
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if err := checkDestination(out); err != nil {
		return err
	}
	inputs, err := deps.fetch(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	catalogBytes, sourceBytes, err := assembleArtifacts(inputs, providerDefinitions(), true, deps.now, deps.warnings)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publish(ctx, out, map[string][]byte{
		"catalog.json":        catalogBytes,
		"catalog-source.json": sourceBytes,
	})
}
