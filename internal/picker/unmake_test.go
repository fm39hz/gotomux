package picker

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/fm39hz/gotomux/internal/config"
)

func unmakeFixture(t *testing.T, kind Kind) (model, *countingConnector, *countingStore, int) {
	t.Helper()
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40, GitConcurrency: 1}
	f := newFixture()
	ctl, st := f.doubles()
	m := newModelCore(cfg, Deps{Ctl: ctl, Store: st}, "newproj", "/w/newproj", Seed{})

	for i, it := range m.ui.items {
		if it.Kind == kind {
			m.ui.cursor = i
			m.ui.syncViewport()
			return m, ctl, st, i
		}
	}
	t.Fatalf("fixture has no %v row: %v", kind, itemKeys(m.ui.items))
	return m, ctl, st, -1
}

func press(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

// TestUnmakeIsChosenByRowKind pins the contract behind a single destructive
// chord: ^d steps the row under the cursor down one level of existence, and the
// row's Kind picks the operation.
//
// The assertions are deliberately about which calls happened, not only about the
// status line. Killing must not delete the preset — that asymmetry is what makes
// the step land on Preset instead of on nothing — and deleting a preset must not
// fork tmux. A row that names a directory rather than a stored object must
// destroy nothing and, because the same chord is fatal two levels up, say so.
func TestUnmakeIsChosenByRowKind(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        Kind
		wantKill    string
		wantDelete  string
		wantKillLog string
		wantStatus  string
	}{
		{
			name: "active steps down to preset", kind: KindActive,
			wantKill: "alpha", wantKillLog: "alpha", wantStatus: "killed alpha",
		},
		{
			name: "preset steps down to nothing", kind: KindPreset,
			wantDelete: "gamma", wantStatus: "deleted gamma",
		},
		{
			name: "zoxide row destroys nothing", kind: KindZoxide,
			wantStatus: "nothing to remove",
		},
		{
			name: "create row destroys nothing", kind: KindCreate,
			wantStatus: "nothing to remove",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ctl, st, idx := unmakeFixture(t, tc.kind)
			if m.ui.items[idx].Name == "" {
				t.Fatal("cursor landed on an unnamed row")
			}

			got := press(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

			if got.ui.status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.ui.status, tc.wantStatus)
			}
			if got.Done().Action != ActionNone {
				t.Errorf("^d quit the picker: %+v", got.Done())
			}
			checkNames(t, "tmux kill", ctl.killed, tc.wantKill)
			checkNames(t, "preset delete", st.deletedPreset, tc.wantDelete)
			checkNames(t, "kill telemetry", st.killLogged, tc.wantKillLog)
		})
	}
}

func checkNames(t *testing.T, what string, got []string, want string) {
	t.Helper()
	if want == "" {
		if len(got) != 0 {
			t.Errorf("%s called %v, want none", what, got)
		}
		return
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("%s called %v, want [%s]", what, got, want)
	}
}

// TestCtrlXIsInert guards the merge itself. The old kill chord must not keep
// destroying anything through some leftover path, or the two keys are still live
// and the help line lies about which one does it.
func TestCtrlXIsInert(t *testing.T) {
	m, ctl, st, _ := unmakeFixture(t, KindActive)

	press(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})

	if len(ctl.killed) != 0 || len(st.deletedPreset) != 0 {
		t.Errorf("ctrl+x still acts: kill=%v delete=%v", ctl.killed, st.deletedPreset)
	}
	if key.Matches(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}, defaultKeyMap.Unmake) {
		t.Error("ctrl+x matches the unmake binding")
	}
}

// TestUnmakeHelpNamesTheRealVerb covers the honesty half of the merge. With one
// chord, the cursor is what chooses the blast radius, so the help line has to
// carry the verb rather than advertise a fixed "delete preset" that is wrong on
// an Active row.
func TestUnmakeHelpNamesTheRealVerb(t *testing.T) {
	for _, tc := range []struct {
		name string
		item Item
		want string
		show bool
	}{
		{name: "active", item: Item{Kind: KindActive, Name: "alpha"}, want: "kill alpha", show: true},
		{name: "preset", item: Item{Kind: KindPreset, Name: "gamma"}, want: "del gamma", show: true},
		{name: "create", item: Item{Kind: KindCreate, Name: "newproj"}},
		{name: "zoxide", item: Item{Kind: KindZoxide, Name: "eps"}},
		{name: "empty list", item: Item{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, ok := unmakeBinding(tc.item)
			if ok != tc.show {
				t.Fatalf("shown = %v, want %v", ok, tc.show)
			}
			short := defaultKeyMap.ShortHelp(tc.item)
			if !tc.show {
				for _, kb := range short {
					if kb.Help().Key == "^d" {
						t.Fatalf("short help advertises ^d for an inert row: %+v", kb.Help())
					}
				}
				return
			}
			if b.Help().Key != "^d" {
				t.Errorf("help key = %q, want ^d", b.Help().Key)
			}
			if !strings.HasPrefix(b.Help().Desc, tc.want) {
				t.Errorf("help description = %q, want %q", b.Help().Desc, tc.want)
			}
			if len(short) != 6 {
				t.Errorf("short help has %d entries, want 6 with ^d", len(short))
			}
			if !key.Matches(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, b) {
				t.Error("contextual binding no longer matches ctrl+d")
			}
		})
	}
}
