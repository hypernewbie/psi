package docs

import (
	"strings"
	"testing"
)

func TestEveryTopicResolves(t *testing.T) {
	for _, topic := range Topics {
		body, ok := Get(topic.Name)
		if !ok || strings.TrimSpace(body) == "" {
			t.Errorf("topic %q did not resolve", topic.Name)
		}
	}
}

func TestListMatchesTopics(t *testing.T) {
	list := List()
	lines := strings.Split(strings.TrimSuffix(list, "\n"), "\n")
	if len(lines) != len(Topics) {
		t.Fatalf("List() has %d lines, want %d:\n%s", len(lines), len(Topics), list)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, Topics[i].Name+" - ") {
			t.Errorf("List() line %d = %q, want prefix %q", i, line, Topics[i].Name)
		}
	}
	if !strings.HasSuffix(list, "\n") {
		t.Error("List() must end with a newline")
	}
}

func TestInternalDocsStayHidden(t *testing.T) {
	for _, name := range []string{"acceptance", "ui-acceptance", "reference-manifest", "plan5-acceptance"} {
		if body, ok := Get(name); ok {
			t.Errorf("Get(%q) returned %d bytes, want not found", name, len(body))
		}
	}
}
