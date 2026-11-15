package mcp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigMergePartialOverride(t *testing.T) {
	dir := t.TempDir()
	user := writeTestFile(t, dir, "mcp.json", `{
		"mcpServers": {
			"files": {
				"command": "node",
				"args": ["server.js"],
				"env": {"TOKEN": "user-token"},
				"enabled": true
			}
		}
	}`)
	project := writeTestFile(t, dir, "project.json", `{
		"mcpServers": {
			"files": {"enabled": false, "exposure": "direct", "toolExposure": {"read_*": "hidden"}}
		}
	}`)
	cfg, err := LoadConfig(user, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %d", len(cfg.Servers))
	}
	server := cfg.Servers[0]
	if server.Enabled {
		t.Error("partial override did not disable the server")
	}
	if server.Command != "node" || server.Env["TOKEN"] != "user-token" {
		t.Errorf("partial override lost the user connection definition: %+v", server)
	}
	if server.EnvSource["TOKEN"] != "user" {
		t.Errorf("credential provenance = %q", server.EnvSource["TOKEN"])
	}
	if server.Exposure != ExposureDirect {
		t.Errorf("exposure = %q", server.Exposure)
	}
	if len(server.ToolRules) != 1 || server.ToolRules[0].Pattern != "read_*" {
		t.Errorf("tool rules = %+v", server.ToolRules)
	}
}

func TestConfigProjectCommandRequiresOptIn(t *testing.T) {
	dir := t.TempDir()
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {}}`)
	project := writeTestFile(t, dir, "project.json", `{
		"mcpServers": {
			"remote": {"command": "node", "env": {"TOKEN": "!printf project-token"}}
		}
	}`)
	cfg, err := LoadConfig(user, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || !cfg.Servers[0].ProjectCommand {
		t.Fatalf("project command provenance not recorded: %+v", cfg.Servers)
	}
	if _, err := Resolve(context.Background(), cfg.Servers[0], false); err == nil {
		t.Fatal("project command ran without the user opt-in")
	}
	resolved, err := Resolve(context.Background(), cfg.Servers[0], true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Env["TOKEN"] != "project-token" {
		t.Errorf("resolved token = %q", resolved.Env["TOKEN"])
	}
}

func TestConfigDuplicateKeys(t *testing.T) {
	dir := t.TempDir()
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {"a": {"command": "x", "enabled": true, "enabled": false}}}`)
	cfg, err := LoadConfig(user, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 0 || len(cfg.Warnings) == 0 {
		t.Fatalf("duplicate entry field should fail only its entry: %+v", cfg)
	}

	writeTestFile(t, dir, "root.json", `{"mcpServers": {}, "mcpServers": {}}`)
	if _, err := LoadConfig(filepath.Join(dir, "root.json"), "", false); err == nil {
		t.Fatal("duplicate root key accepted")
	}

	writeTestFile(t, dir, "names.json", `{"mcpServers": {"a": {"command": "x"}, "a": {"command": "y"}}}`)
	if _, err := LoadConfig(filepath.Join(dir, "names.json"), "", false); err == nil {
		t.Fatal("duplicate server name accepted")
	}

	writeTestFile(t, dir, "siblings.json", `{"mcpServers": {
		"bad": {"command": "x", "env": {"A": "1", "A": "2"}},
		"good": {"command": "node"}
	}}`)
	cfg, err = LoadConfig(filepath.Join(dir, "siblings.json"), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "good" {
		t.Fatalf("sibling recovery failed: %+v", cfg)
	}
}

func TestConfigNullAndCollision(t *testing.T) {
	dir := t.TempDir()
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {
		"nullfield": {"command": "x", "enabled": null},
		"a-b": {"command": "x"},
		"a_b": {"command": "y"}
	}}`)
	cfg, err := LoadConfig(user, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "a-b" {
		t.Fatalf("collision/null handling = %+v", cfg.Servers)
	}
	if len(cfg.Warnings) < 2 {
		t.Errorf("warnings = %v", cfg.Warnings)
	}
}

func TestConfigExposureOrder(t *testing.T) {
	server := Server{Exposure: ExposureCodemode, ToolRules: []ExposureRule{
		{Pattern: "read_*", Exposure: ExposureHidden},
		{Pattern: "*", Exposure: ExposureDirect},
		{Pattern: "read_file", Exposure: ExposureDeferred},
	}}
	if got := effectiveExposure(server, "read_file"); got != ExposureDeferred {
		t.Errorf("exact match = %q", got)
	}
	if got := effectiveExposure(server, "read_dir"); got != ExposureHidden {
		t.Errorf("first wildcard = %q", got)
	}
	if got := effectiveExposure(server, "write_file"); got != ExposureDirect {
		t.Errorf("fallback wildcard = %q", got)
	}
	if got := effectiveExposure(server, "other"); got != ExposureDirect {
		t.Errorf("server default = %q", got)
	}
}

func TestConfigRootShapes(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "absent.json", `{}`)
	cfg, err := LoadConfig(filepath.Join(dir, "absent.json"), "", false)
	if err != nil || len(cfg.Servers) != 0 {
		t.Fatalf("absent mcpServers: %+v, %v", cfg, err)
	}
	writeTestFile(t, dir, "null.json", `{"mcpServers": null}`)
	if _, err := LoadConfig(filepath.Join(dir, "null.json"), "", false); err == nil {
		t.Fatal("null mcpServers accepted")
	}
	writeTestFile(t, dir, "array.json", `{"mcpServers": []}`)
	if _, err := LoadConfig(filepath.Join(dir, "array.json"), "", false); err == nil {
		t.Fatal("array mcpServers accepted")
	}
}

func TestConfigProjectNotReadWithoutTrust(t *testing.T) {
	dir := t.TempDir()
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {}}`)
	project := writeTestFile(t, dir, "project.json", `not json at all`)
	cfg, err := LoadConfig(user, project, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("untrusted project servers loaded: %+v", cfg.Servers)
	}
}

