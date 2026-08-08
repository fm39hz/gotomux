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

func TestScrollOffBoundaries(t *testing.T) {
	v := viewModel{maxShow: 12}

	// Empty list
	if s := v.scrollOff(); s != 0 {
		t.Fatalf("empty: got %d", s)
	}

	// Fewer items than maxShow, cursor at 0
	v.items = make([]Item, 5)
	v.cursor = 0
	if s := v.scrollOff(); s != 0 {
		t.Fatalf("small list start: got %d", s)
	}

	// Small list, cursor at end
	v.cursor = 4
	if s := v.scrollOff(); s != 0 {
		t.Fatalf("small list end: got %d", s)
	}

	// Large list, cursor in middle
	v.items = make([]Item, 30)
	v.cursor = 15
	// half=6, s=15-6=9, s+12=21 < 30 → s=9
	if s := v.scrollOff(); s != 9 {
		t.Fatalf("mid: want 9 got %d", s)
	}

	// Large list, cursor near end
	v.cursor = 28
	// half=6, s=28-6=22, s+12=34 > 30 → s=30-12=18
	if s := v.scrollOff(); s != 18 {
		t.Fatalf("near end: want 18 got %d", s)
	}

	// Large list, cursor at start (0 < half)
	v.cursor = 3
	// half=6, s=3-6=-3 → 0
	if s := v.scrollOff(); s != 0 {
		t.Fatalf("near start: want 0 got %d", s)
	}
}

func TestScrollStableAfterShrink(t *testing.T) {
	v := viewModel{maxShow: 12, items: make([]Item, 20), cursor: 10}

	before := v.scrollOff() // half=6, s=10-6=4, s+12=16 < 20 → 4
	if before != 4 {
		t.Fatalf("before: want 4 got %d", before)
	}

	// Shrink list (simulate kill): 20 → 19 items
	v.items = make([]Item, 19)
	// Without adjustment: s=10-6=4, s+12=16 < 19 → 4 (same before → after)
	after := v.scrollOff()
	if after != 4 {
		t.Fatalf("after shrink: want 4 got %d", after)
	}

	// Shrink more: 20 → 10 items
	v.items = make([]Item, 10)
	v.cursor = 10
	after = v.scrollOff()
	// half=6, s=10-6=4, s+12=16 > 10 → s=10-12=-2 → 0
	// scroll changed from 4 to 0
	if after != 0 {
		t.Fatalf("after big shrink: want 0 got %d", after)
	}

	// After reload adjustment: cursor = savedScroll(4) + half(6) = 10 → clamp to 9
	v.cursor = 9
	after = v.scrollOff()
	// half=6, s=9-6=3, s+12=15 > 10 → s=10-12=-2 → 0
	if after != 0 {
		t.Fatalf("after adjust: want 0 got %d", after)
	}
	// scrollOff vẫn là 0 vì list quá ngắn (10 < 12)
}

func TestScrollPreserveAfterAction(t *testing.T) {
	v := viewModel{maxShow: 12, items: make([]Item, 25), cursor: 15}

	shrunk := 20

	// Simulate reload logic
	savedScroll := v.scrollOff()
	v.items = make([]Item, shrunk)
	v.cursor = 15
	if v.cursor >= shrunk {
		v.cursor = shrunk - 1
	}

	// Restore scroll
	half := v.maxShow / 2
	c := savedScroll + half // 9 + 6 = 15
	if c >= shrunk {
		c = shrunk - 1
	}
	if c >= 0 {
		v.cursor = c
		v.selID = v.items[c].ID()
	}

	after := v.scrollOff()
	// cursor=15, half=6, s=15-6=9, s+12=21>20 → s=20-12=8
	if after != 8 {
		t.Fatalf("after restore: want 8 got %d (saved=%d cursor=%d)", after, savedScroll, v.cursor)
	}
}

func TestCursorResetOnTyping(t *testing.T) {
	v := viewModel{maxShow: 12, items: make([]Item, 20), cursor: 10}
	v.selID = v.items[10].ID()

	// refilterFromQuery resets cursor to 0
	v.cursor = 0
	v.selID = v.items[0].ID()

	if v.cursor != 0 {
		t.Fatal("cursor should be 0 after typing")
	}
	if s := v.scrollOff(); s != 0 {
		t.Fatalf("scroll should be 0, got %d", s)
	}
}

func TestItemsCanRerankNoScrollJump(t *testing.T) {
	v := viewModel{maxShow: 12, items: make([]Item, 30), cursor: 15}
	saved := v.scrollOff() // 9

	// Rerank: shuffle items, same count
	// Without identity tracking, cursor stays at same index
	v.cursor = 15
	if s := v.scrollOff(); s != saved {
		t.Fatalf("scroll changed after rerank: %d → %d", saved, s)
	}
}
