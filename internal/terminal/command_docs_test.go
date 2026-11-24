package terminal

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/docs"
)

const commandDocFormatError = "docs/keyboard.md Commands format error"

// TestCommandDocsParity keeps the static command declarations in the shipped
// keyboard topic aligned with the commands the terminal accepts. It checks
// names only: dispatch and argument behavior stay in their own tests.
func TestCommandDocsParity(t *testing.T) {
	body, ok := docs.Get("keyboard")
	if !ok {
		t.Fatal(`docs.Get("keyboard") did not resolve`)
	}
	missing, stale, err := commandDocDrift(body, CommandNames())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range missing {
		t.Errorf("docs/keyboard.md Commands: missing declaration /%s", name)
	}
	for _, name := range stale {
		t.Errorf("docs/keyboard.md Commands: documented but not registered /%s", name)
	}
}

// TestCommandDocsParityRejectsDrift pins the parser rules with fixed inputs.
// The shipped document is not used, so wording changes do not weaken this
// regression test.
func TestCommandDocsParityRejectsDrift(t *testing.T) {
	registered := []string{"b", "bump"}

	t.Run("description and prose do not declare commands", func(t *testing.T) {
		doc := "# Keyboard\n\n" +
			"Commands (test):\n\n" +
			"- `/b` — request another response, like `/bump` does\n\n" +
			"`/bump` is mentioned here as prose.\n"
		missing, stale, err := commandDocDrift(doc, registered)
		if err != nil {
			t.Fatal(err)
		}
		assertNames(t, "missing", missing, "bump")
		assertNames(t, "stale", stale)
	})

	t.Run("grouped labels and blank-separated rows with CRLF", func(t *testing.T) {
		doc := "Commands (test):\r\n\r\n" +
			"- `/b` and `/bump` — grouped retry\r\n\r\n" +
			"- `/retired-command` — removed command\r\n"
		missing, stale, err := commandDocDrift(doc, registered)
		if err != nil {
			t.Fatal(err)
		}
		assertNames(t, "missing", missing)
		assertNames(t, "stale", stale, "retired-command")
	})

	t.Run("only the dynamic skill placeholder is exempt", func(t *testing.T) {
		doc := "Commands (test):\n\n" +
			"- `/b`, `/bump` — retry\n" +
			"- `/skill:name [args]` — invoke a skill\n" +
			"- `/skill:retired` — removed declaration\n"
		missing, stale, err := commandDocDrift(doc, registered)
		if err != nil {
			t.Fatal(err)
		}
		assertNames(t, "missing", missing)
		assertNames(t, "stale", stale, "skill:retired")
	})

	t.Run("missing Commands heading is an error", func(t *testing.T) {
		doc := "# Keyboard\n\n- `/b` — request another response\n"
		if _, _, err := commandDocDrift(doc, registered); err == nil {
			t.Fatal("expected a format error for a missing Commands heading")
		} else if !strings.Contains(err.Error(), commandDocFormatError) {
			t.Fatalf("error %q does not identify the Commands format", err)
		}
	})

	t.Run("unquoted text after a label is an error", func(t *testing.T) {
		doc := "Commands (test):\n\n- `/b` and `/bump` extra — grouped retry\n"
		if _, _, err := commandDocDrift(doc, registered); err == nil {
			t.Fatal("expected a format error for unquoted text after the labels")
		} else if !strings.Contains(err.Error(), commandDocFormatError) {
			t.Fatalf("error %q does not identify the Commands format", err)
		}
	})
}

func assertNames(t *testing.T, kind string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s names = %v, want %v", kind, got, want)
	}
}

// commandDocDrift compares the static Commands declarations with registered
// names. The returned lists are sorted. A parse failure means the document no
// longer has the reviewed Commands shape.
func commandDocDrift(doc string, registered []string) (missing, stale []string, err error) {
	documented, err := parseCommandDocNames(doc)
	if err != nil {
		return nil, nil, err
	}
	registeredSet := make(map[string]bool, len(registered))
	for _, name := range registered {
		registeredSet[name] = true
	}
	for name := range documented {
		if !registeredSet[name] {
			stale = append(stale, name)
		}
	}
	for name := range registeredSet {
		if !documented[name] {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	slices.Sort(stale)
	return missing, stale, nil
}

func parseCommandDocNames(doc string) (map[string]bool, error) {
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	heading := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "Commands (") && strings.HasSuffix(line, "):") {
			heading = i
			break
		}
	}
	if heading < 0 {
		return nil, fmt.Errorf(`%s: missing "Commands (...):" heading`, commandDocFormatError)
	}

	names := make(map[string]bool)
	sawRow := false
	blankAfterRow := false
	for i := heading + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			if sawRow {
				blankAfterRow = true
			}
			continue
		}
		if strings.HasPrefix(line, "- ") {
			rowNames, err := parseCommandDocRow(line)
			if err != nil {
				return nil, fmt.Errorf("%s at line %d: %q: %w", commandDocFormatError, i+1, line, err)
			}
			for _, name := range rowNames {
				names[name] = true
			}
			sawRow = true
			blankAfterRow = false
			continue
		}
		if !sawRow {
			return nil, fmt.Errorf("%s at line %d: %q: expected a \"- \" command row", commandDocFormatError, i+1, line)
		}
		if blankAfterRow {
			// The first prose line after a blank ends the Commands list.
			break
		}
		return nil, fmt.Errorf("%s at line %d: %q: expected a blank line before prose", commandDocFormatError, i+1, line)
	}
	if !sawRow {
		return nil, fmt.Errorf("%s: empty command list", commandDocFormatError)
	}
	return names, nil
}

func parseCommandDocRow(line string) ([]string, error) {
	left, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), " — ")
	if !ok {
		return nil, errors.New("missing em dash separator before the description")
	}
	left = strings.TrimSpace(left)
	var names []string
	for {
		quoted, ok := strings.CutPrefix(left, "`")
		if !ok {
			return nil, fmt.Errorf("expected a backtick-quoted label near %q", left)
		}
		label, rest, ok := strings.Cut(quoted, "`")
		if !ok {
			return nil, fmt.Errorf("unbalanced backtick near %q", left)
		}
		fields := strings.Fields(label)
		if len(fields) == 0 {
			return nil, errors.New("empty command label")
		}
		name, ok := strings.CutPrefix(fields[0], "/")
		if !ok || name == "" {
			return nil, fmt.Errorf("label %q is not a slash command", label)
		}
		// The keyboard topic uses /skill:name as the placeholder for skills
		// resolved at invocation time, not as a static command declaration.
		if name != "skill:name" {
			names = append(names, name)
		}
		left = rest
		if left == "" {
			return names, nil
		}
		switch {
		case strings.HasPrefix(left, ", and "):
			left = strings.TrimPrefix(left, ", and ")
		case strings.HasPrefix(left, ", "):
			left = strings.TrimPrefix(left, ", ")
		case strings.HasPrefix(left, " and "):
			left = strings.TrimPrefix(left, " and ")
		default:
			return nil, fmt.Errorf("unexpected text %q between labels", left)
		}
	}
}
