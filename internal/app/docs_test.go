package app

import (
	"bytes"
	"strings"
	"testing"

	"psi/docs"
	"psi/internal/testutil"
)

func TestParseDocsFlag(t *testing.T) {
	testutil.IsolatedEnv(t)
	cases := []struct {
		args  []string
		topic string
	}{
		{[]string{"--docs"}, ""},
		{[]string{"--docs", "config"}, "config"},
		{[]string{"--docs=config"}, "config"},
		{[]string{"--docs", "config", "hello"}, "config"},
	}
	for _, tc := range cases {
		o, _, err := Parse(tc.args)
		if err != nil {
			t.Fatalf("Parse(%v): %v", tc.args, err)
		}
		if !o.Docs || o.DocsTopic != tc.topic {
			t.Errorf("Parse(%v) docs=%v topic=%q, want true %q", tc.args, o.Docs, o.DocsTopic, tc.topic)
		}
	}
}

func TestPrintDocs(t *testing.T) {
	var output bytes.Buffer
	for _, topic := range []string{"", "list"} {
		output.Reset()
		if err := printDocs(&output, topic); err != nil {
			t.Fatalf("printDocs(%q): %v", topic, err)
		}
		lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
		if len(lines) != len(docs.Topics) {
			t.Fatalf("printDocs(%q) lines = %d, want %d:\n%s", topic, len(lines), len(docs.Topics), output.String())
		}
	}

	output.Reset()
	if err := printDocs(&output, "config"); err != nil {
		t.Fatal(err)
	}
	want, _ := docs.Get("config")
	if output.String() != want {
		t.Fatalf("printed config differs from the embedded topic: %q", output.String())
	}

	output.Reset()
	err := printDocs(&output, "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown docs topic") {
		t.Fatalf("unknown topic error = %v", err)
	}
	if !strings.Contains(err.Error(), "quickstart") {
		t.Fatalf("unknown topic error does not list topics: %v", err)
	}
}
