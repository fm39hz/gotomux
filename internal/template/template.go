package template

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Dual source for pure shapes — DB is runtime SSoT; config is 1-1 backup + hand-edit.
//
//	$id = layouts/<id>.json  ↔  shape.id
//	Freeze / sticky / edit-in-app  →  DB tx first, then mirror file (post-commit)
//	Hand-edit JSON                →  picked up once per process if mtime > shape.updated_at
//	Same topology (ShapeKey)      →  reuse id (no clone)
//	Preset instance (session tree) is separate table; only pure shape is dual-sourced.
//

func builtinDefault() *store.Preset {
	return &store.Preset{
		Name: "default",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{}}},
			{Name: "shell", Panes: []store.PresetPane{{}}},
		},
	}
}

func configLayoutsDir() string {
	var base string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		base = filepath.Join(xdg, "gotomux")
	} else if home, err := os.UserHomeDir(); err == nil {
		base = filepath.Join(home, ".config", "gotomux")
	} else {
		return ""
	}
	return filepath.Join(base, "layouts")
}

func shapeFilePath(id string) string {
	dir := configLayoutsDir()
	if dir == "" || id == "" {
		return ""
	}
	return filepath.Join(dir, id+".json")
}

// writeConfigMirror: best-effort 1-1 file for id (no error to caller on fail).
func writeConfigMirror(id, body string) {
	path := shapeFilePath(id)
	if path == "" || body == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(body), 0o644)
}

var syncOnce sync.Once

// syncConfigToDB once per process. Dual-source rules (DB is SSoT for runtime):
//
//	config file for id:
//	  - missing in DB → insert (new hand-added layout)
//	  - mtime > DB.updated_at → hand-edit wins, UpsertShapeByID
//	  - mtime <= DB.updated_at → DB wins, rewrite file from DB (backup catch-up)
//	DB id without file → write mirror (backup fill)
//
// Freeze/sticky never read config on hot path after this once.
func syncConfigToDB(st *store.Store) {
	if st == nil {
		return
	}
	syncOnce.Do(func() {
		dir := configLayoutsDir()
		seenFile := map[string]bool{}
		if dir != "" {
			ents, err := os.ReadDir(dir)
			if err == nil {
				for _, e := range ents {
					if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
						continue
					}
					id := strings.TrimSuffix(e.Name(), ".json")
					path := filepath.Join(dir, e.Name())
					raw, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					fi, err := os.Stat(path)
					if err != nil {
						continue
					}
					mtime := fi.ModTime().Unix()
					pr, err := Parse(string(raw))
					if err != nil {
						continue // corrupt hand-edit: leave DB alone
					}
					pure := ToShape(pr, id)
					pure.Name = id
					key := ShapeKey(pure)
					body := Format(pure)
					seenFile[id] = true

					dbBody, dbUpd, ok := st.GetShapeMeta(id)
					if !ok {
						// new id from config
						_ = st.UpsertShapeByID(id, key, body)
						continue
					}
					if mtime > dbUpd {
						// hand-edit newer than last freeze/export
						_ = st.UpsertShapeByID(id, key, body)
					} else if body != dbBody {
						// DB newer or equal time but different — SSoT DB → fix file
						writeConfigMirror(id, dbBody)
					}
				}
			}
		}
		ensureDefault(st)
		// DB → missing files
		ids, _ := st.ListShapes()
		for _, id := range ids {
			if seenFile[id] {
				continue
			}
			if body, ok := st.GetShape(id); ok {
				writeConfigMirror(id, body)
			}
		}
	})
}

func ensureDefault(st *store.Store) {
	def := builtinDefault()
	key := ShapeKey(def)
	body := Format(def)
	_ = st.UpsertShapeByID("default", key, body)
	writeConfigMirror("default", body)
}

func relativizeCwd(root, cwd string) string {
	if cwd == "" || root == "" {
		return ""
	}
	if !filepath.IsAbs(cwd) {
		return filepath.Clean(cwd)
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ""
	}
	if rel == "." {
		return ""
	}
	return rel
}

