package store_test

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/store"
)

func TestSessionToModelRoundtrip(t *testing.T) {
	p := &store.Preset{
		Name: "test",
		Cwd:  "/home/user/project",
		Windows: []store.PresetWindow{
			{Idx: 0, Name: "editor", Cwd: "/home/user/project", Layout: "even-horizontal",
				Panes: []store.PresetPane{
					{Idx: 0, Cwd: "/home/user/project", Cmd: "nvim"},
					{Idx: 1, Cwd: "/home/user/project/src", Cmd: ""},
				}},
			{Idx: 1, Name: "shell", Cwd: "/home/user/project",
				Panes: []store.PresetPane{{Idx: 0, Cwd: "/home/user/project", Cmd: ""}}},
		},
	}

	s := store.SessionToModel(p)
	if s.Name != "test" || s.Cwd != "/home/user/project" {
		t.Errorf("SessionToModel name/cwd = %q/%q", s.Name, s.Cwd)
	}
	if len(s.Windows) != 2 {
		t.Fatalf("SessionToModel windows = %d, want 2", len(s.Windows))
	}
	w0 := s.Windows[0]
	if w0.Name != "editor" || len(w0.Panes) != 2 {
		t.Errorf("window 0 = %+v", w0)
	}
	if w0.Panes[0].Cmd != "nvim" {
		t.Errorf("pane 0 cmd = %q, want nvim", w0.Panes[0].Cmd)
	}

	p2 := store.ModelToSession(s)
	if p2.Name != "test" || p2.Cwd != "/home/user/project" {
		t.Errorf("ModelToSession name/cwd = %q/%q", p2.Name, p2.Cwd)
	}
	if len(p2.Windows) != 2 {
		t.Fatalf("ModelToSession windows = %d, want 2", len(p2.Windows))
	}
}

func TestSessionToModelNil(t *testing.T) {
	if s := store.SessionToModel(nil); s != nil {
		t.Errorf("SessionToModel(nil) = %+v, want nil", s)
	}
}

func TestModelToSessionNil(t *testing.T) {
	if p := store.ModelToSession(nil); p != nil {
		t.Errorf("ModelToSession(nil) = %+v, want nil", p)
	}
}
