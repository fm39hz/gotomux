package picker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/fm39hz/gotomux/internal/config"
	mod "github.com/fm39hz/gotomux/internal/model"
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

// viewModel — UI state, tách biệt khỏi business logic.
type viewModel struct {
	items      []Item
	cursor     int
	selID      ID
	queryInput textinput.Model
	status     string
	done       Result
	width      int
	height     int
	maxShow    int
	helpOpen   bool
	helpModel  help.Model
	started    time.Time
	editPath   string
	editOld    string
}

func (v *viewModel) scrollOff() int {
	ms := v.maxShow
	if ms <= 0 {
		ms = 12
	}
	half := ms / 2
	s := v.cursor - half
	if s < 0 {
		s = 0
	}
	if s+ms > len(v.items) {
		s = len(v.items) - ms
	}
	if s < 0 {
		s = 0
	}
	return s
}

func (v *viewModel) visible() []Item {
	start := v.scrollOff()
	end := start + v.maxShow
	if end > len(v.items) {
		end = len(v.items)
	}
	return v.items[start:end]
}

// viewModel

type model struct {
	sources    []Source
	bySrc      map[Source][]Item
	ctl        tmux.Connector
	store      store.Storer
	cache      *sourceCache
	cfg        *config.Config
	env        Context
	tmpl       string
	createName string
	createCwd  string
	ui         viewModel
}

// ID now method on Item { return it.Name + "\x00" + it.Path }

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

func (m model) Done() Result { return m.ui.done }

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

func maxShow(cfg *config.Config) int {
	if cfg != nil && cfg.MaxShow > 0 {
		return cfg.MaxShow
	}
	return 12
}

func applyUICfg(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.ZoxideCap > 0 {
		zoxCap = cfg.ZoxideCap
	}
}

func gitConc(cfg *config.Config) int {
	if cfg != nil && cfg.MaxShow > 0 {
		return cfg.MaxShow
	}
	return 12
}

func initInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = ""
	ti.Focus()
	return ti
}

func NewModelFromDaemon(cfg *config.Config, ctl tmux.Connector, st store.Storer, createName, createCwd string, sessions []tmux.LiveSession, presets []store.PresetMeta, env Context) model {
	cache := &sourceCache{
		zoxSt:    st,
		zoxMu:    &sync.Mutex{},
		tmuxSnap: sessions,
		presetM:  presets,
	}
	cache.tmuxOK.Store(true)
	cache.presetOK.Store(true)
	applyUICfg(cfg)
	srcs := defaultSources(ctl, st, createName, createCwd, cache)
	bySrc := snapshotAll(srcs)
	applyRankMeta(bySrc, st, env)
	enrichAllSyncWith(bySrc, gitConc(cfg))
	m := model{
		sources:    srcs,
		bySrc:      bySrc,
		cache:      cache,
		ctl:        ctl,
		store:      st,
		cfg:        cfg,
		tmpl:       template.StickyLabel(st),
		env:        env,
		createName: createName,
		createCwd:  createCwd,
		ui: viewModel{
			queryInput: initInput(),
			helpModel:  help.New(),
			maxShow:    maxShow(cfg),
			started:    time.Now(),
		},
	}
	m.refilter()
	return m
}

func NewModel(cfg *config.Config, ctl tmux.Connector, store store.Storer, createName, createCwd string) model {
	cache := &sourceCache{
		zoxSt: store,
		zoxMu: &sync.Mutex{},
	}
	applyUICfg(cfg)
	srcs := defaultSources(ctl, store, createName, createCwd, cache)
	bySrc := snapshotAll(srcs)
	env := newContext(ctl, store)
	applyRankMeta(bySrc, store, env)
	enrichAllSyncWith(bySrc, gitConc(cfg))
	m := model{
		sources:    srcs,
		bySrc:      bySrc,
		cache:      cache,
		ctl:        ctl,
		store:      store,
		cfg:        cfg,
		tmpl:       template.StickyLabel(store),
		env:        env,
		createName: createName,
		createCwd:  createCwd,
		ui: viewModel{
			queryInput: initInput(),
			helpModel:  help.New(),
			maxShow:    maxShow(cfg),
			started:    time.Now(),
		},
	}
	m.refilter()
	return m
}



