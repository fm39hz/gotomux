package picker

import (
	mod "github.com/fm39hz/gotomux/internal/model"
	"testing"
)

func TestProjectionRefreshRetainsCurrentLiveCollisionEvidence(t *testing.T) {
	live := &testSource{}
	presets := &testSource{}
	m := model{
		sources: []Source{live, presets},
		bySrc: map[Source][]Item{
			live:    {{Kind: KindActive, Name: "current", RuntimeID: "$1", ServerKey: "server", Identity: mod.EntityIdentity{RuntimeID: "$1", ServerKey: "server"}}},
			presets: {{Kind: KindPreset, Name: "current", Path: "/work"}},
		},
		env: Context{Session: "current", Identity: mod.EntityIdentity{RuntimeID: "$1", ServerKey: "server"}},
	}
	if got := m.pool(); len(got) != 0 {
		t.Fatalf("initial current action leaked: %+v", got)
	}
	if len(m.bySrc[live]) != 1 || len(m.bySrc[presets]) != 1 {
		t.Fatal("projection destroyed source observations")
	}
	m.mergeSource(presets, []Item{{Kind: KindPreset, Name: "current", Path: "/work"}})
	if got := m.pool(); len(got) != 0 {
		t.Fatalf("refresh lost current collision evidence: %+v", got)
	}
	m.mergeSource(live, nil)
	if got := m.pool(); len(got) != 1 || got[0].Kind != KindPreset {
		t.Fatalf("preset did not become available after live removal: %+v", got)
	}
}

func TestProjectionKeepsIndependentSessionsAtSameProject(t *testing.T) {
	source := &testSource{}
	rows := []Item{
		{Kind: KindActive, Name: "one", Path: "/work/repo", RuntimeID: "$1", ServerKey: "server"},
		{Kind: KindActive, Name: "two", Path: "/work/repo", RuntimeID: "$2", ServerKey: "server"},
		{Kind: KindPreset, Name: "saved", Path: "/work/repo", Identity: mod.EntityIdentity{LogicalID: "saved-instance"}},
	}
	got := flattenSources([]Source{source}, map[Source][]Item{source: rows}, "")
	if len(got) != 3 {
		t.Fatalf("independent session targets collapsed by project: %+v", got)
	}
	if got[0].ID() == got[1].ID() || got[0].ID() == got[2].ID() {
		t.Fatal("independent actions share an ID")
	}
	renamed := got[0]
	renamed.Name = "renamed"
	renamed.Path = "/different/project"
	if renamed.ID() != got[0].ID() {
		t.Fatal("live action ID depends on mutable presentation fields")
	}
}