// ToShape: pure layout — relative cwd, no cmd.
func ToShape(p *store.Preset, id string) *store.Preset {
	if p == nil {
		return builtinDefault()
	}
	root := p.Cwd
	out := &store.Preset{Name: id}
	if out.Name == "" {
		out.Name = "shape"
	}
	for i, w := range p.Windows {
		wname := w.Name
		if wname == "" {
			wname = fmt.Sprintf("w%d", i)
		}
		pw := store.PresetWindow{
			Idx:    i,
			Name:   wname,
			Cwd:    relativizeCwd(root, w.Cwd),
			Layout: tmux.LayoutForStore(w.Layout, len(w.Panes)),
		}
		if len(w.Panes) == 0 {
			pw.Panes = []store.PresetPane{{}}
		} else {
			for j, pn := range w.Panes {
				pw.Panes = append(pw.Panes, store.PresetPane{
					Idx: j,
					Cwd: relativizeCwd(root, pn.Cwd),
				})
			}
		}
		out.Windows = append(out.Windows, pw)
	}
	if len(out.Windows) == 0 {
		return builtinDefault()
	}
	return out
}

func ShapeKey(p *store.Preset) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	for i, w := range p.Windows {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(w.Name)
		b.WriteByte('@')
		b.WriteString(w.Layout)
		b.WriteByte(':')
		b.WriteString(w.Cwd)
		for _, pn := range w.Panes {
			b.WriteByte(',')
			b.WriteString(pn.Cwd)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func shapeIDFrom(p *store.Preset, key string) string {
	var parts []string
	for _, w := range p.Windows {
		n := sanitizeID(w.Name)
		if n != "" {
			parts = append(parts, n)
		}
	}
	if len(parts) > 0 {
		base := strings.Join(parts, "-")
		if len(base) > 40 {
			base = base[:40]
		}
		return base
	}
	if len(key) >= 8 {
		return "shape-" + key[:8]
	}
	return "shape"
}

func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}

// putShapeBoth: DB + config file 1-1 by id.
// Dedupe: if key already in DB under any id, reuse that id (no second file).
func putShapeBoth(st *store.Store, id string, pure *store.Preset) (outID string, created bool, err error) {
	if pure == nil {
		return "", false, fmt.Errorf("nil shape")
	}
	key := ShapeKey(pure)
	pure.Name = id
	body := Format(pure)
	// existing topology?
	if existID, _, ok := st.GetShapeByKey(key); ok {
		// keep existing id; refresh mirror file
		if body2, ok := st.GetShape(existID); ok {
			writeConfigMirror(existID, body2)
		}
		return existID, false, nil
	}
	if id == "" {
		id = shapeIDFrom(pure, key)
		pure.Name = id
		body = Format(pure)
	}
	outID, created, err = st.PutShape(id, key, body)
	if err != nil {
		return "", false, err
	}
	// if PutShape chose different id, re-format name
	if outID != id {
		pure.Name = outID
		body = Format(pure)
		_ = st.UpsertShapeByID(outID, key, body)
	}
	writeConfigMirror(outID, body)
	return outID, created, nil
}

func ReadSticky(st *store.Store) string {
	if st == nil {
		return "default"
	}
	syncConfigToDB(st)
	id := st.StickyID()
	if id == "" {
		return "default"
	}
	return id
}

func ReadActiveName() string { return "default" }

func LoadActive(st *store.Store) (*store.Preset, string, error) {
	if st == nil {
		return builtinDefault(), "default", nil
	}
	syncConfigToDB(st)
	id := st.StickyID()
	if id == "" {
		id = "default"
	}
	body, ok := st.GetShape(id)
	if !ok {
		ensureDefaultBoth(st)
		return builtinDefault(), "default", nil
	}
	p, err := Parse(body)
	if err != nil {
		return builtinDefault(), "default", nil
	}
	return p, id, nil
}

func ensureDefaultBoth(st *store.Store) {
	def := builtinDefault()
	_, _, _ = putShapeBoth(st, "default", def)
	_ = st.SetSticky("default")
}

// StickFrom: sticky from selection. DB: one tx (shape + sticky). Config mirror after commit.
func StickFrom(st *store.Store, p *store.Preset) (id string, created bool, err error) {
	if st == nil {
		return "", false, fmt.Errorf("no store")
	}
	if p == nil {
		return "", false, fmt.Errorf("nothing to stick")
	}
	syncConfigToDB(st)
	pure := ToShape(p, "tmp")
	key := ShapeKey(pure)
	id = shapeIDFrom(p, key)
	pure.Name = id
	body := Format(pure)
	outID, created, err := st.StickShape(id, key, body)
	if err != nil {
		return "", false, err
	}
	// post-commit mirror (best-effort; DB is source of truth if disk fails)
	if b, ok := st.GetShape(outID); ok {
		writeConfigMirror(outID, b)
	} else {
		writeConfigMirror(outID, body)
	}
	return outID, created, nil
}

// RememberShape: pure shape only (when instance already saved). Prefer FreezeSave.
func RememberShape(st *store.Store, p *store.Preset) (id string, created bool, err error) {
	if st == nil || p == nil {
		return "", false, nil
	}
	syncConfigToDB(st)
	pure := ToShape(p, "tmp")
	key := ShapeKey(pure)
	id = shapeIDFrom(p, key)
	pure.Name = id
	body := Format(pure)
	outID, created, err := st.RememberShapeOnly(id, key, body)
	if err != nil {
		return "", false, err
	}
	if b, ok := st.GetShape(outID); ok {
		writeConfigMirror(outID, b)
	}
	return outID, created, nil
}

// FreezeSave: instance + shape in ONE DB transaction; then config mirror.
// setSticky: also point sticky at shape (ctrl-t path can use StickFrom instead).
func FreezeSave(st *store.Store, p *store.Preset, setSticky bool) (shapeID string, shapeCreated bool, err error) {
	if st == nil || p == nil {
		return "", false, fmt.Errorf("freeze save: nil")
	}
	syncConfigToDB(st)
	pure := ToShape(p, "tmp")
	key := ShapeKey(pure)
	id := shapeIDFrom(p, key)
	pure.Name = id
	body := Format(pure)
	shapeID, shapeCreated, err = st.SaveFreeze(p, id, key, body, setSticky)
	if err != nil {
		return "", false, err
	}
	if b, ok := st.GetShape(shapeID); ok {
		writeConfigMirror(shapeID, b)
	}
	return shapeID, shapeCreated, nil
}

func SetActiveFromPreset(st *store.Store, p *store.Preset) (string, error) {
	id, _, err := StickFrom(st, p)
	return id, err
}

func ResetActive(st *store.Store) error {
	if st == nil {
		return nil
	}
	syncConfigToDB(st)
	ensureDefaultBoth(st)
	return st.SetSticky("default")
}

func Apply(tmpl *store.Preset, name, root string) *store.Preset {
	if root == "" {
		root, _ = os.Getwd()
	}
	p := &store.Preset{Name: name, Cwd: root}
	if tmpl == nil || len(tmpl.Windows) == 0 {
		tmpl = builtinDefault()
	}
	for i, w := range tmpl.Windows {
		wcwd := resolveCwd(root, w.Cwd)
		pw := store.PresetWindow{Idx: i, Name: w.Name, Cwd: wcwd, Layout: w.Layout}
		if len(w.Panes) == 0 {
			pw.Panes = []store.PresetPane{{Cwd: wcwd}}
		} else {
			for j, pn := range w.Panes {
				cwd := pn.Cwd
				if cwd == "" {
					cwd = w.Cwd
				}
				pw.Panes = append(pw.Panes, store.PresetPane{Idx: j, Cwd: resolveCwd(root, cwd)})
			}
		}
		p.Windows = append(p.Windows, pw)
	}
	return p
}

func resolveCwd(root, cwd string) string {
	if cwd == "" {
		return root
	}
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Join(root, cwd)
}

func ConnectProject(ctl *tmux.Ctl, st *store.Store, name, cwd string) error {
	if ctl.Has(name) {
		return ctl.Connect(name, "")
	}
	if st != nil {
		if p, err := st.Get(name); err == nil {
			_ = st.Touch(name)
			return ctl.ConnectPreset(p)
		}
	}
	tmpl, _, err := LoadActive(st)
	if err != nil {
		return err
	}
	return ctl.ConnectPreset(Apply(tmpl, name, cwd))
}

func presetToTemplate(p *store.Preset) *store.Preset { return ToShape(p, p.Name) }
func builtinDefaultTemplate() *store.Preset            { return builtinDefault() }
