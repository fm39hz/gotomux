package template_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/fm39hz/gotomux/internal/event"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type mockStorer struct {
	store.Storer
	shapes   map[string]string // id -> body
	keys     map[string]string // key -> id
	stickyID string
	presets  map[string]*store.Preset
	ucalls   []string
}

func newMockStorer() *mockStorer {
	return &mockStorer{
		shapes:  map[string]string{},
		keys:    map[string]string{},
		presets: map[string]*store.Preset{},
	}
}

func (m *mockStorer) StickyID() string {
	if m.stickyID == "" {
		return "default"
	}
	return m.stickyID
}

func (m *mockStorer) SetSticky(id string) error {
	m.stickyID = id
	return nil
}

func (m *mockStorer) UpsertShapeByID(id, key, body string) error {
	m.shapes[id] = body
	m.keys[key] = id
	return nil
}

func (m *mockStorer) GetShape(id string) (string, bool) {
	b, ok := m.shapes[id]
	return b, ok
}

func (m *mockStorer) GetShapeByKey(key string) (string, string, bool) {
	id, ok := m.keys[key]
	if !ok {
		return "", "", false
	}
	return id, m.shapes[id], true
}

func (m *mockStorer) StickShape(shapeID, shapeKey, shapeBody string) (string, bool, error) {
	m.shapes[shapeID] = shapeBody
	m.keys[shapeKey] = shapeID
	m.stickyID = shapeID
	return shapeID, true, nil
}

func (m *mockStorer) SaveFreeze(p *store.Preset, shapeID, shapeKey, shapeBody string, setSticky bool) (string, bool, error) {
	m.shapes[shapeID] = shapeBody
	m.keys[shapeKey] = shapeID
	m.presets[p.Name] = p
	if setSticky {
		m.stickyID = shapeID
	}
	return shapeID, true, nil
}

func (m *mockStorer) RememberShapeOnly(shapeID, shapeKey, shapeBody string) (string, bool, error) {
	m.shapes[shapeID] = shapeBody
	m.keys[shapeKey] = shapeID
	return shapeID, true, nil
}

func (m *mockStorer) Get(name string) (*store.Preset, error) {
	p, ok := m.presets[name]
	if !ok {
		return nil, nil
	}
	return p, nil
}

func (m *mockStorer) Touch(name string) error {
	m.ucalls = append(m.ucalls, "touch:"+name)
	return nil
}

func (m *mockStorer) RecordFork(key, body string) error { return nil }
func (m *mockStorer) RecordPlacement(shapeID, pattern string) error { return nil }
func (m *mockStorer) ListShapes() ([]string, error) {
	var ids []string
	for id := range m.shapes {
		ids = append(ids, id)
	}
	return ids, nil
}

type mockConnector struct {
	tmux.Connector
	has      bool
	loadCall atomic.Int32
}

func (m *mockConnector) Has(ctx context.Context, name string) bool { return m.has }
func (m *mockConnector) Connect(ctx context.Context, name, cwd string) error { return nil }
func (m *mockConnector) Freeze(ctx context.Context, name string) (*model.Session, error) {
	return &model.Session{Name: name, Cwd: "/tmp"}, nil
}
func (m *mockConnector) ConnectPreset(ctx context.Context, s *model.Session) error {
	m.loadCall.Add(1)
	return nil
}

func TestFreezeRemember(t *testing.T) {
	st := newMockStorer()
	ctl := &mockConnector{}
	bus := event.New()
	template.SetEventBus(bus)

	var events int
	bus.On(event.FreezeDone, func(ctx context.Context, args ...any) {
		events++
	})

	sid, created, err := template.FreezeRemember(ctl, st, "my-session")
	if err != nil {
		t.Fatalf("FreezeRemember: %v", err)
	}
	if sid == "" {
		t.Error("FreezeRemember returned empty shapeID")
	}
	if !created {
		t.Error("FreezeRemember should create new shape")
	}
	if events != 1 {
		t.Errorf("events = %d, want 1", events)
	}
	// shape should be sticky after FreezeSave default path (no setSticky)
	if s := st.StickyID(); s != "default" {
		t.Errorf("sticky should remain default, got %q", s)
	}
}

