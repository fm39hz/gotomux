package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type kind int

const (
	kindCreate kind = iota
	kindActive
	kindPreset
	kindZoxide
)

type item struct {
	kind    kind
	title   string
	desc    string
	name    string
	path    string
	windows int
}

type action int

const (
	actionNone action = iota
	actionConnect
	actionQuit
)

type result struct {
	action action
	item   item
	err    error
}

type model struct {
	base     []item // active + optional create + presets (no zoxide)
	zox      []item // full zoxide list (score order)
	view     []item
	cursor   int
	query    string
	ctl      *TmuxCtl
	store    *Store
	create   item
	status   string
	done     result
	width    int
	height   int
	maxShow  int
	help     bool   // ? toggles full key help
	tmpl     string // sticky template name (default|…)
	editPath string // temp file while $EDITOR open
	editOld  string // preset name before edit (rename detect)
}

const zoxCap = 40 // unfiltered list shows top-N zoxide only

var (
	stylePrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	styleCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	// weight: Active strongest → Preset → Create → Zoxide dimmest
	styleActive = lipgloss.NewStyle().Foreground(lipgloss.Color("15")) // bright white
	stylePreset = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))  // normal
	styleCreate = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))  // cyan — action
	styleZoxide = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func styleFor(k kind) lipgloss.Style {
	switch k {
	case kindActive:
		return styleActive
	case kindPreset:
		return stylePreset
	case kindCreate:
		return styleCreate
	case kindZoxide:
		return styleZoxide
	default:
		return stylePreset
	}
}

func newModel(ctl *TmuxCtl, store *Store, createName, createCwd string) model {
	// always offer Create at top — enter bakes sticky template immediately
	create := item{
		kind:  kindCreate,
		title: fmt.Sprintf("[Create] %s", createName),
		desc:  createCwd,
		name:  createName,
		path:  createCwd,
	}
	m := model{
		base:    collectBase(ctl, store, create),
		ctl:     ctl,
		store:   store,
		create:  create,
		maxShow: 12,
		tmpl:    readActiveTemplateName(),
	}
	m.refilter()
	return m
}

// collectBase: Create → Active → Presets(last_used). No zoxide.
func collectBase(ctl *TmuxCtl, store *Store, create item) []item {
	seenName := map[string]bool{}
	var items []item

	live, _ := ctl.ListLive()
	liveNames := map[string]bool{}
	for _, s := range live {
		liveNames[s.Name] = true
	}

	// Create first — sticky tmpl + enter without hunting list
	if create.name != "" && !liveNames[create.name] {
		seenName[create.name] = true
		items = append(items, create)
	}

	for _, s := range live {
		seenName[s.Name] = true
		items = append(items, item{
			kind:    kindActive,
			title:   fmt.Sprintf("[Active] %s", s.Name),
			desc:    fmt.Sprintf("%d windows", s.Windows),
			name:    s.Name,
			path:    s.Path,
			windows: s.Windows,
		})
	}

	if meta, err := store.ListMeta(); err == nil {
		for _, m := range meta {
			if seenName[m.Name] {
				continue
			}
			seenName[m.Name] = true
			items = append(items, item{
				kind:  kindPreset,
				title: fmt.Sprintf("[Preset] %s", m.Name),
				desc:  "saved layout",
				name:  m.Name,
				path:  m.Cwd,
			})
		}
	}
	return items
}

func normPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// occupancy: names + paths already shown (active/preset/create).
func occupancy(items []item) (names, paths map[string]bool) {
	names = map[string]bool{}
	paths = map[string]bool{}
	for _, it := range items {
		names[it.name] = true
		if p := normPath(it.path); p != "" {
			paths[p] = true
		}
	}
	return names, paths
}

// zoxideItems: skip if session name OR path already covered.
func zoxideItems(zpaths []string, names, paths map[string]bool) []item {
	var out []item
	for _, p := range zpaths {
		np := normPath(p)
		base := sessionName(p)
		if base == "" {
			continue
		}
		if names[base] || (np != "" && paths[np]) {
			continue
		}
		names[base] = true
		if np != "" {
			paths[np] = true
		}
		out = append(out, item{
			kind:  kindZoxide,
			title: fmt.Sprintf("[Zoxide] %s", base),
			desc:  p,
			name:  base,
			path:  p,
		})
	}
	return out
}

