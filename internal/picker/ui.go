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
	// Live is the set of session names that were live when the user confirmed.
	// Carried on the result so the caller can record co-occurrence without
	// re-listing tmux, and so it reflects exactly what was on screen.
	Live []string
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

// viewModel

type model struct {
	sources []Source
	bySrc   map[Source][]Item
	ctl     tmux.Connector
	store   store.Storer
	// openStore lazily provides a store when store is nil (daemon mode).
	openStore  func() store.Storer
	cache      *sourceCache
	cfg        *config.Config
	env        Context
	tmpl       string
	createName string
	createCwd  string
	sessID     string
	ui         viewModel
}

// ensureStore returns a store, opening one on first use.
//
// In daemon mode the paint path has no store at all: every input is seeded, so
// opening SQLite (which also runs the migration probes) would put work on cold
// start for data already in hand. The action keys genuinely need one, and they
// are user-initiated and rare, so that is where the cost belongs.
func (m *model) ensureStore() store.Storer {
	if m.store != nil {
		return m.store
	}
	if m.openStore == nil {
		return nil
	}
	m.store = m.openStore()
	if m.cache != nil {
		m.cache.zoxSt = m.store
	}
	return m.store
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

func zoxCapFrom(cfg *config.Config) int {
	if cfg != nil && cfg.ZoxideCap > 0 {
		return cfg.ZoxideCap
	}
	return defaultZoxCap
}

// gitConc bounds concurrency for git-branch reads. It used to return
// cfg.MaxShow — a display setting driving an I/O thread budget — while
// cfg.GitConcurrency existed and was read nowhere.
func gitConc(cfg *config.Config) int {
	if cfg != nil && cfg.GitConcurrency > 0 {
		return cfg.GitConcurrency
	}
	return 4
}

func initInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = ""
	ti.Focus()
	return ti
}

// Deps are the collaborators the picker acts through.
type Deps struct {
	Ctl   tmux.Connector
	Store store.Storer
	// OpenStore supplies a store on demand when Store is nil. The paint path
	// needs no store when everything is seeded, but the action keys
	// (kill/freeze/delete/edit) do — so the SQLite open moves off cold start and
	// onto the first action instead of being skipped or forced.
	OpenStore func() store.Storer
	// SessionID is this process's tmux session id ("$0"), read from $TMUX at the
	// edge. Holding it here rather than calling os.Getenv deep in the package keeps
	// context derivation testable and independent of where the test runs.
	SessionID string
}

// Seed carries pre-computed picker inputs, normally straight from the daemon.
// Fields left zero are gathered locally, which is what makes the two run modes
// one code path with different inputs rather than two implementations.
type Seed struct {
	Sessions    []tmux.LiveSession
	Presets     []store.PresetMeta
	ZoxideItems []Item
	StickyLabel string
	// Env, when non-nil, replaces the locally derived ranking context.
	Env *Context
	// Seeded marks the payload authoritative: empty slices then mean "empty",
	// not "unknown", and no source may fall back to tmux or SQLite.
	Seeded bool
}