func (m *model) pool() []Item {
	return flattenSources(m.sources, m.bySrc, strings.TrimSpace(m.ui.queryInput.Value()))
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
	// Preserve scroll offset so visual position doesn't jump on kill/delete.
	q := strings.ToLower(strings.TrimSpace(m.ui.queryInput.Value()))
	m.ui.items = rankItems(q, m.pool())

	if m.ui.cursor >= len(m.ui.items) {
		m.ui.cursor = len(m.ui.items) - 1
	}
	if m.ui.cursor < 0 && len(m.ui.items) > 0 {
		m.ui.cursor = 0
	}
	if len(m.ui.items) > 0 {
		m.ui.selID = m.ui.items[m.ui.cursor].ID()
	} else {
		m.ui.selID = ""
	}

	for i := range m.ui.items {
		setGitBranch(&m.ui.items[i])
	}
}

func (m *model) refilterFromQuery() {
	m.refilter()
	m.ui.cursor = 0
	if len(m.ui.items) > 0 {
		m.ui.selID = m.ui.items[0].ID()
	} else {
		m.ui.selID = ""
	}
}

func (m *model) totalCount() int {
	return countSources(m.bySrc)
}

func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, textinput.Blink)
	cmds = append(cmds, refreshCmds(m.sources)...)
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case sourceMsg:
		if len(msg.items) == 0 {
			return m, nil
		}
		m.mergeSource(msg.src, msg.items)
		m.refilter()
		return m, nil

	case tea.WindowSizeMsg:
		m.ui.width, m.ui.height = msg.Width, msg.Height
		if m.ui.maxShow <= 0 {
			m.ui.maxShow = 12
		}
		m.ui.helpModel.SetWidth(msg.Width)

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, defaultKeyMap.Quit):
			if msg.String() == "esc" && time.Since(m.ui.started) < 500*time.Millisecond {
				return m, nil
			}
			m.ui.done = Result{Action: ActionQuit}
			return m, tea.Quit

		case key.Matches(msg, defaultKeyMap.Help):
			m.ui.helpOpen = !m.ui.helpOpen
			return m, nil

		case key.Matches(msg, defaultKeyMap.Confirm):
			if len(m.ui.items) > 0 && m.ui.cursor < len(m.ui.items) {
				m.ui.done = Result{Action: ActionConnect, Item: m.ui.items[m.ui.cursor]}
				m.ui.queryInput.SetValue("")
				m.ui.items = m.ui.items[:0]
				return m, tea.Quit
			}
			return m, nil

		case key.Matches(msg, defaultKeyMap.Up):
			if len(m.ui.items) > 0 {
				m.ui.cursor--
				if m.ui.cursor < 0 {
					m.ui.cursor = len(m.ui.items) - 1
				}
				m.ui.selID = m.ui.items[m.ui.cursor].ID()
			}
			return m, nil

		case key.Matches(msg, defaultKeyMap.Down):
			if len(m.ui.items) > 0 {
				m.ui.cursor = (m.ui.cursor + 1) % len(m.ui.items)
				m.ui.selID = m.ui.items[m.ui.cursor].ID()
			}
			return m, nil

		case key.Matches(msg, defaultKeyMap.Sticky):
			if len(m.ui.items) > 0 {
				it := m.ui.items[m.ui.cursor]
				var p *mod.Session
				var err error
				switch it.Kind {
				case KindPreset:
					p, err = m.store.Get(it.Name)
				case KindActive:
					var s *mod.Session
					s, err = m.ctl.Freeze(context.Background(), it.Name)
					if err == nil {
						p = s
					}
				default:
					if err := template.ResetActive(m.store); err != nil {
						m.ui.status = err.Error()
					} else {
						m.tmpl = "default"
						m.ui.status = "sticky: default"
					}
					return m, nil
				}
				if err != nil {
					m.ui.status = err.Error()
					return m, nil
				}
				id, created, err := template.StickFrom(m.store, p)
				if err != nil {
					m.ui.status = err.Error()
					return m, nil
				}
				m.tmpl = template.StickyLabel(m.store)
				if m.tmpl == "" || m.tmpl == id {
					m.tmpl = template.ShapeLabel(template.ToShape(p, id))
				}
				if created {
					m.ui.status = "sticky <- " + m.tmpl + "  (new)"
				} else {
					m.ui.status = "sticky <- " + m.tmpl
				}
				return m, nil
			}
			if err := template.ResetActive(m.store); err != nil {
				m.ui.status = err.Error()
			} else {
				m.tmpl = "default"
				m.ui.status = "sticky: default"
			}
			return m, nil

		case key.Matches(msg, defaultKeyMap.Kill):
			if len(m.ui.items) > 0 {
				it := m.ui.items[m.ui.cursor]
				if it.Kind == KindActive {
					if err := m.ctl.Kill(context.Background(), it.Name); err != nil {
						m.ui.status = err.Error()
					} else {
						if m.store != nil {
							_ = m.store.RecordKill(it.Name)
						}
						m.ui.status = "killed " + it.Name
						m.cache.invalidate()
						m.reload()
					}
				}
			}
			return m, nil

		case key.Matches(msg, defaultKeyMap.Freeze):
			if len(m.ui.items) > 0 {
				it := m.ui.items[m.ui.cursor]
				name := it.Name
				if it.Kind == KindActive || (it.Kind == KindPreset && m.ctl.Has(context.Background(), name)) {
					stop := HoldInterrupt()
					sid, created, err := template.FreezeRemember(m.ctl, m.store, name)
					stop()
					if err != nil {
						m.ui.status = err.Error()
						return m, nil
					}
					if created {
						m.ui.status = "froze " + name + " | shape " + sid
					} else if sid != "" {
						m.ui.status = "froze " + name + " | shape " + sid + " (exists)"
					} else {
						m.ui.status = "froze " + name
					}
					m.cache.invalidate()
					m.reload()
				} else if it.Kind == KindPreset {
					m.ui.status = "session not running - attach first"
				}
			}
			return m, nil

		case key.Matches(msg, defaultKeyMap.Edit):
			if len(m.ui.items) == 0 {
				return m, nil
			}
			it := m.ui.items[m.ui.cursor]
			switch it.Kind {
			case KindActive:
				stop := HoldInterrupt()
				_, _, err := template.FreezeRemember(m.ctl, m.store, it.Name)
				stop()
				if err != nil {
					m.ui.status = err.Error()
					return m, nil
				}
				cmd, err := m.beginEdit(it.Name)
				if err != nil {
					m.ui.status = err.Error()
					return m, nil
				}
				ClearInline(m.FrameLines())
				return m, cmd
			case KindPreset:
				cmd, err := m.beginEdit(it.Name)
				if err != nil {
					m.ui.status = err.Error()
					return m, nil
				}
				ClearInline(m.FrameLines())
				return m, cmd
			default:
				m.ui.status = "edit: pick Active or Preset"
			}
			return m, nil

		case key.Matches(msg, defaultKeyMap.Delete):
			if len(m.ui.items) > 0 {
				it := m.ui.items[m.ui.cursor]
				if it.Kind == KindPreset {
					if err := m.store.Delete(it.Name); err != nil {
						m.ui.status = err.Error()
					} else {
						m.ui.status = "deleted " + it.Name
						m.cache.invalidate()
						m.reload()
					}
				}
			}
			return m, nil
		}

		// Modifier chords: don't pass to textinput (prevent alt+key insertion)
		if msg.Key().Mod != 0 && msg.Key().Mod != tea.ModShift {
			return m, nil
		}

	case editDoneMsg:
		ClearInline(m.FrameLines())
		if msg.err != nil {
			m.ui.status = msg.err.Error()
		} else {
			m.ui.status = "saved " + msg.name
			m.cache.invalidate()
			m.reload()
		}
		return m, nil
	}

	// Pass remaining messages to textinput (BlinkMsg, WindowSizeMsg, unhandled KeyPressMsg, etc.)
	prev := m.ui.queryInput.Value()
	var cmd tea.Cmd
	m.ui.queryInput, cmd = m.ui.queryInput.Update(msg)
	if m.ui.queryInput.Value() != prev {
		m.refilterFromQuery()
	}
	return m, cmd
}