type zoxideMsg []string

func loadZoxideCmd() tea.Msg {
	return zoxideMsg(zoxideList())
}

func (m *model) mergeZoxide(paths []string) {
	names, pths := occupancy(m.base)
	m.zox = zoxideItems(paths, names, pths)
}

func (m *model) pool() []item {
	q := strings.TrimSpace(m.query)
	var out []item
	for _, it := range m.base {
		// Create only when query empty — sticky-tmpl enter without filter noise
		if it.kind == kindCreate && q != "" {
			continue
		}
		out = append(out, it)
	}
	if len(m.zox) == 0 {
		return out
	}
	if q == "" {
		n := zoxCap
		if n > len(m.zox) {
			n = len(m.zox)
		}
		return append(out, m.zox[:n]...)
	}
	return append(out, m.zox...)
}

func (m *model) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.query))
	pool := m.pool()
	type scored struct {
		it    item
		score int
		idx   int
	}
	hits := make([]scored, 0, len(pool))
	for i, it := range pool {
		s := scoreItem(q, it)
		if s < 0 {
			continue
		}
		hits = append(hits, scored{it, s, i})
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].idx < hits[b].idx
	})
	m.view = m.view[:0]
	for _, h := range hits {
		m.view = append(m.view, h.it)
	}
	if m.cursor >= len(m.view) {
		m.cursor = len(m.view) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// refilterFromQuery: user edited filter → jump to best match.
func (m *model) refilterFromQuery() {
	m.refilter()
	m.cursor = 0
}

func (m *model) totalCount() int {
	return len(m.base) + len(m.zox)
}

// scoreItem ranks one list entry. -1 = no match (only when q non-empty).
//
//	empty  → kind weight only
//	typed  → name/basename quality + kind; full path only weak (avoids .config/* flood)
func scoreItem(q string, it item) int {
	if q == "" {
		return kindScore(it.kind)
	}
	best := -1
	// Primary: session name (hyphen segments count)
	if s := scoreName(q, strings.ToLower(it.name)); s > best {
		best = s
	}
	// Basename of path (zoxide folder name)
	base := strings.ToLower(filepath.Base(it.path))
	if base != "" && base != strings.ToLower(it.name) {
		if s := scoreName(q, base); s > best {
			best = s
		}
	}
	// Full path: weak only — enough to keep a hit, not to outrank names
	if p := strings.ToLower(it.path); p != "" {
		if s := scorePathWeak(q, p); s > best {
			best = s
		}
	}
	if best < 0 {
		return -1
	}
	return best + kindScore(it.kind)
}

// kindScore: idle ranking + typed tie-break. Below one match tier step (~10k).
func kindScore(k kind) int {
	switch k {
	case kindCreate:
		return 8_000
	case kindActive:
		return 6_000
	case kindPreset:
		return 4_000
	default:
		return 0
	}
}

// scoreName: match against a label (session name or folder basename).
// Takes max(whole label, hyphen/underscore segments) so "confi" scores the
// "config" segment inside "dotfiles-config", not a weak mid-string hit on the whole string.
func scoreName(q, name string) int {
	if name == "" {
		return -1
	}
	best := -1
	if s := scoreMatch(q, name); s >= 0 {
		best = s + densityBonus(q, name)
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	}) {
		if seg == "" || seg == name {
			continue
		}
		s := scoreMatch(q, seg)
		if s < 0 {
			continue
		}
		// segment hit: full tier on the segment + density on segment length
		total := s + densityBonus(q, seg)
		if total > best {
			best = total
		}
	}
	return best
}

// scorePathWeak: path hit only if a path segment matches; capped low.
func scorePathWeak(q, path string) int {
	best := -1
	for _, seg := range strings.Split(path, string(os.PathSeparator)) {
		if seg == "" {
			continue
		}
		s := scoreMatch(q, strings.ToLower(seg))
		if s < 0 {
			continue
		}
		// cap so path never beats a real name prefix/exact
		if s > 25_000 {
			s = 25_000
		}
		s += densityBonus(q, seg) / 2
		if s > best {
			best = s
		}
	}
	if best < 0 {
		return -1
	}
	return best
}