// assemble builds the model value and produces the first ranked list. Split out
// so the profiler can time it as one stage of a single pass instead of repeating
// the whole construction — and so there remains exactly one place where this
// struct is populated.
func assemble(cfg *config.Config, d Deps, createName, createCwd string,
	cache *sourceCache, srcs []Source, bySrc map[Source][]Item,
	env Context, stickyLabel string) model {

	tmpl := stickyLabel
	if tmpl == "" && d.Store != nil {
		tmpl = template.StickyLabel(d.Store)
	}
	m := model{
		sources:    srcs,
		bySrc:      bySrc,
		cache:      cache,
		ctl:        d.Ctl,
		store:      d.Store,
		openStore:  d.OpenStore,
		cfg:        cfg,
		tmpl:       tmpl,
		env:        env,
		createName: createName,
		createCwd:  createCwd,
		sessID:     d.SessionID,
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

// newModelCore is the single construction path. Both NewModel and
// NewModelFromDaemon (and ProfileRun) route through it, because "the two paths
// must stay behaviorally identical" is only true if there is one path; three
// copies of this literal is what let them drift.
func newModelCore(cfg *config.Config, d Deps, createName, createCwd string, seed Seed) model {
	cache := &sourceCache{
		zoxMu:  &sync.Mutex{},
		zoxSt:  d.Store,
		zoxCap: zoxCapFrom(cfg),
		seeded: seed.Seeded,
	}
	if seed.Seeded {
		cache.tmuxSnap = seed.Sessions
		cache.presetM = seed.Presets
		cache.tmuxDone.Store(true)
		cache.presetDone.Store(true)
		if len(seed.ZoxideItems) > 0 {
			cache.zoxMem = seed.ZoxideItems
			// Only stamp freshness when there is something to be fresh about; the
			// old code stamped time.Now() even for an empty payload.
			cache.zoxAt = time.Now()
		}
	}

	srcs := defaultSources(d.Ctl, d.Store, createName, createCwd, cache)
	bySrc := snapshotAll(srcs)

	env := Context{}
	if seed.Env != nil {
		env = *seed.Env
	} else {
		env = newContext(d.Ctl, d.Store, cache.tmuxSnap, d.SessionID)
	}
	// Co-occurrence is meaningless without a current session. The guard lived
	// only in newContext, so the daemon path — which built Context by hand in
	// main — could apply pair scores with no session context at all.
	if env.Session == "" {
		env.Pairs = nil
	}
	if env.Now == 0 {
		env.Now = time.Now().Unix()
	}
	applyRankMeta(bySrc, d.Store, env)

	m := assemble(cfg, d, createName, createCwd, cache, srcs, bySrc, env, seed.StickyLabel)
	if !seed.Seeded {
		// Fill git labels for the rows about to be shown, so the first paint is
		// visually complete. This is safe to do after ranking because GitBranch is
		// display-only — rankOf never reads it — so no amount of late enrichment can
		// reorder the list. The remaining paths are done in the background by
		// enrichRestCmd.
		//
		// Standalone runs previously showed no branch on any row until the user
		// pressed a key that triggered reload(), the only place enrichment ran.
		m.enrichVisible()
	}
	return m
}

// enrichVisible reads git labels for the rows currently on screen.
func (m *model) enrichVisible() {
	n := m.ui.maxShow
	if n <= 0 || n > len(m.ui.items) {
		n = len(m.ui.items)
	}
	if n == 0 {
		return
	}
	paths := make([]string, 0, n)
	for i := range m.ui.items[:n] {
		paths = append(paths, m.ui.items[i].Path)
	}
	enrichPaths(paths, gitConc(m.cfg))
	for i := range m.ui.items[:n] {
		setGitBranch(&m.ui.items[i])
	}
}

// gitDoneMsg reports that background git enrichment finished.
type gitDoneMsg struct{}

// enrichRestCmd reads the git labels that enrichVisible did not cover.
// Returns nil when the daemon already supplied them.
func (m model) enrichRestCmd() tea.Cmd {
	if m.cache == nil || m.cache.seeded {
		return nil
	}
	paths := collectPaths(m.bySrc)
	if len(paths) == 0 {
		return nil
	}
	conc := gitConc(m.cfg)
	return func() tea.Msg {
		enrichPaths(paths, conc)
		return gitDoneMsg{}
	}
}

// NewModelFromDaemon builds the picker from a daemon payload.
func NewModelFromDaemon(cfg *config.Config, d Deps, createName, createCwd string, seed Seed) model {
	seed.Seeded = true
	return newModelCore(cfg, d, createName, createCwd, seed)
}

// NewModel builds the picker by reading everything locally.
func NewModel(cfg *config.Config, ctl tmux.Connector, st store.Storer, createName, createCwd string) model {
	d := Deps{Ctl: ctl, Store: st, SessionID: tmux.CurrentSessionID()}
	return newModelCore(cfg, d, createName, createCwd, Seed{})
}

// liveNames returns the live session names already held in the source cache. No
// tmux call: this is the snapshot the list was painted from.
func (m *model) liveNames() []string {
	if m.cache == nil || len(m.cache.tmuxSnap) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.cache.tmuxSnap))
	for _, s := range m.cache.tmuxSnap {
		out = append(out, s.Name)
	}
	return out
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
	if c := m.enrichRestCmd(); c != nil {
		cmds = append(cmds, c)
	}
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

	case gitDoneMsg:
		// Re-apply labels only. Deliberately not a refilter: GitBranch does not
		// participate in ranking, so re-ranking here could only risk a visible jump
		// for no benefit.
		for i := range m.ui.items {
			setGitBranch(&m.ui.items[i])
		}
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
				m.ui.done = Result{
					Action: ActionConnect,
					Item:   m.ui.items[m.ui.cursor],
					Live:   m.liveNames(),
				}
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
			st := m.ensureStore()
			if st == nil {
				m.ui.status = "sticky: store unavailable"
				return m, nil
			}
			if len(m.ui.items) > 0 {
				it := m.ui.items[m.ui.cursor]
				var p *mod.Session
				var err error
				switch it.Kind {
				case KindPreset:
					p, err = st.Get(it.Name)
				case KindActive:
					var s *mod.Session
					s, err = m.ctl.Freeze(context.Background(), it.Name)
					if err == nil {
						p = s
					}
				default:
					if err := template.ResetActive(st); err != nil {
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
				id, created, err := template.StickFrom(st, p)
				if err != nil {
					m.ui.status = err.Error()
					return m, nil
				}
				m.tmpl = template.StickyLabel(st)
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
			if err := template.ResetActive(st); err != nil {
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
						if st := m.ensureStore(); st != nil {
							_ = st.RecordKill(it.Name)
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
					sid, created, err := template.FreezeRemember(m.ctl, m.ensureStore(), name)
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
				_, _, err := template.FreezeRemember(m.ctl, m.ensureStore(), it.Name)
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
					if err := m.ensureStore().Delete(it.Name); err != nil {
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

// reload re-reads every source after a mutating action.
//
// It invalidates the cache first, including the seeded flag: a kill, delete or
// freeze makes the daemon's payload wrong, so this must read from tmux and SQLite
// rather than re-serving what was handed over at startup.
func (m *model) reload() {
	savedScroll := m.ui.scrollOff()
	st := m.ensureStore()
	m.cache.invalidate()
	m.sources = defaultSources(m.ctl, st, m.createName, m.createCwd, m.cache)
	m.bySrc = snapshotAll(m.sources)
	m.env = newContext(m.ctl, st, m.cache.tmuxSnap, m.sessID)
	applyRankMeta(m.bySrc, st, m.env)
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
	p, err := m.ensureStore().Get(name)
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
	st := m.ensureStore()
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
		if m.ui.done.Action != ActionConnect {
			return tea.View{}
		}
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
