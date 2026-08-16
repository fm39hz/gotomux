package picker

import (
	"testing"
)

func TestViewKeepsBlankAnchorAfterDone(t *testing.T) {
	for _, action := range []Action{ActionConnect, ActionQuit} {
		m := model{ui: viewModel{done: Result{
			Action: action,
			Item:   Item{Title: "[Active] should-not-leak"},
		}}}
		if got := m.View().Content; got != " " {
			t.Fatalf("action %v final frame = %q, want one blank anchor", action, got)
		}
	}
}

func TestCancelMsgUsesNormalQuitTransition(t *testing.T) {
	m := model{}
	next, cmd := m.Update(cancelMsg{})
	got := next.(model)

	if got.Done().Action != ActionQuit {
		t.Fatalf("action = %v, want %v", got.Done().Action, ActionQuit)
	}
	if cmd == nil {
		t.Fatal("cancel did not return tea.Quit")
	}
	if frame := got.View().Content; frame != " " {
		t.Fatalf("final frame = %q, want one blank anchor", frame)
	}
}

func namedItems(n int) []Item {
	items := make([]Item, n)
	for i := range items {
		items[i].Name = string(rune('a' + i))
	}
	return items
}

func TestSelectionUsesCenterFollowViewport(t *testing.T) {
	v := viewModel{maxShow: 12, items: namedItems(30)}
	v.resetSelection()

	for range 6 {
		v.moveSelection(1)
	}
	if v.cursor != 6 || v.viewportStart != 0 {
		t.Fatalf("before center-follow: cursor=%d start=%d", v.cursor, v.viewportStart)
	}

	v.moveSelection(1)
	if v.cursor != 7 || v.viewportStart != 1 {
		t.Fatalf("after center-follow: cursor=%d start=%d, want 7/1", v.cursor, v.viewportStart)
	}

	v.moveSelection(-1)
	if v.cursor != 6 || v.viewportStart != 0 {
		t.Fatalf("reverse center-follow: cursor=%d start=%d", v.cursor, v.viewportStart)
	}
}

func TestSelectionCyclesAtBoundaries(t *testing.T) {
	v := viewModel{maxShow: 12, items: namedItems(20)}
	v.resetSelection()
	v.moveSelection(-1)
	if v.cursor != 19 || v.viewportStart != 8 {
		t.Fatalf("up at start: cursor=%d start=%d, want 19/8", v.cursor, v.viewportStart)
	}

	v.moveSelection(1)
	if v.cursor != 0 || v.viewportStart != 0 {
		t.Fatalf("down at end: cursor=%d start=%d, want 0/0", v.cursor, v.viewportStart)
	}
}

func TestSelectionClampsAfterListShrinks(t *testing.T) {
	v := viewModel{maxShow: 12, items: namedItems(25), cursor: 22, viewportStart: 13}
	v.syncViewport()
	v.items = v.items[:10]
	v.syncViewport()

	if v.cursor != 9 || v.viewportStart != 0 {
		t.Fatalf("after shrink: cursor=%d start=%d, want 9/0", v.cursor, v.viewportStart)
	}
}

func TestResetSelectionResetsViewport(t *testing.T) {
	v := viewModel{maxShow: 12, items: namedItems(20), cursor: 10, viewportStart: 4}
	v.resetSelection()
	if v.cursor != 0 || v.viewportStart != 0 {
		t.Fatalf("reset: cursor=%d start=%d", v.cursor, v.viewportStart)
	}
}

func TestViewUsesRealTextCursorWithPromptOffset(t *testing.T) {
	input := initInput()
	input.SetValue("ab")
	input.CursorEnd()
	m := model{ui: viewModel{queryInput: input, maxShow: 1}}

	view := m.View()
	if view.Cursor == nil {
		t.Fatal("view has no hardware cursor")
	}
	if view.Cursor.Position.X != 4 || view.Cursor.Position.Y != 0 {
		t.Fatalf("cursor = (%d,%d), want (4,0)", view.Cursor.Position.X, view.Cursor.Position.Y)
	}
}
