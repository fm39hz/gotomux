package picker

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type animTickMsg struct{}

type Action int

const (
	ActionNone Action = iota
	ActionConnect
	ActionQuit
)

type Result struct {
	Action Action
	Item   Item
	Err    error
}

type animEntry struct {
	cur float64 // current rendering Y
	dst float64 // target Y (rank index)
}

type model struct {
	sources   []Source
	bySrc     map[Source][]Item
	view      []Item
	cursorIdx int      // index in view (logical), for keyboard navigation
	cursorKey string   // anim key of selected item, stable across animation
	query     string
	ctl      tmux.Connector
	store    *store.Store
	status   string
	done     Result
	width    int
	height   int
	maxShow  int
	cache    sourceCache
	help     bool
	tmpl     string
	started  time.Time
	env      Context
	editPath   string
	editOld    string
	createName string
	createCwd  string
	anim     map[string]animEntry // item key → Y position
}

func animKey(it Item) string { return it.Name + "\x00" + it.Path }

var (
	styleCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	// weight: Active strongest -> Preset -> Create -> Zoxide dimmest
	styleActive = lipgloss.NewStyle().Foreground(lipgloss.Color("15")) // bright white
	stylePreset = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))  // normal
	styleCreate = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))  // cyan - action
	styleZoxide = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func (m model) Done() Result { return m.done }

func styleFor(k Kind) lipgloss.Style {
	switch k {
	case KindActive:
		return styleActive
	case KindPreset:
		return stylePreset
	case KindCreate:
		return styleCreate
	case KindZoxide:
		return styleZoxide
	default:
		return stylePreset
	}
}

func NewModelFromDaemon(ctl tmux.Connector, st *store.Store, createName, createCwd string, sessions []tmux.LiveSession, presets []store.PresetMeta, env Context) model {
	var cache sourceCache
	cache.zoxSt = st
	cache.zoxMu = &sync.Mutex{}
	cache.tmuxSnap = sessions
	cache.tmuxOK = true
	cache.presetM = presets
	cache.presetOK = true
	srcs := defaultSources(ctl, st, createName, createCwd, &cache)
	bySrc := snapshotAll(srcs)
	applyRankMeta(bySrc, st, env)
	enrichAllSync(bySrc)
	m := model{
		sources:    srcs,
		bySrc:      bySrc,
		cache:      cache,
		ctl:        ctl,
		store:      st,
		maxShow:    12,
		tmpl:       template.StickyLabel(st),
		started:    time.Now(),
		env:        env,
		createName: createName,
		createCwd:  createCwd,
	}
	m.refilter()
	return m
}

func NewModel(ctl tmux.Connector, store *store.Store, createName, createCwd string) model {
	var cache sourceCache
	cache.zoxSt = store
	cache.zoxMu = &sync.Mutex{}
	srcs := defaultSources(ctl, store, createName, createCwd, &cache)
	bySrc := snapshotAll(srcs)
	env := newContext(ctl, store)
	applyRankMeta(bySrc, store, env)
	enrichAllSync(bySrc)
	m := model{
		sources:    srcs,
		bySrc:      bySrc,
		cache:      cache,
		ctl:        ctl,
		store:      store,
		maxShow:    12,
		tmpl:       template.StickyLabel(store),
		started:    time.Now(),
		env:        env,
		createName: createName,
		createCwd:  createCwd,
	}
	m.refilter()
	return m
}



func (m *model) pool() []Item {
	return flattenSources(m.sources, m.bySrc, strings.TrimSpace(m.query))
}

func (m *model) mergeSource(src Source, items []Item) {
	if m.bySrc == nil {
		m.bySrc = map[Source][]Item{}
	}
	slot := map[Source][]Item{src: items}
	applyRankMeta(slot, m.store, m.env)
	m.bySrc[src] = slot[src]
}

