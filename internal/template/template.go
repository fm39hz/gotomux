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
	"github.com/fm39hz/gotomux/internal/toolclass"
)

// Dual source for pure shapes - DB is runtime SSoT; config is 1-1 backup + hand-edit.
//
//	Shape = topology only: window/pane counts (+ optional named split), role labels.
//	No cwd/session/project paths - root at bake; tools are pane intent.
//	Instance (preset session tree) keeps cwd/cmd; separate tables.
//
//	$id = shapes/<id>.json  <->  shape.id
//	Freeze / sticky -> DB tx first, then mirror file (post-commit)
//	Hand-edit JSON  -> picked up once per process if mtime > shape.updated_at
//	Same topology (ShapeKey) -> reuse id
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

func configBaseDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gotomux")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "gotomux")
	}
	return ""
}

// configShapesDir: $XDG_CONFIG_HOME/gotomux/shapes (mirror of shape rows).
// One-time: rename legacy layouts/ -> shapes/ if shapes missing.
func configShapesDir() string {
	base := configBaseDir()
	if base == "" {
		return ""
	}
	dir := filepath.Join(base, "shapes")
	legacy := filepath.Join(base, "layouts")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		if st, err := os.Stat(legacy); err == nil && st.IsDir() {
			_ = os.Rename(legacy, dir)
		}
	}
	return dir
}

// shapeFilePath: human label slug + short id suffix for uniqueness.
// default -> default.json; others -> <label>--<8hex>.json
func shapeFilePath(id, label string) string {
	dir := configShapesDir()
	if dir == "" || id == "" {
		return ""
	}
	if id == "default" {
		return filepath.Join(dir, "default.json")
	}
	lab := LabelFileSlug(label)
	if lab == "" || lab == "shape" {
		lab = "shape"
	}
	// short stable suffix from id (shape-<16hex> -> last 8)
	suf := id
	if strings.HasPrefix(id, "shape-") && len(id) >= 14 {
		suf = id[len(id)-8:]
	} else if len(suf) > 8 {
		suf = suf[len(suf)-8:]
	}
	return filepath.Join(dir, lab+"--"+suf+".json")
}

// writeConfigMirror: product JSON; filename from label, id inside body.
func writeConfigMirror(id, body string) {
	if id == "" || body == "" {
		return
	}
	label := shapeLabelFromBody(id, body)
	path := shapeFilePath(id, label)
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(body), 0o644)
}

func shapeLabelFromBody(id, body string) string {
	if pr, err := Parse(body); err == nil {
		pr = ToShape(pr, id)
		pr.Name = id
		return ShapeLabel(pr)
	}
	if id == "default" {
		return "default"
	}
	return "shape"
}

