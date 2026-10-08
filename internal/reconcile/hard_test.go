package reconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

type fakeExecutor struct {
	calls    []string
	renumber string
	failOn   string
	created  []model.Window
	selected int
	state    *model.Session
	nextID   int
}

func (f *fakeExecutor) add(call string) error {
	f.calls = append(f.calls, call)
	if f.failOn == call {
		return errors.New("injected failure")
	}
	return nil
}
func (f *fakeExecutor) Freeze(context.Context, string) (*model.Session, error) {
	if f.state == nil {
		return nil, errors.New("fake state not set")
	}
	return cloneSession(f.state), nil
}
func (f *fakeExecutor) MoveWindowTo(_ context.Context, session, id string, from, to int) error {
	call := fmt.Sprintf("move:%s:%d:%d", id, from, to)
	if err := f.add(call); err != nil {
		return err
	}
	for i := range f.state.Windows {
		w := &f.state.Windows[i]
		if (id != "" && w.TmuxID == id) || (id == "" && w.Idx == from) {
			w.Idx = to
			return nil
		}
	}
	return errors.New("fake move source missing")
}
func (f *fakeExecutor) NewWindowAt(_ context.Context, session string, idx int, w model.Window, _ string) error {
	call := fmt.Sprintf("new:%d:%s", idx, w.Name)
	if err := f.add(call); err != nil {
		return err
	}
	w.Idx, w.TmuxID = idx, f.nextTmuxID("@")
	w.Panes = append([]model.Pane(nil), w.Panes...)
	for i := range w.Panes {
		w.Panes[i].Idx = i
		w.Panes[i].TmuxID = f.nextTmuxID("%")
	}
	if f.state.Name == "" {
		f.state.Name = session
	}
	f.state.Windows = append(f.state.Windows, w)
	f.created = append(f.created, w)
	return nil
}
func (f *fakeExecutor) KillWindowAt(_ context.Context, _, id string, idx int) error {
	if err := f.add(fmt.Sprintf("kill:%s:%d", id, idx)); err != nil {
		return err
	}
	for i, w := range f.state.Windows {
		if (id != "" && w.TmuxID == id) || (id == "" && w.Idx == idx) {
			f.state.Windows = append(f.state.Windows[:i], f.state.Windows[i+1:]...)
			return nil
		}
	}
	return errors.New("fake kill target missing")
}
func (f *fakeExecutor) AddPaneToWindow(_ context.Context, _, id string, idx int, pane model.Pane) error {
	if err := f.add(fmt.Sprintf("pane:%s:%d", id, idx)); err != nil {
		return err
	}
	w := f.findWindow(id, idx)
	if w == nil {
		return errors.New("fake pane target missing")
	}
	pane.Idx, pane.TmuxID = len(w.Panes), f.nextTmuxID("%")
	w.Panes = append(w.Panes, pane)
	return nil
}
func (f *fakeExecutor) RenameWindowAt(_ context.Context, _, id string, idx int, name string) error {
	if err := f.add(fmt.Sprintf("rename:%s:%d:%s", id, idx, name)); err != nil {
		return err
	}
	w := f.findWindow(id, idx)
	if w == nil {
		return errors.New("fake rename target missing")
	}
	w.Name = name
	return nil
}
func (f *fakeExecutor) ApplyLayoutAt(_ context.Context, _, id string, idx int, layout string) error {
	if err := f.add(fmt.Sprintf("layout:%s:%d:%s", id, idx, layout)); err != nil {
		return err
	}
	w := f.findWindow(id, idx)
	if w == nil {
		return errors.New("fake layout target missing")
	}
	w.Layout = layout
	return nil
}
func (f *fakeExecutor) ShowOption(_ context.Context, session, _ string) (string, error) {
	if session == "" {
		return f.renumber, nil
	}
	return "", nil
}
func (f *fakeExecutor) SetOption(_ context.Context, _, name, value string) error {
	return f.add("set:" + name + ":" + value)
}
func (f *fakeExecutor) UnsetOption(_ context.Context, _, name string) error {
	return f.add("unset:" + name)
}
func (f *fakeExecutor) SelectWindow(_ context.Context, _ string, idx int) error {
	f.selected = idx
	return f.add(fmt.Sprintf("select:%d", idx))
}

