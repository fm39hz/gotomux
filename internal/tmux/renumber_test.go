package tmux

import (
	"context"
	"testing"

	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

func TestStabilizeWindowIndicesRestoresInheritedOptionAfterResume(t *testing.T) {
	tmuxtest.Isolate(t)
	runIsolatedTmux(t, "new-session", "-d", "-s", "renumber", "-c", "/tmp", "sleep", "300")
	runIsolatedTmux(t, "set-option", "-g", "renumber-windows", "on")
	ctl, _ := New()
	sessionID := runIsolatedTmux(t, "list-sessions", "-F", "#{session_id}")
	restore, err := ctl.StabilizeWindowIndices(context.Background(), sessionID, "run-stable-1")
	if err != nil {
		t.Fatalf("stabilize: %v", err)
	}
	if got := runIsolatedTmux(t, "show-options", "-qv", "-t", sessionID, "renumber-windows"); got != "off" {
		t.Fatalf("temporary session option = %q, want off", got)
	}
	// Simulate process death: a new invocation resumes from the marker.
	resume, err := ctl.StabilizeWindowIndices(context.Background(), sessionID, "run-stable-1")
	if err != nil {
		t.Fatalf("resume stable-index mode: %v", err)
	}
	if err := resume(); err != nil {
		t.Fatalf("restore after resumed run: %v", err)
	}
	if got := runIsolatedTmux(t, "show-options", "-gqv", "renumber-windows"); got != "on" {
		t.Fatalf("global renumber-windows after restore = %q, want on", got)
	}
	if got := runIsolatedTmux(t, "show-options", "-q", "-t", sessionID, "renumber-windows"); got != "" {
		t.Fatalf("session override remained after restore: %q", got)
	}
	_ = restore // the resumed closure already restored the shared marker.
}

func TestStabilizeWindowIndicesRestoresExplicitLocalOverride(t *testing.T) {
	tmuxtest.Isolate(t)
	runIsolatedTmux(t, "new-session", "-d", "-s", "renumber-local", "-c", "/tmp", "sleep", "300")
	runIsolatedTmux(t, "set-option", "-t", "renumber-local", "renumber-windows", "on")
	ctl, _ := New()
	sessionID := runIsolatedTmux(t, "list-sessions", "-F", "#{session_id}")
	restore, err := ctl.StabilizeWindowIndices(context.Background(), sessionID, "run-stable-2")
	if err != nil {
		t.Fatalf("stabilize local override: %v", err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore local override: %v", err)
	}
	if got := runIsolatedTmux(t, "show-options", "-qv", "-t", sessionID, "renumber-windows"); got != "on" {
		t.Fatalf("local renumber-windows after restore = %q, want on", got)
	}
}