func densityBonus(q, target string) int {
	if len(target) == 0 {
		return 0
	}
	// 0..5000 — "confi"/"config" ≈ 4166, "confi"/"dotfiles-config" whole is lower path
	return 5000 * len(q) / len(target)
}

// scoreMatch: higher = better. -1 = no match.
// exact > segment-boundary prefix > prefix > substring > fuzzy.
func scoreMatch(query, text string) int {
	if query == "" {
		return 0
	}
	if text == query {
		return 100_000
	}
	if strings.HasPrefix(text, query) {
		rest := text[len(query):]
		if rest == "" {
			return 100_000
		}
		// longer completion after a clean prefix is fine; prefer denser via densityBonus
		return 80_000 - (len(text) - len(query))
	}
	if i := strings.Index(text, query); i >= 0 {
		// mid-string substring (not a leading prefix) — weaker
		return 40_000 - i*20 - len(text)
	}
	return scoreFuzzy(query, text)
}

// scoreFuzzy: rune subsequence with consecutive-run bonus. -1 if no match.
func scoreFuzzy(query, text string) int {
	qr, tr := []rune(query), []rune(text)
	if len(qr) == 0 {
		return 0
	}
	if len(qr) > len(tr) {
		return -1
	}
	ti := 0
	score := 0
	prev := -2 // last match index
	first := -1
	for _, q := range qr {
		found := false
		for ; ti < len(tr); ti++ {
			if tr[ti] != q {
				continue
			}
			if first < 0 {
				first = ti
			}
			// consecutive run
			if ti == prev+1 {
				score += 50
			} else {
				score += 10
				// gap penalty
				if prev >= 0 {
					gap := ti - prev - 1
					if gap > 0 {
						score -= gap
					}
				}
			}
			// word-boundary / start bonus
			if ti == 0 || tr[ti-1] == '/' || tr[ti-1] == '-' || tr[ti-1] == '_' || tr[ti-1] == ' ' {
				score += 20
			}
			prev = ti
			ti++
			found = true
			break
		}
		if !found {
			return -1
		}
	}
	// earlier first match, shorter text → better
	score += 1000 - first*2
	score -= len(tr)
	if score < 0 {
		score = 0
	}
	return score
}

// fuzzyMatch kept for pick.go / tests — true if any match.
func fuzzyMatch(query, text string) bool {
	if query == "" {
		return true
	}
	return scoreMatch(query, text) >= 0
}