func TestConfigDisabledDoesNotResolve(t *testing.T) {
	dir := t.TempDir()
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {
		"off": {"command": "x", "enabled": false, "env": {"TOKEN": "!printf nope"}}
	}}`)
	cfg, err := LoadConfig(user, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Enabled {
		t.Fatalf("disabled entry was not retained: %+v", cfg.Servers)
	}
	if !reflect.DeepEqual(cfg.Servers[0].Env, map[string]string{"TOKEN": "!printf nope"}) {
		t.Errorf("disabled entry was interpolated at load: %+v", cfg.Servers[0].Env)
	}
}

func TestConfigTimeoutBounds(t *testing.T) {
	dir := t.TempDir()
	for _, value := range []string{"0", "-1", "1e300"} {
		user := writeTestFile(t, dir, "mcp-"+strings.ReplaceAll(value, "-", "neg")+".json", `{"mcpServers":{"bad":{"command":"x","timeout":`+value+`},"good":{"command":"node"}}}`)
		cfg, err := LoadConfig(user, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "good" {
			t.Fatalf("timeout %s did not fail only its entry: %+v", value, cfg.Servers)
		}
	}
}

func TestConfigRejectsTrailingData(t *testing.T) {
	dir := t.TempDir()
	bad := writeTestFile(t, dir, "bad.json", `{"mcpServers": {}} garbage`)
	if _, err := LoadConfig(bad, "", false); err == nil {
		t.Fatal("trailing JSON data was accepted")
	}
	good := writeTestFile(t, dir, "good.json", "{\"mcpServers\": {}}\n\t")
	if _, err := LoadConfig(good, "", false); err != nil {
		t.Fatalf("trailing whitespace was rejected: %v", err)
	}
}

func TestConfigRejectsHeaderCRLF(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "headers.json", `{"mcpServers": {
		"bad": {"url": "http://127.0.0.1:1/mcp", "headers": {"X-Test": "ok\r\nInjected: yes"}},
		"good": {"command": "node"}
	}}`)
	cfg, err := LoadConfig(path, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "good" {
		t.Fatalf("header injection entry was not rejected: %+v", cfg.Servers)
	}
}

func TestResolveRejectsInterpolatedHeaderCRLF(t *testing.T) {
	t.Setenv("MCP_HEADER_VALUE", "a\r\nb")
	server := Server{
		Name:         "header-env",
		Headers:      map[string]string{"X-Test": "${MCP_HEADER_VALUE}"},
		HeaderSource: map[string]string{"X-Test": "user"},
	}
	if _, err := Resolve(context.Background(), server, true); err == nil {
		t.Fatal("interpolated header CRLF was accepted")
	}
}
