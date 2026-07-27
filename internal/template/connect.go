package template

import (
	"context"
	"fmt"

	"github.com/fm39hz/gotomux/internal/event"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

var globalBus *event.Bus

func SetEventBus(b *event.Bus) {
	globalBus = b
	if b != nil {
		b.On(event.FreezeDone, func(ctx context.Context, args ...any) {
			st := args[0].(store.Storer)
			shapeID := args[1].(string)
			p := args[2].(*model.Session)
			mirrorAfter(st, shapeID)
			observeAfterShape(st, shapeID, p)
		})
	}
}

func ReadSticky(st store.Storer) string {
	if st == nil {
		return "default"
	}
	id := st.StickyID()
	if id == "" {
		return "default"
	}
	return id
}

func StickyLabel(st store.Storer) string {
	if st == nil {
		return "default"
	}
	id := st.StickyID()
	if id == "" || id == "default" {
		return "default"
	}
	body, ok := st.GetShape(id)
	if !ok {
		return id
	}
	p, err := Parse(body)
	if err != nil {
		return id
	}
	p = ToShape(p, id)
	return ShapeLabel(p)
}

func LoadActive(st store.Storer) (*model.Session, string, error) {
	if st == nil {
		return builtinDefault(), "default", nil
	}
	ensureShapesReady(st)
	id := st.StickyID()
	if id == "" {
		id = "default"
	}
	body, ok := st.GetShape(id)
	if !ok {
		if err := ensureDefault(st); err != nil {
			return builtinDefault(), "default", fmt.Errorf("ensure default shape: %w", err)
		}
		return builtinDefault(), "default", nil
	}
	p, err := Parse(body)
	if err != nil {
		if err2 := ensureDefault(st); err2 != nil {
			return builtinDefault(), "default", fmt.Errorf("parse shape %q: %w (and ensure default: %v)", id, err, err2)
		}
		return builtinDefault(), "default", nil
	}
	return p, id, nil
}

func ensureDefault(st store.Storer) error {
	if st == nil {
		return fmt.Errorf("nil store")
	}
	def := builtinDefault()
	id, key, body := shapeBody(def, true)
	if err := st.UpsertShapeByID(id, key, body); err != nil {
		return err
	}
	writeConfigMirror(st, id, body)
	return nil
}

func emitShapeEvent(ctx context.Context, st store.Storer, shapeID string, p *model.Session) {
	if globalBus != nil {
		globalBus.Emit(ctx, event.FreezeDone, st, shapeID, p)
	} else {
		mirrorAfter(st, shapeID)
		observeAfterShape(st, shapeID, p)
	}
}

func StickFrom(st store.Storer, p *model.Session) (id string, created bool, err error) {
	if st == nil {
		return "", false, fmt.Errorf("stick: nil store")
	}
	if p == nil {
		return "", false, fmt.Errorf("stick: nil preset")
	}
	ensureShapesReady(st)
	id, key, body := shapeBody(p, false)
	outID, created, err := st.StickShape(id, key, body)
	if err != nil {
		return "", false, fmt.Errorf("stick shape: %w", err)
	}
	emitShapeEvent(context.Background(), st, outID, p)
	return outID, created, nil
}

func RememberShape(st store.Storer, p *model.Session) (id string, created bool, err error) {
	if st == nil || p == nil {
		return "", false, nil
	}
	ensureShapesReady(st)
	id, key, body := shapeBody(p, false)
	outID, created, err := st.RememberShapeOnly(id, key, body)
	if err != nil {
		return "", false, fmt.Errorf("remember shape: %w", err)
	}
	emitShapeEvent(context.Background(), st, outID, p)
	return outID, created, nil
}

func FreezeSave(st store.Storer, s *model.Session, setSticky bool) (shapeID string, shapeCreated bool, err error) {
	if st == nil || s == nil {
		return "", false, fmt.Errorf("freeze save: nil store or preset")
	}
	ensureShapesReady(st)
	id, key, body := shapeBody(s, false)
	shapeID, shapeCreated, err = st.SaveFreeze(s, id, key, body, setSticky)
	if err != nil {
		return "", false, fmt.Errorf("freeze save: %w", err)
	}
	emitShapeEvent(context.Background(), st, shapeID, s)
	return shapeID, shapeCreated, nil
}

func FreezeRemember(ctl tmux.Connector, st store.Storer, name string) (shapeID string, shapeCreated bool, err error) {
	if ctl == nil {
		return "", false, fmt.Errorf("freeze: nil tmux")
	}
	if st == nil {
		return "", false, fmt.Errorf("freeze: nil store")
	}
	p, err := ctl.Freeze(context.Background(), name)
	if err != nil {
		return "", false, err
	}
	return FreezeSave(st, p, false)
}

func ResetActive(st store.Storer) error {
	if st == nil {
		return fmt.Errorf("reset sticky: nil store")
	}
	ensureShapesReady(st)
	if err := ensureDefault(st); err != nil {
		return err
	}
	if err := st.SetSticky("default"); err != nil {
		return fmt.Errorf("set sticky default: %w", err)
	}
	return nil
}

func Apply(tmpl *model.Session, name, root string) *model.Session {
	return bakeShape(nil, tmpl, name, root, "")
}

func ConnectProject(ctl tmux.Connector, st store.Storer, name, cwd string) error {
	if ctl == nil {
		return fmt.Errorf("connect project: nil tmux")
	}
	if name == "" {
		return fmt.Errorf("connect project: empty session name")
	}
	if ctl.Has(context.Background(), name) {
		if err := ctl.Connect(context.Background(), name, ""); err != nil {
			return fmt.Errorf("attach %q: %w", name, err)
		}
		return nil
	}
	if st != nil {
		if p, err := st.Get(name); err == nil {
			_ = st.Touch(name)
			if err := ctl.ConnectPreset(context.Background(), p); err != nil {
				return fmt.Errorf("load preset %q: %w", name, err)
			}
			return nil
		}
	}
	tmpl, sid, err := LoadActive(st)
	if err != nil {
		return fmt.Errorf("load sticky shape: %w", err)
	}
	baked := bakeShape(st, tmpl, name, cwd, sid)
	if err := ctl.ConnectPreset(context.Background(), baked); err != nil {
		return fmt.Errorf("bake sticky %q as %q: %w", sid, name, err)
	}
	return nil
}