// truncateRunes cuts s to at most n runes, adding "…" when clipped.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func (m model) Init() tea.Cmd { return loadZoxideCmd }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case zoxideMsg:
		m.mergeZoxide(msg)
		m.refilter()
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// inline mode: keep list short like fzf --height
		if m.maxShow <= 0 {
			m.maxShow = 12
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.done = result{action: actionQuit}
			return m, tea.Quit

		case "?":
			m.help = !m.help
			return m, nil

		case "ctrl+t": // sticky template from preset; else reset default
			if len(m.view) > 0 {
				it := m.view[m.cursor]
				if it.kind == kindPreset {
					p, err := m.store.Get(it.name)
					if err != nil {
						m.status = err.Error()
						return m, nil
					}
					name, err := setActiveFromPreset(p)
					if err != nil {
						m.status = err.Error()
						return m, nil
					}
					m.tmpl = name
					m.status = "tmpl: " + name + "  (create/zoxide use this)"
					return m, nil
				}
			}
			if err := resetActiveTemplate(); err != nil {
				m.status = err.Error()
			} else {
				m.tmpl = "default"
				m.status = "tmpl: default"
			}
			return m, nil

		case "enter":
			if len(m.view) > 0 && m.cursor < len(m.view) {
				m.done = result{action: actionConnect, item: m.view[m.cursor]}
				return m, tea.Quit
			}

		case "ctrl+n", "down":
			if len(m.view) > 0 {
				m.cursor = (m.cursor + 1) % len(m.view)
			}
			return m, nil

		case "ctrl+p", "up":
			if len(m.view) > 0 {
				m.cursor--
				if m.cursor < 0 {
					m.cursor = len(m.view) - 1
				}
			}
			return m, nil

		case "ctrl+u":
			m.query = ""
			m.refilterFromQuery()
			return m, nil

		case "ctrl+w":
			m.query = trimLastWord(m.query)
			m.refilterFromQuery()
			return m, nil

		case "backspace":
			if len(m.query) > 0 {
				// drop last rune
				r := []rune(m.query)
				m.query = string(r[:len(r)-1])
				m.refilterFromQuery()
			}
			return m, nil

		case "ctrl+x": // kill active
			if len(m.view) > 0 {
				it := m.view[m.cursor]
				if it.kind == kindActive {
					if err := m.ctl.Kill(it.name); err != nil {
						m.status = err.Error()
					} else {
						m.status = "killed " + it.name
						m.reload()
					}
				}
			}
			return m, nil

		case "ctrl+f": // freeze active
			if len(m.view) > 0 {
				it := m.view[m.cursor]
				if it.kind == kindActive {
					p, err := m.ctl.Freeze(it.name)
					if err != nil {
						m.status = err.Error()
					} else if err := m.store.Save(p); err != nil {
						m.status = err.Error()
					} else {
						m.status = "froze " + it.name
						m.reload()
					}
				}
			}
			return m, nil

		case "ctrl+e": // edit preset in $EDITOR
			if len(m.view) > 0 {
				it := m.view[m.cursor]
				if it.kind == kindPreset {
					cmd, err := m.beginEdit(it.name)
					if err != nil {
						m.status = err.Error()
						return m, nil
					}
					// wipe inline frame before ReleaseTerminal — else editor return duplicates UI
					clearInline(m.frameLines())
					return m, cmd
				}
			}
			return m, nil

		case "ctrl+d": // delete preset
			if len(m.view) > 0 {
				it := m.view[m.cursor]
				if it.kind == kindPreset {
					if err := m.store.Delete(it.name); err != nil {
						m.status = err.Error()
					} else {
						m.status = "deleted " + it.name
						m.reload()
					}
				}
			}
			return m, nil

		default:
			// unmapped ctrl/alt chords: ignore (don't leak into query)
			if isModifierChord(msg) {
				return m, nil
			}
			// plain printable → filter
			if msg.Type == tea.KeyRunes {
				for _, r := range msg.Runes {
					if unicode.IsPrint(r) {
						m.query += string(r)
					}
				}
				m.refilterFromQuery()
			}
		}

	case editDoneMsg:
		// editor left junk below; wipe so repaint is single frame
		clearInline(m.frameLines())
		if msg.err != nil {
			m.status = msg.err.Error()
		} else {
			m.status = "saved " + msg.name
			m.reload()
		}
		return m, nil
	}
	return m, nil
}

func (m *model) reload() {
	m.base = collectBase(m.ctl, m.store, m.create)
	names, pths := occupancy(m.base)
	m.zox = zoxideItems(zoxideList(), names, pths)
	m.refilter()
}

// frameLines is fixed height of View — used to wipe residual UI after quit.
func (m model) frameLines() int {
	maxShow := m.maxShow
	if maxShow <= 0 {
		maxShow = 12
	}
	// prompt + header + list + status
	return maxShow + 3
}

type editDoneMsg struct {
	err  error
	name string
}