func (m *model) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.query))
	m.view = rankItems(q, m.pool())

	// Sync cursorKey: find previously selected item in new view.
	if m.cursorKey != "" && len(m.view) > 0 {
		found := false
		for i, it := range m.view {
			if animKey(it) == m.cursorKey {
				m.cursorIdx = i
				found = true
				break
			}
		}
		if !found {
			m.cursorIdx = 0
		}
	} else if len(m.view) > 0 {
		m.cursorIdx = 0
	}
	if m.cursorIdx >= len(m.view) {
		m.cursorIdx = len(m.view) - 1
	}
	if m.cursorIdx < 0 && len(m.view) > 0 {
		m.cursorIdx = 0
	}
	if len(m.view) > 0 {
		m.cursorKey = animKey(m.view[m.cursorIdx])
	} else {
		m.cursorKey = ""
	}

	for i := range m.view {
		setGitBranch(&m.view[i])
	}
	// Update animation targets — keep old currents for new items.
	old := m.anim
	m.anim = map[string]animEntry{}
	for i, it := range m.view {
		key := animKey(it)
		if e, ok := old[key]; ok {
			e.dst = float64(i)
			m.anim[key] = e
		} else {
			m.anim[key] = animEntry{cur: float64(i), dst: float64(i)}
		}
	}
}

// refilterFromQuery: user edited filter -> jump to best match.
func (m *model) refilterFromQuery() {
	m.refilter()
	m.cursorIdx = 0
}

const animSpeed = 0.3

// animTick moves all items toward their target by animSpeed fraction.
// Returns false when all items have settled.
func (m *model) animTick() bool {
	busy := false
	for k := range m.anim {
		e := m.anim[k]
		if e.cur == e.dst {
			continue
		}
		e.cur += (e.dst - e.cur) * animSpeed
		if math.Abs(e.cur-e.dst) < 0.01 {
			e.cur = e.dst
		} else {
			busy = true
		}
		m.anim[k] = e
	}
	return busy
}

func animSettled(anim map[string]animEntry) bool {
	for _, e := range anim {
		if e.cur != e.dst {
			return false
		}
	}
	return true
}

func (m *model) totalCount() int {
	return countSources(m.bySrc)
}