func (f *fakeExecutor) nextTmuxID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%d", prefix, f.nextID)
}

func (f *fakeExecutor) findWindow(id string, idx int) *model.Window {
	for i := range f.state.Windows {
		w := &f.state.Windows[i]
		if (id != "" && w.TmuxID == id) || (id == "" && w.Idx == idx) {
			return w
		}
	}
	return nil
}

func TestHardApplyKeepsSessionAliveAndRebuildsFromBaseline(t *testing.T) {
	root := t.TempDir()
	base := &model.Session{Name: "s", Cwd: root, Windows: []model.Window{
		{Idx: 0, Name: "editor", Layout: "even-vertical", Panes: []model.Pane{{Cwd: root, Cmd: "nvim"}, {Cwd: root}}},
		{Idx: 2, Name: "files", Panes: []model.Pane{{Cwd: root, Cmd: "yazi"}}},
	}}
	live := &model.Session{Name: "s", Cwd: root, ServerKey: "server", Windows: []model.Window{
		{Idx: 0, Name: "changed", TmuxID: "@4", Panes: []model.Pane{{TmuxID: "%7"}}},
	}}
	ops := &fakeExecutor{renumber: "on", state: cloneSession(live)}
	report, err := HardApply(context.Background(), ops, "s", base, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.created) != 3 || ops.created[0].Name != "__gotomux_reconcile_anchor" || ops.created[1].Name != "editor" || ops.created[1].Layout != "even-vertical" || ops.created[2].Name != "files" {
		t.Fatalf("created windows = %+v", ops.created)
	}
	if ops.calls[0] != "set:renumber-windows:off" || ops.calls[1] != "new:3:__gotomux_reconcile_anchor" || ops.calls[2] != "kill:@4:0" {
		t.Fatalf("operation prefix = %v", ops.calls[:3])
	}
	if got := ops.calls[len(ops.calls)-3:]; got[0] != "kill:@1:3" || got[1] != "select:0" || got[2] != "unset:renumber-windows" {
		t.Fatalf("operation suffix = %v", got)
	}
	if report == "" || ops.selected != 0 {
		t.Fatalf("report=%q active=%d", report, ops.selected)
	}
}

func TestHardApplyStopsOnPartialTmuxFailureAndRestoresOption(t *testing.T) {
	root := t.TempDir()
	base := &model.Session{Name: "s", Cwd: root, Windows: []model.Window{{Idx: 0, Name: "editor", Panes: []model.Pane{{Cwd: root}}}}}
	live := &model.Session{Name: "s", Cwd: root, ServerKey: "server", Windows: []model.Window{{Idx: 0, Name: "old", TmuxID: "@3", Panes: []model.Pane{{Cwd: root}}}}}
	ops := &fakeExecutor{renumber: "on", failOn: "kill:@3:0", state: cloneSession(live)}
	if _, err := HardApply(context.Background(), ops, "s", base, live); err == nil {
		t.Fatal("expected injected failure")
	}
	if ops.calls[len(ops.calls)-1] != "unset:renumber-windows" {
		t.Fatalf("global option override was not cleared: %v", ops.calls)
	}
}

func TestHardApplyPreflightsBaselineBeforeAnyTmuxMutation(t *testing.T) {
	base := &model.Session{Name: "s", Cwd: "/path/that/does/not/exist", Windows: []model.Window{{Idx: 0, Name: "editor", Panes: []model.Pane{{Cwd: "/path/that/does/not/exist"}}}}}
	live := &model.Session{Name: "s", Cwd: "/tmp", ServerKey: "server", Windows: []model.Window{{Idx: 0, Name: "live", TmuxID: "@7", Panes: []model.Pane{{Cwd: "/tmp"}}}}}
	ops := &fakeExecutor{renumber: "off"}
	if _, err := HardApply(context.Background(), ops, "s", base, live); err == nil {
		t.Fatal("expected unavailable baseline cwd error")
	}
	if len(ops.calls) != 0 {
		t.Fatalf("preflight failure mutated tmux: %v", ops.calls)
	}
}

