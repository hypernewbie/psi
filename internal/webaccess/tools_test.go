package webaccess

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestDefinitionsFollowEnablement(t *testing.T) {
	cfg := testConfig()
	client := testClient(cfg)
	defs := client.Definitions()
	var names []string
	for _, def := range defs {
		names = append(names, def.Name)
		if def.Schema["additionalProperties"] != false {
			t.Fatalf("%s accepts unknown arguments", def.Name)
		}
	}
	if want := []string{ToolSearch, ToolFetch, ToolContent}; !reflect.DeepEqual(names, want) {
		t.Fatalf("definitions = %v, want %v", names, want)
	}

	cfg.FetchEnabled = false
	cfg.SearchEnabled = false
	if defs := testClient(cfg).Definitions(); len(defs) != 1 || defs[0].Name != ToolContent {
		t.Fatalf("definitions after disable = %#v", defs)
	}
}

func TestFetchDefinitionMatchesAcceptedModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "default", cfg: Config{}, want: "readable"},
		{name: "configured", cfg: Config{FetchModes: []string{"raw"}, FetchMode: "raw"}, want: "raw"},
		{name: "stale default", cfg: Config{FetchModes: []string{"raw"}, FetchMode: "readable"}, want: "raw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := testClient(tc.cfg).fetchDefinition()
			mode := def.Schema["properties"].(map[string]any)["mode"].(map[string]any)
			enum := mode["enum"].([]string)
			if len(enum) == 0 || enum[0] != tc.want {
				t.Fatalf("advertised modes = %v, want first %q", enum, tc.want)
			}
			args, err := parseFetchArgs(map[string]any{"url": "https://example.com"}, tc.cfg)
			if err != nil {
				t.Fatalf("default fetch args: %v", err)
			}
			if args.mode != tc.want {
				t.Fatalf("default mode = %q, want %q", args.mode, tc.want)
			}
		})
	}
}

func TestDispatchCancellationUsesToolName(t *testing.T) {
	client := testClient(testConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{ToolFetch, ToolSearch, ToolContent} {
		result := client.Dispatch(ctx, toolsInvocation(name, nil))
		if !result.IsError || !strings.HasPrefix(result.Content[0].Text, name+" failed: ") {
			t.Fatalf("%s cancellation result: %+v", name, result)
		}
		if !strings.Contains(result.Content[0].Text, classCancelled) {
			t.Fatalf("%s cancellation class: %s", name, result.Content[0].Text)
		}
	}
}