// reconcileConfigShapes rebuilds shapes/ from DB (SSoT):
// normalize bodies, write label--suffix.json, delete orphan/legacy files.
// Runs on sync and after freeze/stick - silent, no UI.
func reconcileConfigShapes(st *store.Store) {
	if st == nil {
		return
	}
	dir := configShapesDir()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	ids, err := st.ListShapes()
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, id := range ids {
		body, ok := st.GetShape(id)
		if !ok {
			continue
		}
		if clean := normalizeShapeBody(id, body); clean != "" {
			if clean != body {
				pure := mustParseShape(id, clean)
				_ = st.UpsertShapeByID(id, ShapeKey(pure), clean)
				body = clean
			}
		}
		label := shapeLabelFromBody(id, body)
		path := shapeFilePath(id, label)
		if path == "" {
			continue
		}
		_ = os.WriteFile(path, []byte(body), 0o644)
		keep[filepath.Base(path)] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

var syncOnce sync.Once

// syncConfigToDB once per process. Dual-source rules (DB is SSoT for runtime):
//
//	config file for id:
//	  - missing in DB -> insert (new hand-added shape)
//	  - mtime > DB.updated_at -> hand-edit wins, UpsertShapeByID
//	  - mtime <= DB.updated_at -> DB wins, rewrite file from DB (backup catch-up)
//	DB id without file -> write mirror (backup fill)
//
// Import config -> DB once per process. Does not rebuild shapes/ tree.
// Full disk reconcile: mirrorAfter / reconcileConfigShapes after mutations only.
func syncConfigToDB(st *store.Store) {
	if st == nil {
		return
	}
	syncOnce.Do(func() {
		dir := configShapesDir()
		seenFile := map[string]bool{}
		if dir != "" {
			ents, err := os.ReadDir(dir)
			if err == nil {
				for _, e := range ents {
					if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
						continue
					}
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
					// id from body field preferred; fallback filename stem / legacy shape-*
					id := pr.Name
					if !isShapeID(id) {
						stem := strings.TrimSuffix(e.Name(), ".json")
						if isShapeID(stem) {
							id = stem
						} else if i := strings.LastIndex(stem, "--"); i >= 0 {
							suf := stem[i+2:]
							id = "shape-" + suf // may be 8 hex; Upsert by this if exists
							// try resolve full id from DB list by suffix
							if ids, _ := st.ListShapes(); len(ids) > 0 {
								for _, cand := range ids {
									if strings.HasSuffix(cand, suf) || cand == id {
										id = cand
										break
									}
								}
							}
						} else if stem == "default" {
							id = "default"
						} else {
							continue // unknown file naming
						}
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
						// DB newer or equal time but different - SSoT DB -> fix file
						writeConfigMirror(id, dbBody)
					}
				}
			}
		}
		_ = ensureDefault(st)
		// Do NOT full-reconcile on open - rewrite of shapes/ is freeze/stick only.
	})
}


// ToShape: shape essence - topology + pane tool intent.
//
//	keep: pane count, split class (h/v/tiled), tool (nvim/yazi/...)
//	drop: cwd, abs paths, session name, pixel dumps, shell noise
//
// Tools are workflow intent of a pane slot, not project identity.
func ToShape(p *store.Preset, id string) *store.Preset {
	if p == nil {
		return builtinDefault()
	}
	out := &store.Preset{Name: id}
	if out.Name == "" {
		out.Name = "shape"
	}
	sess := p.Name
	base := ""
	if p.Cwd != "" {
		base = filepath.Base(p.Cwd)
	}
	for i, w := range p.Windows {
		n := len(w.Panes)
		if n == 0 {
			n = 1
		}
		pw := store.PresetWindow{
			Idx:    i,
			Layout: tmux.LayoutForShape(w.Layout, n),
		}
		pw.Panes = make([]store.PresetPane, n)
		for j := 0; j < n; j++ {
			pw.Panes[j].Idx = j
			if j < len(w.Panes) {
				pw.Panes[j].Cmd = tmux.ToolIntent(w.Panes[j].Cmd)
			}
		}
		// chrome only - not identity (ShapeKey/fork ignore Name)
		pw.Name = windowChromeRole(w.Name, pw, i, sess, base)
		out.Windows = append(out.Windows, pw)
	}
	if len(out.Windows) == 0 {
		return builtinDefault()
	}
	return out
}

// ShapeKey fingerprints shape essence:
//
//	per-window: pane count + split class + per-pane tool intent
//
// No cwd, labels, pixel dumps.
func ShapeKey(p *store.Preset) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	for i, w := range p.Windows {
		if i > 0 {
			b.WriteByte('|')
		}
		n := len(w.Panes)
		if n == 0 {
			n = 1
		}
		b.WriteByte('#')
		b.WriteString(fmt.Sprintf("%d", i))
		b.WriteByte('x')
		b.WriteString(fmt.Sprintf("%d", n))
		b.WriteByte('@')
		b.WriteString(tmux.LayoutForShape(w.Layout, n))
		for j := 0; j < n; j++ {
			b.WriteByte(',')
			if j < len(w.Panes) {
				b.WriteString(tmux.ToolIntent(w.Panes[j].Cmd))
			}
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// shapeIDFrom: stable opaque id from key only - never from window titles/paths.
// "default" is reserved for builtin; all others shape-<16hex>.
func shapeIDFrom(_ *store.Preset, key string) string {
	if key == "" {
		return "shape-0000000000000000"
	}
	// full 16 hex chars from ShapeKey (8 bytes)
	return "shape-" + key
}



// normalizeShapeBody rewrites legacy/dump bodies into product shape JSON.
func normalizeShapeBody(id, body string) string {
	p, err := Parse(body)
	if err != nil {
		return ""
	}
	pure := ToShape(p, id)
	pure.Name = id
	return Format(pure)
}

func mustParseShape(id, body string) *store.Preset {
	p, err := Parse(body)
	if err != nil {
		return &store.Preset{Name: id}
	}
	return ToShape(p, id)
}

// windowChromeRole: display role only (not ShapeKey/fork identity).
// Prefer tool->role; else neutral role slug; never path/session/project basename.
func windowChromeRole(raw string, w store.PresetWindow, idx int, sess, projBase string) string {
	n := len(w.Panes)
	if n == 0 {
		n = 1
	}
	// 1) tool intent -> portable chrome
	if role := roleFromTools(w); role != "" {
		return role
	}
	// 2) cleaned neutral role from live name
	if role := neutralRoleSlug(raw); role != "" {
		if role == sess || role == projBase {
			return defaultChrome(n)
		}
		return role
	}
	return defaultChrome(n)
}

func roleFromTools(w store.PresetWindow) string {
	var tools []string
	seen := map[string]bool{}
	for _, pn := range w.Panes {
		t := tmux.ToolIntent(pn.Cmd)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tools = append(tools, t)
	}
	if len(tools) == 0 {
		return ""
	}
	if len(tools) > 1 {
		// mixed pane tools - first wins for tab title
		return chromeFromTool(tools[0])
	}
	return chromeFromTool(tools[0])
}

func chromeFromTool(tool string) string {
	return toolclass.ChromeRole(tool)
}

func neutralRoleSlug(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "~/") || strings.Contains(name, "/home/") ||
		strings.Contains(name, "/Users/") || strings.Count(name, "/") >= 1 {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
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
	if out == "" || len(out) > 24 {
		return ""
	}
	switch out {
	case "editor", "shell", "files", "file", "term", "terminal", "main", "aux", "test", "git", "agent":
		if out == "file" {
			return "files"
		}
		if out == "term" || out == "terminal" {
			return "shell"
		}
		if out == "agent" {
			return "agent"
		}
		return out
	default:
		// project-ish or custom - not portable chrome
		return ""
	}
}

func defaultChrome(nPanes int) string {
	if nPanes > 1 {
		return "shell"
	}
	return "shell"
}


// shapeBody prepares pure shape id/key/body for DB.
// default builtin keeps id "default"; everything else is shape-<key>.
func shapeBody(p *store.Preset, forceDefault bool) (id, key, body string) {
	pure := ToShape(p, "tmp")
	key = ShapeKey(pure)
	if forceDefault {
		id = "default"
	} else {
		id = shapeIDFrom(pure, key)
	}
	pure.Name = id
	return id, key, Format(pure)
}

func mirrorAfter(st *store.Store, _ string) {
	reconcileConfigShapes(st)
}

// ensureShapesReady imports hand-edited config once per process (no full reconcile).
// Call before freeze/stick/load-active that must see config files.
func ensureShapesReady(st *store.Store) {
	syncConfigToDB(st)
}

func ReadSticky(st *store.Store) string {
	if st == nil {
		return "default"
	}
	// Hot path: DB only. Config import is deferred to mutation / LoadActive.
	id := st.StickyID()
	if id == "" {
		return "default"
	}
	return id
}

// StickyLabel: human slug for UI - DB read only (no shapes/ rewrite).
func StickyLabel(st *store.Store) string {
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

// LoadActive loads sticky pure shape from DB (SSoT).
func LoadActive(st *store.Store) (*store.Preset, string, error) {
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
		// corrupt shape row - fall back without hiding that we did
		if err2 := ensureDefault(st); err2 != nil {
			return builtinDefault(), "default", fmt.Errorf("parse shape %q: %w (and ensure default: %v)", id, err, err2)
		}
		return builtinDefault(), "default", nil
	}
	return p, id, nil
}

func ensureDefault(st *store.Store) error {
	if st == nil {
		return fmt.Errorf("nil store")
	}
	def := builtinDefault()
	id, key, body := shapeBody(def, true)
	// Upsert by id so "default" stays stable (not shape-<key>)
	if err := st.UpsertShapeByID(id, key, body); err != nil {
		return err
	}
	writeConfigMirror(id, body)
	return nil
}

// observeAfterShape: silent learning after shape write (place + fork units).
func observeAfterShape(st *store.Store, shapeID string, p *store.Preset) {
	ObservePlacement(st, shapeID, p)
	ObserveForks(st, p)
}

// StickFrom: one DB tx (shape + sticky), then config mirror.
func StickFrom(st *store.Store, p *store.Preset) (id string, created bool, err error) {
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
	mirrorAfter(st, outID)
	observeAfterShape(st, outID, p)
	return outID, created, nil
}

// RememberShape: shape only (tests / rare). Prefer FreezeSave for freeze path.
func RememberShape(st *store.Store, p *store.Preset) (id string, created bool, err error) {
	if st == nil || p == nil {
		return "", false, nil
	}
	ensureShapesReady(st)
	id, key, body := shapeBody(p, false)
	outID, created, err := st.RememberShapeOnly(id, key, body)
	if err != nil {
		return "", false, fmt.Errorf("remember shape: %w", err)
	}
	mirrorAfter(st, outID)
	return outID, created, nil
}

// FreezeSave: instance + shape in ONE DB transaction; config mirror after commit.
// Silently learns non-trivial pane placement for this shape (umbrella slots).
func FreezeSave(st *store.Store, p *store.Preset, setSticky bool) (shapeID string, shapeCreated bool, err error) {
	if st == nil || p == nil {
		return "", false, fmt.Errorf("freeze save: nil store or preset")
	}
	ensureShapesReady(st)
	id, key, body := shapeBody(p, false)
	shapeID, shapeCreated, err = st.SaveFreeze(p, id, key, body, setSticky)
	if err != nil {
		return "", false, fmt.Errorf("freeze save: %w", err)
	}
	mirrorAfter(st, shapeID)
	observeAfterShape(st, shapeID, p)
	return shapeID, shapeCreated, nil
}

// FreezeRemember: live session -> instance+shape (setSticky=false always).
// Caller owns SIGINT (HoldInterrupt) around this if needed.
// Does NOT change sticky - that is intentional via StickFrom / ^t.
func FreezeRemember(ctl tmux.Connector, st *store.Store, name string) (shapeID string, shapeCreated bool, err error) {
	if ctl == nil {
		return "", false, fmt.Errorf("freeze: nil tmux")
	}
	if st == nil {
		return "", false, fmt.Errorf("freeze: nil store")
	}
	p, err := ctl.Freeze(name)
	if err != nil {
		return "", false, err
	}
	return FreezeSave(st, p, false)
}

func ResetActive(st *store.Store) error {
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

// Apply bakes pure shape onto project root (all-root; no placement store).
// Prefer bakeShape via ConnectProject for sticky+learned slots.
func Apply(tmpl *store.Preset, name, root string) *store.Preset {
	return bakeShape(nil, tmpl, name, root, "")
}

func ConnectProject(ctl tmux.Connector, st *store.Store, name, cwd string) error {
	if ctl == nil {
		return fmt.Errorf("connect project: nil tmux")
	}
	if name == "" {
		return fmt.Errorf("connect project: empty session name")
	}
	if ctl.Has(name) {
		if err := ctl.Connect(name, ""); err != nil {
			return fmt.Errorf("attach %q: %w", name, err)
		}
		return nil
	}
	if st != nil {
		if p, err := st.Get(name); err == nil {
			_ = st.Touch(name) // best-effort recency
			if err := ctl.ConnectPreset(p); err != nil {
				return fmt.Errorf("load preset %q: %w", name, err)
			}
			return nil
		}
	}
	tmpl, sid, err := LoadActive(st)
	if err != nil {
		return fmt.Errorf("load sticky shape: %w", err)
	}
	// silent: topology x learned placement x current children
	baked := bakeShape(st, tmpl, name, cwd, sid)
	if err := ctl.ConnectPreset(baked); err != nil {
		return fmt.Errorf("bake sticky %q as %q: %w", sid, name, err)
	}
	return nil
}