func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, refreshCmds(m.sources)...)
	if !animSettled(m.anim) {
		cmds = append(cmds, tea.Tick(time.Second/60, func(t time.Time) tea.Msg { return animTickMsg{} }))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case animTickMsg:
		if m.animTick() {
			return m, tea.Tick(time.Second/60, func(t time.Time) tea.Msg { return animTickMsg{} })
		}
		return m, nil

	case sourceMsg:
		if len(msg.items) == 0 {
			return m, nil
		}
		m.mergeSource(msg.src, msg.items)
		m.refilter()
		// Start animation if needed
		if !animSettled(m.anim) {
			return m, tea.Tick(time.Second/60, func(t time.Time) tea.Msg { return animTickMsg{} })
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// inline mode: keep list short like fzf --height
		if m.maxShow <= 0 {
			m.maxShow = 12
		}
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			m.done = Result{Action: ActionQuit}
			return m, tea.Quit

		case "esc":
			// display-popup + bind -n M-* : releasing Alt often injects ESC into the
			// new pane and would quit instantly. Ignore brief false ESC after open.
			if time.Since(m.started) < 500*time.Millisecond {
				return m, nil
			}
			m.done = Result{Action: ActionQuit}
			return m, tea.Quit

		case "?":
			m.help = !m.help
			return m, nil

		case "ctrl+t": // sticky <- shape from selection; Create/Zox use it
			if len(m.view) > 0 {
				it := m.view[m.cursorIdx]
				var p *store.Preset
				var err error
				switch it.Kind {
				case KindPreset:
					p, err = m.store.Get(it.Name)
				case KindActive:
					p, err = m.ctl.Freeze(it.Name)
				default:
					if err := template.ResetActive(m.store); err != nil {
						m.status = err.Error()
					} else {
						m.tmpl = "default"
						m.status = "sticky: default"
					}
					return m, nil
				}
				if err != nil {
					m.status = err.Error()
					return m, nil
				}
				id, created, err := template.StickFrom(m.store, p)
				if err != nil {
					m.status = err.Error()
					return m, nil
				}
				m.tmpl = template.StickyLabel(m.store)
				if m.tmpl == "" || m.tmpl == id {
					m.tmpl = template.ShapeLabel(template.ToShape(p, id))
				}
				if created {
					m.status = "sticky <- " + m.tmpl + "  (new)"
				} else {
					m.status = "sticky <- " + m.tmpl
				}
				return m, nil
			}
			if err := template.ResetActive(m.store); err != nil {
				m.status = err.Error()
			} else {
				m.tmpl = "default"
				m.status = "sticky: default"
			}
			return m, nil

		case "enter":
			if len(m.view) > 0 && m.cursorIdx < len(m.view) {
				m.done = Result{Action: ActionConnect, Item: m.view[m.cursorIdx]}
				m.query = ""
				m.view = m.view[:0]
				return m, tea.Quit
			}

		case "ctrl+n", "down":
			if len(m.view) > 0 {
				m.cursorIdx = (m.cursorIdx + 1) % len(m.view)
				m.cursorKey = animKey(m.view[m.cursorIdx])
			}
			return m, nil

		case "ctrl+p", "up":
			if len(m.view) > 0 {
				m.cursorIdx--
				if m.cursorIdx < 0 {
					m.cursorIdx = len(m.view) - 1
				}
				m.cursorKey = animKey(m.view[m.cursorIdx])
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
				it := m.view[m.cursorIdx]
				if it.Kind == KindActive {
					if err := m.ctl.Kill(it.Name); err != nil {
						m.status = err.Error()
					} else {
						if m.store != nil {
							_ = m.store.RecordKill(it.Name)
						}
						m.status = "killed " + it.Name
						m.cache.invalidate()
						m.reload()
					}
				}
			}
			return m, nil

		case "ctrl+f": // freeze instance+shape; sticky stays intentional (^t)
			if len(m.view) > 0 {
				it := m.view[m.cursorIdx]
				name := it.Name
				if it.Kind == KindActive || (it.Kind == KindPreset && m.ctl.Has(name)) {
					stop := HoldInterrupt()
					sid, created, err := template.FreezeRemember(m.ctl, m.store, name)
					stop()
					if err != nil {
						m.status = err.Error()
						return m, nil
					}
					if created {
						m.status = "froze " + name + " | shape " + sid
					} else if sid != "" {
						m.status = "froze " + name + " | shape " + sid + " (exists)"
					} else {
						m.status = "froze " + name
					}
					m.cache.invalidate()
					m.reload()
				} else if it.Kind == KindPreset {
					m.status = "session not running - attach first"
				}
			}
			return m, nil

		case "ctrl+e": // edit preset (Active: freeze to preset first)
			if len(m.view) == 0 {
				return m, nil
			}
			it := m.view[m.cursorIdx]
			switch it.Kind {
			case KindActive:
				stop := HoldInterrupt()
				_, _, err := template.FreezeRemember(m.ctl, m.store, it.Name)
				stop()
				if err != nil {
					m.status = err.Error()
					return m, nil
				}
				cmd, err := m.beginEdit(it.Name)
				if err != nil {
					m.status = err.Error()
					return m, nil
				}
				ClearInline(m.FrameLines())
				return m, cmd
			case KindPreset:
				cmd, err := m.beginEdit(it.Name)
				if err != nil {
					m.status = err.Error()
					return m, nil
				}
				ClearInline(m.FrameLines())
				return m, cmd
			default:
				m.status = "edit: pick Active or Preset"
			}
			return m, nil

		case "ctrl+d": // delete preset
			if len(m.view) > 0 {
				it := m.view[m.cursorIdx]
				if it.Kind == KindPreset {
					if err := m.store.Delete(it.Name); err != nil {
						m.status = err.Error()
					} else {
						m.status = "deleted " + it.Name
						m.cache.invalidate()
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
			// plain printable -> filter
			if text := msg.Key().Text; text != "" {
				for _, r := range text {
					if unicode.IsPrint(r) {
						m.query += string(r)
					}
				}
				m.refilterFromQuery()
			}
		}

	case editDoneMsg:
		// editor left junk below; wipe so repaint is single frame
		ClearInline(m.FrameLines())
		if msg.err != nil {
			m.status = msg.err.Error()
		} else {
			m.status = "saved " + msg.name
			m.cache.invalidate()
			m.reload()
		}
		return m, nil
	}
	if !animSettled(m.anim) {
		return m, tea.Tick(time.Second/60, func(t time.Time) tea.Msg { return animTickMsg{} })
	}
	return m, nil
}

func (m *model) reload() {
	m.sources = defaultSources(m.ctl, m.store, m.createName, m.createCwd, &m.cache)
	m.bySrc = snapshotAll(m.sources)
	m.env = newContext(m.ctl, m.store)
	applyRankMeta(m.bySrc, m.store, m.env)
	enrichAllSync(m.bySrc)
	m.refilter()
}

// FrameLines is fixed height of View - wipe residual inline UI after quit.
func (m model) FrameLines() int {
	maxShow := m.maxShow
	if maxShow <= 0 {
		maxShow = 12
	}
	// prompt line + header + list + status
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
	dir, err := store.DataDir()
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "edit-*.json")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if _, err := f.WriteString(template.Format(p)); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	f.Close()
	m.editPath = path
	m.editOld = name

	c := editorCmd(path)
	if c.Stdin == nil {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	st := m.store
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
		np, err := template.Parse(string(raw))
		if err != nil {
			return editDoneMsg{err: fmt.Errorf("parse: %w", err)}
		}
		stop := HoldInterrupt()
		err = template.CommitEdit(st, old, np)
		stop()
		if err != nil {
			return editDoneMsg{err: err}
		}
		return editDoneMsg{name: np.Name}
	}), nil
}