func (m *model) beginEdit(name string) (tea.Cmd, error) {
	p, err := m.store.Get(name)
	if err != nil {
		return nil, err
	}
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "edit-*.json")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if _, err := f.WriteString(formatPreset(p)); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	f.Close()
	m.editPath = path
	m.editOld = name

	ed := os.Getenv("EDITOR")
	if ed == "" {
		ed = os.Getenv("VISUAL")
	}
	if ed == "" {
		ed = "nvim"
	}
	var c *exec.Cmd
	if fields := strings.Fields(ed); len(fields) > 1 {
		c = exec.Command(fields[0], append(fields[1:], path)...)
	} else {
		c = exec.Command(ed, path)
	}
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	store := m.store
	old := name
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return editDoneMsg{err: fmt.Errorf("editor: %w", err)}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return editDoneMsg{err: err}
		}
		np, err := parsePreset(string(raw))
		if err != nil {
			return editDoneMsg{err: fmt.Errorf("parse: %w", err)}
		}
		if err := store.Save(np); err != nil {
			return editDoneMsg{err: err}
		}
		if np.Name != old {
			_ = store.Delete(old)
		}
		return editDoneMsg{name: np.Name}
	}), nil
}

func trimLastWord(s string) string {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	i := strings.LastIndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return ""
	}
	return s[:i+1]
}

func (m model) View() string {
	var b strings.Builder

	// prompt line — fzf style
	b.WriteString(stylePrompt.Render("❯ "))
	b.WriteString(m.query)
	b.WriteString(styleDim.Render("█"))
	b.WriteByte('\n')
	if m.help {
		b.WriteString(styleHeader.Render(fmt.Sprintf("  %d/%d  ^n/p · enter · ^t tmpl · ^x kill · ^f freeze · ^e edit · ^d del · esc · ?", len(m.view), m.totalCount())))
	} else {
		head := fmt.Sprintf("  %d/%d  enter · esc · ?", len(m.view), m.totalCount())
		if m.tmpl != "" && m.tmpl != "default" {
			head = fmt.Sprintf("  %d/%d  tmpl:%s  enter · esc · ?", len(m.view), m.totalCount(), m.tmpl)
		}
		b.WriteString(styleHeader.Render(head))
	}
	b.WriteByte('\n')

	maxShow := m.maxShow
	if maxShow <= 0 {
		maxShow = 12
	}

	shown := 0
	if len(m.view) == 0 {
		b.WriteString(styleDim.Render("  (no match)"))
		b.WriteByte('\n')
		shown = 1
	} else {
		start := 0
		if m.cursor >= maxShow {
			start = m.cursor - maxShow + 1
		}
		end := start + maxShow
		if end > len(m.view) {
			end = len(m.view)
		}
		for i := start; i < end; i++ {
			it := m.view[i]
			line := it.title
			if it.desc != "" {
				line = line + "  " + it.desc
			}
			if m.width > 4 {
				line = truncateRunes(line, m.width-2)
			}
			if i == m.cursor {
				b.WriteString(styleCursor.Render("▸ " + line))
			} else {
				b.WriteString(styleFor(it.kind).Render("  " + line))
			}
			b.WriteByte('\n')
			shown++
		}
	}
	// pad to fixed height so filter shrink doesn't leave ghost lines
	for shown < maxShow {
		b.WriteByte('\n')
		shown++
	}

	// status always occupies 1 line — fixed frame height for clearInline
	if m.status != "" {
		b.WriteString(styleStatus.Render(m.status))
	}
	b.WriteByte('\n')
	return b.String()
}

// isModifierChord: ctrl/alt/meta combo that is not plain text.
// Prevents ctrl+l etc. from inserting "l" into the filter.
func isModifierChord(msg tea.KeyMsg) bool {
	if msg.Alt {
		return true
	}
	s := msg.String()
	if strings.HasPrefix(s, "ctrl+") || strings.HasPrefix(s, "alt+") ||
		strings.HasPrefix(s, "shift+ctrl+") || strings.HasPrefix(s, "ctrl+alt+") {
		return true
	}
	if strings.Contains(s, "+") && msg.Type != tea.KeyRunes {
		return true
	}
	return false
}

// clearInline erases n lines of residual bubbletea inline UI (fzf-style).
// Bubble Tea stop() only clears the current line — the rest stays in scrollback.
func clearInline(n int) {
	if n <= 0 {
		return
	}
	var b strings.Builder
	// cursor is at start of last rendered line after stop(); go up n-1 then erase n
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString("\x1b[1A") // up
		}
		b.WriteString("\x1b[2K") // erase line
	}
	b.WriteByte('\r')
	fmt.Fprint(os.Stdout, b.String())
}