func TestHardApplyAbortsWhenLiveStateChangedAfterSnapshot(t *testing.T) {
	root := t.TempDir()
	base := &model.Session{Name: "s", Cwd: root, Windows: []model.Window{{Idx: 0, Name: "editor", Panes: []model.Pane{{Cwd: root}}}}}
	live := &model.Session{Name: "s", Cwd: root, ServerKey: "srv", Windows: []model.Window{{Idx: 0, Name: "editor", TmuxID: "@1", Panes: []model.Pane{{Cwd: root, TmuxID: "%1"}}}}}
	state := cloneSession(live)
	state.Windows[0].Name = "changed after snapshot"
	ops := &fakeExecutor{renumber: "off", state: state}
	if _, err := HardApply(context.Background(), ops, "s", base, live); err == nil {
		t.Fatal("expected hard precondition failure")
	}
	if len(ops.calls) != 0 {
		t.Fatalf("hard reconcile mutated changed state: %v", ops.calls)
	}
}

func TestApplyExecutesExtraWindowMoveAfterVacatingBaselineRange(t *testing.T) {
	root := t.TempDir()
	base := &model.Session{Name: "s", Cwd: root, ServerKey: "srv", Windows: []model.Window{
		{Idx: 0, Name: "editor", Cwd: root, TmuxID: "@1", Panes: []model.Pane{{Cmd: "nvim", Cwd: root}}},
		{Idx: 1, Name: "shell", Cwd: root, TmuxID: "@2", Panes: []model.Pane{{Cwd: root}}},
	}}
	live := &model.Session{Name: "s", Cwd: root, ServerKey: "srv", Windows: []model.Window{
		{Idx: 0, Name: "shell", Cwd: root, TmuxID: "@2", Panes: []model.Pane{{Cwd: root}}},
		{Idx: 1, Name: "extra", Cwd: root, TmuxID: "@9", Panes: []model.Pane{{Cmd: "htop", Cwd: root}}},
	}}
	p := Build(base, live, 0)
	if len(p.Missing) != 1 || len(p.Extra) != 1 || len(p.Extras) != 1 || !p.NeedsApply {
		t.Fatalf("plan = %+v", p)
	}
	ops := &fakeExecutor{renumber: "off", state: cloneSession(live)}
	if _, err := Apply(context.Background(), ops, "s", root, p); err != nil {
		t.Fatal(err)
	}
	want := "move:@9:3:2"
	found := false
	for _, call := range ops.calls {
		if call == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("extra window relocation not executed: %v", ops.calls)
	}
}

func TestApplyPreflightsMissingWindowPathsBeforeMovingLiveWindows(t *testing.T) {
	plan := Plan{
		ServerKey:  "server",
		NeedsApply: true,
		Moves:      []Move{{WindowID: "@7", From: 0, To: 9}},
		Creates:    []CreateWindow{{Index: 0, Window: model.Window{Idx: 0, Name: "missing", Panes: []model.Pane{{Cwd: "/path/that/does/not/exist"}}}}},
	}
	ops := &fakeExecutor{renumber: "off", state: &model.Session{Name: "s", Cwd: "/tmp", ServerKey: "server"}}
	if _, err := Apply(context.Background(), ops, "s", "/tmp", plan); err == nil {
		t.Fatal("expected preflight cwd failure")
	}
	if len(ops.calls) != 0 {
		t.Fatalf("preflight failure ran tmux operations: %v", ops.calls)
	}
}

func TestApplyAbortsWhenObservedWindowChangedAfterPlanning(t *testing.T) {
	root := t.TempDir()
	base := &model.Session{Name: "s", Cwd: root, ServerKey: "srv", Windows: []model.Window{{Idx: 1, Name: "editor", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "nvim", TmuxID: "%1"}}}}}
	live := &model.Session{Name: "s", Cwd: root, ServerKey: "srv", Windows: []model.Window{{Idx: 0, Name: "editor", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "nvim", TmuxID: "%1"}}}}}
	p := Build(base, live, 0)
	state := cloneSession(live)
	state.Windows[0].Name = "renamed after planner snapshot"
	ops := &fakeExecutor{renumber: "off", state: state}
	if _, err := Apply(context.Background(), ops, "s", root, p); err == nil {
		t.Fatal("expected stale-plan precondition failure")
	}
	if len(ops.calls) != 0 {
		t.Fatalf("stale plan reached tmux mutations: %v", ops.calls)
	}
}