func (m *model) reload() {
	savedScroll := m.ui.scrollOff()
	m.sources = defaultSources(m.ctl, m.store, m.createName, m.createCwd, m.cache)
	m.bySrc = snapshotAll(m.sources)
	m.env = newContext(m.ctl, m.store)
	applyRankMeta(m.bySrc, m.store, m.env)
	enrichAllSyncWith(m.bySrc, gitConc(m.cfg))
	m.refilter()
	// Actions (kill/freeze/delete/edit) change list length → preserve scroll.
	if savedScroll > 0 && savedScroll != m.ui.scrollOff() {
		half := m.ui.maxShow / 2
		c := savedScroll + half
		if c >= len(m.ui.items) {
			c = len(m.ui.items) - 1
		}
		if c >= 0 {
			m.ui.cursor = c
			m.ui.selID = m.ui.items[c].ID()
		}
	}
}

// FrameLines is fixed height of View - wipe residual inline UI after quit.
func (m model) FrameLines() int {
	maxShow := m.ui.maxShow
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
	f, err := os.CreateTemp("", "gotomux-edit-*.json")
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
	m.ui.editPath = path
	m.ui.editOld = name

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

func (m model) View() tea.View {
	if m.ui.done.Action != ActionNone {
		var b strings.Builder
		b.WriteString(m.ui.done.Item.Title)
		b.WriteByte('\n')
		return tea.NewView(b.String())
	}

	var b strings.Builder

	b.WriteString(styleDim.Render(iconPrompt()))
	b.WriteString(m.ui.queryInput.View())
	b.WriteByte('\n')

	meta := fmt.Sprintf("  %d/%d", len(m.ui.items), m.totalCount())
	if m.ui.helpOpen {
		meta += "  " + m.ui.helpModel.ShortHelpView(defaultKeyMap.ShortHelp())
	} else if m.tmpl != "" && m.tmpl != "default" {
		meta += formatStickyMeta(m.tmpl) + "  enter | esc | ?"
	} else {
		meta += "  enter | esc | ?"
	}
	b.WriteString(styleHeader.Render(meta))
	b.WriteByte('\n')

	maxShow := m.ui.maxShow
	if maxShow <= 0 {
		maxShow = 12
	}

	shown := 0
	if len(m.ui.items) == 0 {
		b.WriteString(styleDim.Render("  (no match)"))
		b.WriteByte('\n')
		shown = 1
	} else {
		half := maxShow / 2
		start := m.ui.cursor - half
		if start < 0 {
			start = 0
		}
		if start+maxShow > len(m.ui.items) {
			start = len(m.ui.items) - maxShow
		}
		if start < 0 {
			start = 0
		}
		end := start + maxShow
		if end > len(m.ui.items) {
			end = len(m.ui.items)
		}

		for i := start; i < end; i++ {
			it := m.ui.items[i]
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
			if m.ui.width > 4 {
				line = truncateRunes(line, m.ui.width-2)
			}
			if it.ID() == m.ui.selID {
				b.WriteString(styleCursor.Render(iconCursor() + line))
			} else {
				b.WriteString(styleFor(it.Kind).Render("  " + line))
			}
			b.WriteByte('\n')
			shown++
		}
	}
	for shown < maxShow {
		b.WriteByte('\n')
		shown++
	}

	if m.ui.status != "" {
		b.WriteString(styleStatus.Render(m.ui.status))
	}
	b.WriteByte('\n')
	return tea.NewView(b.String())
}


