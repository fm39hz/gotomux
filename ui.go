package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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
	zox      []item // zoxide items (seeded from disk cache)
	zoxStale bool   // need background zoxide refresh
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
	help     bool      // ? toggles full key help
	tmpl     string    // sticky template name (default|…)
	started  time.Time // swallow Alt-release ESC right after open (display-popup)
	ctx      string    // current tmux session (co-occurrence context)
	pairs    map[string]int64
	editPath string // temp file while $EDITOR open
	editOld  string // preset name before edit (rename detect)
}

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
	base := collectBase(ctl, store, create)
	now := time.Now().Unix()
	if us, err := store.AllUsage(); err == nil {
		applyUsage(base, us, now)
	}
	ctx := ""
	if ctl != nil {
		ctx = ctl.CurrentSession()
	}
	var pairs map[string]int64
	if store != nil && ctx != "" {
		pairs, _ = store.PairScores(ctx, now)
		applyCooccur(base, pairs)
	}
	// Seed zoxide from prebuilt item cache — same frame as Create/Active (no walk).
	var zox []item
	zoxItems, zoxAge, zoxOK := loadZoxItemsSync()
	if zoxOK {
		zox = dedupeZoxAgainst(zoxItems, base)
		if store != nil {
			if us, err := store.AllUsage(); err == nil {
				applyUsage(zox, us, now)
			}
			applyCooccur(zox, pairs)
		}
	}
	m := model{
		base:     base,
		zox:      zox,
		ctl:      ctl,
		store:    store,
		create:   create,
		maxShow:  12,
		tmpl:     readActiveTemplateName(),
		started:  time.Now(),
		ctx:      ctx,
		pairs:    pairs,
		zoxStale: zoxItemsStale(zoxAge, zoxOK),
	}
	m.refilter()
	return m
}

// zoxideMsg: background refresh finished (pre-built items).
type zoxideMsg struct {
	items []item
}

func loadZoxideFreshCmd() tea.Msg {
	return zoxideMsg{items: rebuildZoxItems()}
}

func dedupeZoxAgainst(items, base []item) []item {
	names, pths := occupancy(base)
	out := make([]item, 0, len(items))
	for _, it := range items {
		nr := normPath(it.path)
		if names[it.name] || (nr != "" && pths[nr]) {
			continue
		}
		names[it.name] = true
		if nr != "" {
			pths[nr] = true
		}
		out = append(out, it)
	}
	return out
}

func (m *model) mergeZoxide(items []item) {
	m.zox = dedupeZoxAgainst(items, m.base)
	if m.store != nil {
		now := time.Now().Unix()
		if us, err := m.store.AllUsage(); err == nil {
			applyUsage(m.zox, us, now)
		}
		applyCooccur(m.zox, m.pairs)
	}
	m.zoxStale = false
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
	m.view = rankItems(q, m.pool())
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

func (m model) Init() tea.Cmd {
	// always refresh full zoxide in background — cache only seeds paint
	return loadZoxideFreshCmd
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case zoxideMsg:
		if len(msg.items) == 0 {
			m.zoxStale = false
			return m, nil
		}
		m.mergeZoxide(msg.items)
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
		case "ctrl+c":
			m.done = result{action: actionQuit}
			return m, tea.Quit

		case "esc":
			// display-popup + bind -n M-* : releasing Alt often injects ESC into the
			// new pane and would quit instantly. Ignore brief false ESC after open.
			if time.Since(m.started) < 500*time.Millisecond {
				return m, nil
			}
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
						if m.store != nil {
							_ = m.store.RecordKill(it.name)
						}
						m.status = "killed " + it.name
						m.reload()
					}
				}
			}
			return m, nil

		case "ctrl+f": // freeze live session → overwrite preset
			if len(m.view) > 0 {
				it := m.view[m.cursor]
				name := it.name
				// Active always live; Preset freezes only if session currently exists
				if it.kind == kindActive || (it.kind == kindPreset && m.ctl.Has(name)) {
					p, err := m.ctl.Freeze(name)
					if err != nil {
						m.status = err.Error()
					} else if err := m.store.Save(p); err != nil {
						m.status = err.Error()
					} else {
						m.status = "froze " + name
						m.reload()
					}
				} else if it.kind == kindPreset {
					m.status = "session not running — attach first"
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
	if items, age, ok := loadZoxItemsSync(); ok {
		m.zox = dedupeZoxAgainst(items, m.base)
		m.zoxStale = zoxItemsStale(age, true)
	} else {
		m.zox = nil
		m.zoxStale = true
	}
	if m.store != nil {
		now := time.Now().Unix()
		if us, err := m.store.AllUsage(); err == nil {
			applyUsage(m.base, us, now)
			applyUsage(m.zox, us, now)
		}
		if m.ctl != nil {
			m.ctx = m.ctl.CurrentSession()
		}
		if m.ctx != "" {
			m.pairs, _ = m.store.PairScores(m.ctx, now)
		} else {
			m.pairs = nil
		}
		applyCooccur(m.base, m.pairs)
		applyCooccur(m.zox, m.pairs)
	}
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
