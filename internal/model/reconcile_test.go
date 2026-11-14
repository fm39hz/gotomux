package model

import (
	"encoding/json"
	"testing"
)

func TestBaselineJSONKeepsSessionAndSlotIdentitySeparate(t *testing.T) {
	want := Session{
		Name: "demo", LogicalID: "session-uuid", TmuxID: "$4", ServerKey: "server-a", SchemaVersion: 3, ClassifierVersion: 1,
		Windows: []Window{{SlotID: "window-slot", TmuxID: "@8", Role: "editor", Panes: []Pane{{SlotID: "pane-slot", TmuxID: "%12"}}}},
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encode baseline: %v", err)
	}
	var got Session
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if got.LogicalID != want.LogicalID || got.TmuxID != want.TmuxID || got.ClassifierVersion != want.ClassifierVersion || got.Windows[0].SlotID != want.Windows[0].SlotID || got.Windows[0].TmuxID != want.Windows[0].TmuxID || got.Windows[0].Role != want.Windows[0].Role || got.Windows[0].Panes[0].SlotID != want.Windows[0].Panes[0].SlotID || got.Windows[0].Panes[0].TmuxID != want.Windows[0].Panes[0].TmuxID {
		t.Fatalf("session/slot identity round trip = %+v, want %+v", got, want)
	}
}
