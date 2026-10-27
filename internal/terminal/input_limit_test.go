package terminal

import (
	"strings"
	"testing"
)

func TestInsertBudgetRejectsWholeEdit(t *testing.T) {
	e := NewEditor()
	e.Insert(strings.Repeat("x", editorBudget-1))
	before, pos := e.Text(), e.Pos()
	if e.Insert("ab") || e.Text() != before || e.Pos() != pos {
		t.Fatal("over-budget edit partially inserted")
	}
	if !e.Insert("a") || len(e.Text()) != editorBudget {
		t.Fatal("exact-budget insert refused")
	}
}
func TestTypedBudgetHasOneNoticeAndKeepsDraft(t *testing.T) {
	c, _, raw := reviewConsole()
	c.Editor.Insert(strings.Repeat("x", editorBudget))
	before, pos := c.Editor.Text(), c.Editor.Pos()
	for i := 0; i < 3; i++ {
		c.Handle(Input{Key: []byte("a")}, false)
	}
	if c.Editor.Text() != before || c.Editor.Pos() != pos {
		t.Fatal("over-budget key changed draft")
	}
	if n := strings.Count(raw.String(), "Input exceeds 256 KiB. Draft unchanged."); n != 1 {
		t.Fatalf("%d limit notices", n)
	}
	c.Handle(Input{Key: []byte{127}}, false)
	c.Handle(Input{Key: []byte("a")}, false)
	if len(c.Editor.Text()) != editorBudget {
		t.Fatal("editing did not recover after limit notice")
	}
}