func TestFreezeSave(t *testing.T) {
	st := newMockStorer()
	s := &model.Session{Name: "test", Cwd: "/tmp",
		Windows: []model.Window{
			{Name: "editor", Panes: []model.Pane{{Cmd: "nvim"}}},
		},
	}
	shapeID, created, err := template.FreezeSave(st, s, true)
	if err != nil {
		t.Fatalf("FreezeSave: %v", err)
	}
	if shapeID == "" {
		t.Fatal("FreezeSave returned empty shapeID")
	}
	if !created {
		t.Error("FreezeSave should create new shape")
	}
	if st.StickyID() != shapeID {
		t.Errorf("sticky = %q, want %q", st.StickyID(), shapeID)
	}
}

func TestStickFrom(t *testing.T) {
	st := newMockStorer()
	p := &store.Preset{Name: "test-session", Cwd: "/tmp",
		Windows: []store.PresetWindow{
			{Name: "code", Panes: []store.PresetPane{{Cmd: "nvim"}}},
			{Name: "term", Panes: []store.PresetPane{{}}},
		},
	}
	id, created, err := template.StickFrom(st, p)
	if err != nil {
		t.Fatalf("StickFrom: %v", err)
	}
	if id == "" {
		t.Fatal("StickFrom returned empty id")
	}
	if !created {
		t.Error("StickFrom should create new shape")
	}
	if st.StickyID() != id {
		t.Errorf("sticky id = %q, want %q", st.StickyID(), id)
	}
}

func TestLoadActiveDefault(t *testing.T) {
	st := newMockStorer()
	p, sid, err := template.LoadActive(st)
	if err != nil {
		t.Fatalf("LoadActive: %v", err)
	}
	if p == nil {
		t.Fatal("LoadActive returned nil preset")
	}
	if sid != "default" {
		t.Errorf("sid = %q, want default", sid)
	}
	if p.Name != "default" {
		t.Errorf("preset name = %q, want default", p.Name)
	}
}

func TestLoadActiveSticky(t *testing.T) {
	st := newMockStorer()
	p := &store.Preset{Name: "my-session", Cwd: "/tmp",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{Cmd: "nvim"}}},
		},
	}
	sid, _, err := template.StickFrom(st, p)
	if err != nil {
		t.Fatalf("StickFrom: %v", err)
	}

	loaded, gotSid, err := template.LoadActive(st)
	if err != nil {
		t.Fatalf("LoadActive: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadActive returned nil")
	}
	if gotSid != sid {
		t.Errorf("sid = %q, want %q", gotSid, sid)
	}
}

func TestConnectProjectExisting(t *testing.T) {
	st := newMockStorer()
	ctl := &mockConnector{has: true}
	err := template.ConnectProject(ctl, st, "my-session", "/tmp")
	if err != nil {
		t.Fatalf("ConnectProject: %v", err)
	}
	if ctl.loadCall.Load() != 0 {
		t.Error("ConnectPreset should not be called when session exists")
	}
}

func TestConnectProjectPreset(t *testing.T) {
	st := newMockStorer()
	st.presets["my-session"] = &store.Preset{Name: "my-session", Cwd: "/tmp"}
	ctl := &mockConnector{}
	err := template.ConnectProject(ctl, st, "my-session", "/tmp")
	if err != nil {
		t.Fatalf("ConnectProject: %v", err)
	}
	if ctl.loadCall.Load() != 1 {
		t.Errorf("ConnectPreset called %d times, want 1", ctl.loadCall.Load())
	}
}

func TestConnectProjectBake(t *testing.T) {
	st := newMockStorer()
	ctl := &mockConnector{}
	err := template.ConnectProject(ctl, st, "new-session", "/tmp")
	if err != nil {
		t.Fatalf("ConnectProject: %v", err)
	}
	if ctl.loadCall.Load() != 1 {
		t.Errorf("ConnectPreset called %d times, want 1", ctl.loadCall.Load())
	}
}

func TestFreezeRememberNilCtl(t *testing.T) {
	_, _, err := template.FreezeRemember(nil, newMockStorer(), "x")
	if err == nil {
		t.Error("expected error for nil ctl")
	}
}

func TestConnectProjectNilCtl(t *testing.T) {
	err := template.ConnectProject(nil, newMockStorer(), "x", "/tmp")
	if err == nil {
		t.Error("expected error for nil ctl")
	}
}
