package picker

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

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

type model struct {
	sources  []Source
	bySrc    map[string][]Item // Snapshot/Refresh slots keyed by Source.ID
	view     []Item
	cursor   int
	query    string
	ctl      *tmux.Ctl
	store    *store.Store
	status   string
	done     Result
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

func NewModel(ctl *tmux.Ctl, store *store.Store, createName, createCwd string) model {
	srcs := defaultSources(ctl, store, createName, createCwd)
	bySrc := snapshotAll(srcs)
	ctx := ""
	if ctl != nil {
		ctx = ctl.CurrentSession()
	}
	now := time.Now().Unix()
	var pairs map[string]int64
	if store != nil && ctx != "" {
		pairs, _ = store.PairScores(ctx, now)
	}
	applyRankMeta(bySrc, store, pairs, ctx)
	m := model{
		sources: srcs,
		bySrc:   bySrc,
		ctl:     ctl,
		store:   store,
		maxShow: 12,
		tmpl:    template.StickyLabel(store),
		started: time.Now(),
		ctx:     ctx,
		pairs:   pairs,
	}
	m.refilter()
	return m
}

func (m *model) pool() []Item {
	return flattenSources(m.sources, m.bySrc, strings.TrimSpace(m.query))
}

func (m *model) mergeSource(id string, items []Item) {
	if m.bySrc == nil {
		m.bySrc = map[string][]Item{}
	}
	for i := range items {
		if items[i].Src == "" {
			items[i].Src = id
		}
	}
	slot := map[string][]Item{id: items}
	applyRankMeta(slot, m.store, m.pairs, m.ctx)
	m.bySrc[id] = slot[id]
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
	return countSources(m.bySrc)
}

func (m model) Init() tea.Cmd {
	cmds := refreshCmds(m.sources)
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case sourceMsg:
		if len(msg.items) == 0 {
			return m, nil
		}
		m.mergeSource(msg.id, msg.items)
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

		case "ctrl+t": // sticky ← shape from selection; Create/Zox use it
			if len(m.view) > 0 {
				it := m.view[m.cursor]
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
					m.status = "sticky ← " + m.tmpl + "  (new)"
				} else {
					m.status = "sticky ← " + m.tmpl
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
			if len(m.view) > 0 && m.cursor < len(m.view) {
				m.done = Result{Action: ActionConnect, Item: m.view[m.cursor]}
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
				if it.Kind == KindActive {
					if err := m.ctl.Kill(it.Name); err != nil {
						m.status = err.Error()
					} else {
						if m.store != nil {
							_ = m.store.RecordKill(it.Name)
						}
						m.status = "killed " + it.Name
						m.reload()
					}
				}
			}
			return m, nil

		case "ctrl+f": // freeze instance+shape; sticky stays intentional (^t)
			if len(m.view) > 0 {
				it := m.view[m.cursor]
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
						m.status = "froze " + name + " · shape " + sid
					} else if sid != "" {
						m.status = "froze " + name + " · shape " + sid + " (exists)"
					} else {
						m.status = "froze " + name
					}
					m.reload()
				} else if it.Kind == KindPreset {
					m.status = "session not running — attach first"
				}
			}
			return m, nil

		case "ctrl+e": // edit preset (Active: freeze to preset first)
			if len(m.view) == 0 {
				return m, nil
			}
			it := m.view[m.cursor]
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
				it := m.view[m.cursor]
				if it.Kind == KindPreset {
					if err := m.store.Delete(it.Name); err != nil {
						m.status = err.Error()
					} else {
						m.status = "deleted " + it.Name
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
		ClearInline(m.FrameLines())
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
	// re-snapshot every source (truth for sync sources; cache for zoxide)
	m.bySrc = snapshotAll(m.sources)
	now := time.Now().Unix()
	if m.ctl != nil {
		m.ctx = m.ctl.CurrentSession()
	}
	if m.store != nil && m.ctx != "" {
		m.pairs, _ = m.store.PairScores(m.ctx, now)
	} else {
		m.pairs = nil
	}
	applyRankMeta(m.bySrc, m.store, m.pairs, m.ctx)
	m.refilter()
}

// FrameLines is fixed height of View — wipe residual inline UI after quit.
func (m model) FrameLines() int {
	maxShow := m.maxShow
	if maxShow <= 0 {
		maxShow = 12
	}
	// filter+meta line + list + status
	return maxShow + 2
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

func (m model) View() string {
	var b strings.Builder

	// Filter line — fzf-like "> ", NOT shell ❯ (avoids double-prompt look under fish/starship).
	// Shell chrome stays above; we only own the inline block below the real prompt.
	b.WriteString(styleDim.Render("❯ "))
	b.WriteString(m.query)
	// count + keys on same line as filter (compact, less "second shell")
	meta := fmt.Sprintf("  %d/%d", len(m.view), m.totalCount())
	if m.help {
		meta += "  ^n/p · enter · ^t sticky · ^x kill · ^f freeze · ^e edit · ^d del · ^u/^w · esc"
	} else if m.tmpl != "" && m.tmpl != "default" {
		meta += "  sticky:" + m.tmpl + "  enter · esc · ?"
	} else {
		meta += "  enter · esc · ?"
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
			line := it.Title
			if it.Desc != "" {
				line = line + "  " + it.Desc
			}
			if m.width > 4 {
				line = truncateRunes(line, m.width-2)
			}
			if i == m.cursor {
				b.WriteString(styleCursor.Render("▸ " + line))
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

	// status always occupies 1 line — fixed frame height for clearInline
	if m.status != "" {
		b.WriteString(styleStatus.Render(m.status))
	}
	b.WriteByte('\n')
	return b.String()
}
