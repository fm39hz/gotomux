package template

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
)

func configBaseDir(cfg *config.Config) string {
	if cfg != nil {
		return cfg.ResolveConfigDir()
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gotomux")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "gotomux")
	}
	return ""
}

func configShapesDir(cfg *config.Config) string {
	base := configBaseDir(cfg)
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

func shapeFilePath(id, label string) string {
	dir := configShapesDir(nil)
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
	suf := id
	if strings.HasPrefix(id, "shape-") && len(id) >= 14 {
		suf = id[len(id)-8:]
	} else if len(suf) > 8 {
		suf = suf[len(suf)-8:]
	}
	return filepath.Join(dir, lab+"--"+suf+".json")
}

// writeFileAtomic writes data to path via a temp file + rename.
// Prevents partial writes if the process crashes mid-write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeConfigMirror writes one shape's mirror file and records what it wrote.
//
// Recording matters even here: without a signature the next import cannot tell
// this file from a hand edit, so it would be re-imported and then overwritten
// again on every single sync.
func writeConfigMirror(st store.Storer, id, body string) {
	if id == "" || body == "" {
		return
	}
	label := shapeLabelFromBody(id, body)
	path := shapeFilePath(id, label)
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		log.Printf("mirror %s: %v", path, err)
		return
	}
	if st != nil {
		_ = st.SetShapeMirror(id, path, bodySig(body))
	}
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

// bodySig fingerprints a shape body. Used to decide authorship of a mirror file,
// never for identity — ShapeKey is what identifies a shape.
func bodySig(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}

// resolveShapeID recovers the shape id a mirror file belongs to: from the body's
// own id, else the filename stem, else the "--<suffix>" tail matched against known
// shapes, else the literal "default". Empty means "cannot tell".
func resolveShapeID(st store.Storer, fileName string, parsed *model.Session) string {
	if parsed != nil && isShapeID(parsed.Name) {
		return parsed.Name
	}
	stem := strings.TrimSuffix(fileName, ".json")
	if isShapeID(stem) {
		return stem
	}
	if i := strings.LastIndex(stem, "--"); i >= 0 {
		suf := stem[i+2:]
		id := "shape-" + suf
		if ids, _ := st.ListShapes(); len(ids) > 0 {
			for _, cand := range ids {
				if strings.HasSuffix(cand, suf) || cand == id {
					return cand
				}
			}
		}
		return id
	}
	if stem == "default" {
		return "default"
	}
	return ""
}

func reconcileConfigShapes(st store.Storer) {
	if st == nil {
		return
	}
	dir := configShapesDir(nil)
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
				if err := st.UpsertShapeByID(id, ShapeKey(pure), clean); err != nil {
					log.Printf("upsert shape: %v", err)
				}
				body = clean
			}
		}
		label := shapeLabelFromBody(id, body)
		path := shapeFilePath(id, label)
		if path == "" {
			continue
		}
		if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
			log.Printf("mirror %s: %v", path, err)
			continue
		}
		keep[filepath.Base(path)] = true

		// Remember what we wrote, so a later read can tell our own output from a
		// human edit. Also lets us retire the previous filename when the label —
		// and therefore the filename — changes.
		prev := ""
		if meta, ok := st.GetShapeMeta(id); ok {
			prev = meta.MirrorPath
		}
		_ = st.SetShapeMirror(id, path, bodySig(body))
		if prev != "" && prev != path && filepath.Dir(prev) == dir {
			_ = os.Remove(prev)
		}
	}
	sweepStaleMirrors(st, dir, ids, keep)
}

// sweepStaleMirrors removes files that cannot be a shape we still need, and only
// those.
//
// The old sweep deleted every *.json not written in the current pass, which took
// hand-authored files whose shape id the DB had not imported yet — the exact
// content this directory exists to let you write. The discriminator is now what the
// file *is*, not whether we happened to write it this pass:
//
//   - unparseable: junk, remove.
//   - resolves to a shape the DB knows, under a non-canonical name: a stale name for
//     something we already wrote correctly, remove.
//   - resolves to a shape the DB does not know: keep — the next import adopts it.
func sweepStaleMirrors(st store.Storer, dir string, ids []string, keep map[string]bool) {
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if keep[e.Name()] {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pr, err := Parse(string(raw))
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		id := resolveShapeID(st, e.Name(), pr)
		if id != "" && known[id] {
			_ = os.Remove(path)
		}
	}
}

// syncMu serialises directory imports; there is no once-per-process guard.
//
// syncConfigToDB used to run under a sync.Once, which meant the daemon — alive for
// hours — imported the shapes directory exactly once at startup and never noticed
// an edit again, while every subsequent freeze reconciled the directory back to the
// DB. Hand edits were therefore unreachable in the mode most people run.
var syncMu sync.Mutex

func syncConfigToDB(st store.Storer) {
	if st == nil {
		return
	}
	syncMu.Lock()
	defer syncMu.Unlock()
	func() {
		dir := configShapesDir(nil)
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
					pr, err := Parse(string(raw))
					if err != nil {
						continue
					}
					id := resolveShapeID(st, e.Name(), pr)
					if id == "" {
						continue
					}
					pure := ToShape(pr, id)
					pure.Name = id
					key := ShapeKey(pure)
					body := Format(pure)
					seenFile[id] = true

					meta, ok := st.GetShapeMeta(id)
					switch {
					case !ok:
						// No row: the file is the only source. Import it.
						if err := st.UpsertShapeByID(id, key, body); err != nil {
							log.Printf("upsert shape: %v", err)
						}
					case meta.MirrorSig == "":
						// Never mirrored (or written before mirror bookkeeping existed), so
						// we cannot claim authorship of this file. Treat it as the user's.
						if err := st.UpsertShapeByID(id, key, body); err != nil {
							log.Printf("upsert shape: %v", err)
						}
						_ = st.SetShapeMirror(id, path, bodySig(body))
					case bodySig(string(raw)) == meta.MirrorSig:
						// Byte-identical to what we last wrote: untouched. The DB stays
						// authoritative, and reconcileConfigShapes will refresh the file if
						// the row has since changed.
					default:
						// The file differs from what we wrote there, so a human changed it.
						// Import rather than overwrite.
						//
						// This used to be decided by `mtime > dbUpd`. Both sides have
						// one-second resolution and a freeze writes the row and the file
						// inside the same second, so a tie was common — and a tie resolved
						// to "DB wins", silently destroying the edit.
						if err := st.UpsertShapeByID(id, key, body); err != nil {
							log.Printf("upsert shape: %v", err)
						}
						_ = st.SetShapeMirror(id, path, bodySig(body))
					}
				}
			}
		}
		_ = ensureDefault(st)
	}()
}

func mirrorAfter(st store.Storer, _ string) { reconcileConfigShapes(st) }

func ensureShapesReady(st store.Storer) { syncConfigToDB(st) }
