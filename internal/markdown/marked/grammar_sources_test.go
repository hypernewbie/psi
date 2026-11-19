package marked

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// upstreamRules is the grammar of marked 18.0.11 as it is published in
// marked/src/rules.ts.
type upstreamRules struct {
	Comment string                       `json:"$comment"`
	Version string                       `json:"version"`
	Other   map[string]string            `json:"other"`
	Block   map[string]map[string]string `json:"-"`
	Inline  map[string]map[string]string `json:"-"`
}

// loadUpstreamRules reads the recorded upstream grammar.
func loadUpstreamRules(t *testing.T) (map[string]map[string]string, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := json.Unmarshal(raw["version"], &version); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	for key, value := range raw {
		switch key {
		case "$comment", "version":
			continue
		}
		var rules map[string]string
		if err := json.Unmarshal(value, &rules); err != nil {
			t.Fatal(err)
		}
		out[key] = rules
	}
	return out, version
}

// normalizeRuleSource makes the two spellings of an equivalent expression
// compare equal.
//
// marked records the literal source of a regular expression. A JavaScript
// expression may escape a slash (`\/`) and may write a newline either as an
// escape (`\n`) or as the character itself. Both spellings describe the same
// expression, so the comparison normalizes them.
func normalizeRuleSource(source string) string {
	source = strings.ReplaceAll(source, `\/`, "/")
	source = strings.ReplaceAll(source, `\n`, "\n")
	source = strings.ReplaceAll(source, `\t`, "\t")
	return source
}

// TestGrammarSourcesMatchUpstream checks that the port builds the same
// expressions as marked 18.0.11. A difference here changes how the whole
// parser behaves, so the check is on the expression sources and not on the
// parsed result.
func TestGrammarSourcesMatchUpstream(t *testing.T) {
	upstream, version := loadUpstreamRules(t)
	if version != "18.0.11" {
		t.Fatalf("recorded grammar is marked %s, want 18.0.11", version)
	}

	port := GrammarSources()
	port["other"] = OtherSources()

	for _, group := range SortedKeys(upstream) {
		want := upstream[group]
		got, ok := port[group]
		if !ok {
			t.Errorf("the port has no %s grammar", group)
			continue
		}
		for _, name := range SortedKeys(want) {
			expected := want[name]
			if expected == "[object Object]" {
				// marked's `noopTest` has no source.
				expected = ""
			}
			actual, ok := got[name]
			if !ok {
				t.Errorf("%s.%s is missing from the port", group, name)
				continue
			}
			if normalizeRuleSource(actual) != normalizeRuleSource(expected) {
				t.Errorf("%s.%s differs from marked\n  upstream: %q\n  port:     %q",
					group, name, expected, actual)
			}
		}
	}
}
