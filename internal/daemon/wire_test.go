package daemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// TestResponseRoundTrip: every populated field must survive the wire. The payload
// leaks internal types (tmux.LiveSession, store.PresetMeta/Usage/ZoxRow), so a
// field added to any of them silently rides along — or silently does not.
func TestResponseRoundTrip(t *testing.T) {
	want := Response{
		OK: true, Ready: true, SyncedAt: 1785069096, Version: 7,
		Sessions: []tmux.LiveSession{
			{ID: "$0", Name: "alpha", Windows: 3, Path: "/w/a",
				LastAttached: 11, Activity: 12, Created: 10, Attached: 1, ActiveCmd: "nvim"},
		},
		Presets:     []store.PresetMeta{{Name: "p1", Cwd: "/w/p1", LastUsed: 99}},
		Pairs:       map[string]int64{"beta": 500},
		Usage:       map[string]store.Usage{"beta": {Name: "beta", Opens: 3, Kills: 1, LastOpen: 5, LastKill: 2}},
		StickyLabel: "nvim+v2",
		GitBranches: map[string]string{"/w/a": "master | worktree"},
		Zoxide:      []store.ZoxRow{{Name: "z", Path: "/w/z", Title: "[Zoxide] z", Desc: "d", Recency: 4}},
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Response
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip lost data:\nwant %+v\ngot  %+v\nraw: %s", want, got, raw)
	}
}

// TestWireNamingIsSnakeCase guards a consistency wart: Response uses snake_case
// json tags, but the embedded store types had no tags at all, so one payload
// mixed "sticky_label" with Go field names like "Name"/"Path".
func TestWireNamingIsSnakeCase(t *testing.T) {
	raw, err := json.Marshal(Response{
		OK:       true,
		Zoxide:   []store.ZoxRow{{Name: "z", Path: "/w/z", Recency: 1}},
		Sessions: []tmux.LiveSession{{ID: "$0", Name: "a", Path: "/a"}},
		Presets:  []store.PresetMeta{{Name: "p", Cwd: "/p"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, capitalized := range []string{`"Name"`, `"Path"`, `"Recency"`, `"Cwd"`, `"LastUsed"`, `"ActiveCmd"`} {
		if strings.Contains(s, capitalized) {
			t.Errorf("payload contains Go-style key %s; wire uses snake_case: %s", capitalized, s)
		}
	}
}

func TestRequestRoundTrip(t *testing.T) {
	want := Request{Cmd: "list", Name: "n", SessID: "$3"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Request
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("round trip: want %+v got %+v (raw %s)", want, got, raw)
	}
}
