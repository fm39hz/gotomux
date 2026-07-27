package template

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/toolclass"
)

func builtinDefault() *model.Session {
	return &model.Session{
		Name: "default",
		Windows: []model.Window{
			{Name: "editor", Panes: []model.Pane{{}}},
			{Name: "shell", Panes: []model.Pane{{}}},
		},
	}
}

func ToShape(p *model.Session, id string) *model.Session {
	if p == nil {
		return builtinDefault()
	}
	out := &model.Session{Name: id}
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
		pw := model.Window{
			Idx:    i,
			Layout: tmux.LayoutForShape(w.Layout, n),
		}
		pw.Panes = make([]model.Pane, n)
		for j := 0; j < n; j++ {
			pw.Panes[j].Idx = j
			if j < len(w.Panes) {
				pw.Panes[j].Cmd = tmux.ToolIntent(w.Panes[j].Cmd)
			}
		}
		pw.Name = windowChromeRole(w.Name, pw, i, sess, base)
		out.Windows = append(out.Windows, pw)
	}
	if len(out.Windows) == 0 {
		return builtinDefault()
	}
	return out
}

func ShapeKey(p *model.Session) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	for i, w := range p.Windows {
		if i > 0 {
			b.WriteByte('|')
		}
		// Fork key (class-based) replaces exact tool hash
		fk := WindowForkKey(w)
		b.WriteString(fk)
		n := len(w.Panes)
		if n == 0 {
			n = 1
		}
		split := tmux.LayoutForShape(w.Layout, n)
		if split != "" {
			b.WriteByte('@')
			b.WriteString(split)
		}
		_ = i
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func shapeIDFrom(_ *model.Session, key string) string {
	if key == "" {
		return "shape-0000000000000000"
	}
	return "shape-" + key
}

func normalizeShapeBody(id, body string) string {
	p, err := Parse(body)
	if err != nil {
		return ""
	}
	pure := ToShape(p, id)
	pure.Name = id
	return Format(pure)
}

func mustParseShape(id, body string) *model.Session {
	p, err := Parse(body)
	if err != nil {
		return &model.Session{Name: id}
	}
	return ToShape(p, id)
}

func windowChromeRole(raw string, w model.Window, idx int, sess, projBase string) string {
	n := len(w.Panes)
	if n == 0 {
		n = 1
	}
	if role := roleFromTools(w); role != "" {
		return role
	}
	if role := neutralRoleSlug(raw); role != "" {
		if role == sess || role == projBase {
			return defaultChrome(n)
		}
		return role
	}
	return defaultChrome(n)
}

func roleFromTools(w model.Window) string {
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
	return chromeFromTool(tools[0])
}

func chromeFromTool(tool string) string { return toolclass.ChromeRole(tool) }

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
		return ""
	}
}

func defaultChrome(nPanes int) string { return "shell" }

func shapeBody(p *model.Session, forceDefault bool) (id, key, body string) {
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

func observeAfterShape(st store.Storer, shapeID string, p *model.Session) {
	ObservePlacement(st, shapeID, p)
	ObserveForks(st, p)
}
