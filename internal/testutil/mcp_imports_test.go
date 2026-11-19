package testutil

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMCPSDKImportBoundary(t *testing.T) {
	// go test runs in internal/testutil, two directories below the repo root.
	if err := checkMCPSDKImports(os.DirFS("../..")); err != nil {
		t.Fatal(err)
	}
}

func TestMCPSDKImportBoundaryFixtures(t *testing.T) {
	source := fstest.MapFS{
		"cmd/psi/main.go":          {Data: []byte("package main\n// The MCP SDK is github.com/modelcontextprotocol/go-sdk.\n")},
		"internal/mcp/catalog.go":  {Data: []byte("package mcp\nimport sdk \"github.com/modelcontextprotocol/go-sdk/mcp\"\n")},
		"internal/app/mcp_test.go": {Data: []byte("package app\nimport sdk \"github.com/modelcontextprotocol/go-sdk/mcp\"\n")},
	}
	if err := checkMCPSDKImports(source); err != nil {
		t.Fatalf("allowed files: %v", err)
	}
	const forbidden = "internal/mcphelper/catalog.go"
	source[forbidden] = &fstest.MapFile{Data: []byte("package mcphelper\nimport (\n sdk `github.com/modelcontextprotocol/go-sdk/mcp`\n)\n")}
	if err := checkMCPSDKImports(source); err == nil || !strings.HasPrefix(err.Error(), forbidden+" imports ") {
		t.Fatalf("production SDK import outside internal/mcp was not rejected: %v", err)
	}
}

func checkMCPSDKImports(source fs.FS) error {
	const sdkModule = "github.com/modelcontextprotocol/go-sdk"
	for _, root := range []string{"cmd", "internal"} {
		if err := fs.WalkDir(source, root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == "internal/mcp" {
				return fs.SkipDir
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := fs.ReadFile(source, path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return fmt.Errorf("%s: invalid import path: %w", path, err)
				}
				if importPath == sdkModule || strings.HasPrefix(importPath, sdkModule+"/") {
					return fmt.Errorf("%s imports MCP SDK package %q outside internal/mcp", path, importPath)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