// editorCmd opens path in $EDITOR (default nvim).
func editorCmd(path string) *exec.Cmd {
	ed := os.Getenv("EDITOR")
	if ed == "" {
		ed = os.Getenv("VISUAL")
	}
	if ed == "" {
		ed = "nvim"
	}
	if fields := strings.Fields(ed); len(fields) > 1 {
		return exec.Command(fields[0], append(fields[1:], path)...)
	}
	return exec.Command(ed, path)
}

func trimLastWord(s string) string {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	i := strings.LastIndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return ""
	}
	return s[:i+1]
}

func (m model) View() tea.View {
	var b strings.Builder

	// First line: continuation prompt + query (giống gõ ở shell prompt).
	b.WriteString(styleDim.Render(iconPrompt()))
	b.WriteString(m.query)
	b.WriteByte('\n')
	// Second line: header với count + meta + keys.
	meta := fmt.Sprintf("  %d/%d", len(m.view), m.totalCount())
	if m.help {
		meta += "  ^n/p | enter | ^t sticky | ^x kill | ^f freeze | ^e edit | ^d del | ^u/^w | esc"
	} else if m.tmpl != "" && m.tmpl != "default" {
		meta += formatStickyMeta(m.tmpl) + "  enter | esc | ?"
	} else {
		meta += "  enter | esc | ?"
	}
	b.WriteString(styleHeader.Render(meta))
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
		half := maxShow / 2
		start := m.cursorIdx - half
		if start < 0 {
			start = 0
		}
		if start+maxShow > len(m.view) {
			start = len(m.view) - maxShow
		}
		if start < 0 {
			start = 0
		}
		end := start + maxShow
		if end > len(m.view) {
			end = len(m.view)
		}

		// Sort visible items by animated Y so moving items slide smoothly.
		type visItem struct{ it Item }
		vis := make([]visItem, 0, end-start)
		for i := start; i < end; i++ {
			vis = append(vis, visItem{m.view[i]})
		}
		if !animSettled(m.anim) {
			sort.SliceStable(vis, func(a, b int) bool {
				ya := m.anim[animKey(vis[a].it)].cur
				yb := m.anim[animKey(vis[b].it)].cur
				return ya < yb
			})
		}
		for _, v := range vis {
			it := v.it
			line := it.Title
			if it.GitBranch != "" {
				line += " (" + it.GitBranch + ")"
			}
			if it.Desc != "" {
				titleW := lipgloss.Width(line)
				if titleW < 44 {
					line += strings.Repeat(" ", 44-titleW)
				} else {
					line = truncateRunes(line, 42)
					line += "  "
				}
				line += styleDim.Render(it.Desc)
			}
			if m.width > 4 {
				line = truncateRunes(line, m.width-2)
			}
			if animKey(it) == m.cursorKey {
				b.WriteString(styleCursor.Render(iconCursor() + line))
			} else {
				b.WriteString(styleFor(it.Kind).Render("  " + line))
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

	// status always occupies 1 line - fixed frame height for clearInline
	if m.status != "" {
		b.WriteString(styleStatus.Render(m.status))
	}
	b.WriteByte('\n')
	return tea.NewView(b.String())
}


