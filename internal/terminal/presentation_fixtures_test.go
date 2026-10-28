package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type plan4ComponentFixture struct {
	Reference string               `json:"reference"`
	Size      string               `json:"size"`
	Snapshot  PresentationSnapshot `json:"snapshot"`
	PlainRows []string             `json:"plainRows"`
}

func TestPlan4BaselineFixtureIsSyntheticAndCanaryFree(t *testing.T) {
	data, err := os.ReadFile("testdata/plan4-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PsiRevision string   `json:"psiRevision"`
		PiRevision  string   `json:"piRevision"`
		Offline     []string `json:"offlineStartup40x12"`
		MustNot     []string `json:"mustNotContain"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.PsiRevision != "9baf1d786b367417592d173f4299ddc2c8fe5985" || fixture.PiRevision != "d5b3de22d0e24a3e9c38a9410d3329b099717283" {
		t.Fatalf("baseline revisions drifted: %+v", fixture)
	}
	capture := strings.Join(fixture.Offline, "\n")
	for _, canary := range fixture.MustNot {
		if strings.Contains(capture, canary) {
			t.Fatalf("baseline fixture contains hidden/private canary %q", canary)
		}
	}
	for _, private := range []string{"/home/hypernewbie", "/Users/", "/tmp/psi-plan4-"} {
		if strings.Contains(capture, private) {
			t.Fatalf("unsanitized path in fixture: %q", private)
		}
	}
}

func TestPlan4PiMarkdownFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/plan4-pi-markdown.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Reference   string   `json:"reference"`
		Size        string   `json:"size"`
		Width       int      `json:"width"`
		Source      string   `json:"source"`
		MessageRows []string `json:"messageRows"`
		StyleTokens []string `json:"styleTokens"`
		Omissions   []string `json:"omissions"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	got := FormatMessage(fixture.Source, fixture.Width, true)
	if len(got) != len(fixture.MessageRows) {
		t.Fatalf("message rows=%#v want %#v", got, fixture.MessageRows)
	}
	for i := range got {
		if got[i] != fixture.MessageRows[i] {
			t.Errorf("message row %d: got %q want %q", i, got[i], fixture.MessageRows[i])
		}
	}
	if !strings.Contains(fixture.Reference, "d5b3de22d0e24a3e9c38a9410d3329b099717283") || len(fixture.StyleTokens) == 0 || len(fixture.Omissions) == 0 {
		t.Fatal("Markdown fixture must identify pinned behavior and intentional omissions")
	}
}

func TestPlan4PiEditorAndFooterFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/plan4-pi-components.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture plan4ComponentFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.Snapshot.WorkingDirectory = filepath.FromSlash(fixture.Snapshot.WorkingDirectory)
	fixture.Snapshot.HomeDirectory = filepath.FromSlash(fixture.Snapshot.HomeDirectory)
	if len(fixture.PlainRows) <= 3 {
		t.Fatalf("component fixture is missing the footer path row: %q", fixture.PlainRows)
	}
	wantRows := append([]string(nil), fixture.PlainRows...)
	wantRows[3] = strings.ReplaceAll(wantRows[3], "/", string(filepath.Separator))
	view := ComposeEditorView(40, 12, nil, 0, fixture.Snapshot, "")
	if len(view.Rows) != len(wantRows) {
		t.Fatalf("rows=%d want=%d: %q", len(view.Rows), len(wantRows), view.Rows)
	}
	for i, row := range view.Rows {
		if got := Sanitize(row); got != wantRows[i] {
			t.Errorf("row %d: got %q want %q", i, got, wantRows[i])
		}
	}
	if !strings.Contains(fixture.Reference, "d5b3de22d0e24a3e9c38a9410d3329b099717283") {
		t.Fatal("expected component fixture lacks pinned Pi reference")
	}
}
