package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

// --- unit tests: no tmux required, run under -short ---

func TestQuoteArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"list-sessions", "list-sessions"},
		{"-F", "-F"},
		{"even-vertical", "even-vertical"},
		{"/home/a/b.c", "/home/a/b.c"},
		// A TAB must force quoting. The previous quote set omitted it, so the
		// tab-separated list format was split into argv by tmux and only "S"
		// survived as the format string.
		{"S\t#{session_name}", "'S\t#{session_name}'"},
		{"#{session_id}", "'#{session_id}'"},
		{"a b", "'a b'"},
		{";", "';'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
	}
	for _, c := range cases {
		if got := quoteArg(c.in); got != c.want {
			t.Errorf("quoteArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildCommandQuotesFormat(t *testing.T) {
	line := buildCommand([]string{"list-sessions", "-F", ListSessFmt})
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("command not newline-terminated: %q", line)
	}
	if !strings.Contains(line, "'"+ListSessFmt+"'") {
		t.Errorf("format not single-quoted in %q", line)
	}
	if strings.Count(line, " ") != 2 {
		// "list-sessions", "-F", "<quoted fmt>" — the tabs inside the quoted
		// format must not become argument separators.
		t.Errorf("unexpected argument split: %q", line)
	}
}

func TestDropHiddenKeepsOrderAndNotNil(t *testing.T) {
	in := []LiveSession{{Name: "a"}, {Name: HiddenControlSession}, {Name: "b"}}
	got := DropHidden(in)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("DropHidden = %v, want [a b]", got)
	}
	if empty := DropHidden([]LiveSession{{Name: HiddenControlSession}}); empty == nil || len(empty) != 0 {
		t.Errorf("DropHidden(all-hidden) = %v, want non-nil empty", empty)
	}
}

func TestClientBlockUsesBeginFlag(t *testing.T) {
	// Flag 0 is the unsolicited block emitted for the connection's own
	// new-session; handing it to the first Send would mispair every reply after.
	if clientBlock("%begin 1785069236 677 0") {
		t.Error("flag 0 block treated as a client reply")
	}
	if !clientBlock("%begin 1785069237 686 1") {
		t.Error("flag 1 block not treated as a client reply")
	}
	if clientBlock("%begin garbage") {
		t.Error("malformed begin line treated as a client reply")
	}
}

func TestMembershipEventNames(t *testing.T) {
	// Guards the event names measured against tmux 3.7b. Creating a session
	// emits %sessions-changed — there is no %session-created.
	if !IsHiddenSession(HiddenControlSession) {
		t.Error("IsHiddenSession false for its own constant")
	}
	if IsHiddenSession("gotomux") {
		t.Error("IsHiddenSession true for a user session")
	}
}

// --- live tests: need a tmux binary, skipped under -short ---

// isolatedServer points tmux at a private socket dir and proves the isolation
// before any teardown is registered — see internal/tmuxtest for why the proof is
// not optional.
func isolatedServer(t *testing.T) {
	t.Helper()
	tmuxtest.Isolate(t)
}

func tmuxOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		t.Fatalf("tmux %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

const attachProbe = "#{session_name} #{session_attached} #{session_last_attached}"

func TestControlConnListsAndDoesNotPerturb(t *testing.T) {
	isolatedServer(t)
	tmuxtest.NewSessions(t, "zt-alpha", "zt-beta")
	before := tmuxOut(t, "list-sessions", "-F", attachProbe)

	cc, err := StartControl()
	if err != nil {
		t.Fatalf("StartControl: %v", err)
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := cc.SendLines(
		ctx,
		[]string{"list-sessions", "-F", ListSessFmt},
		[]string{"list-panes", "-s", "-F", ListPanesFmt},
	)
	if err != nil {
		t.Fatalf("SendLines: %v", err)
	}

	live := ParseLiveOutput(raw)
	got := map[string]LiveSession{}
	for _, s := range live {
		got[s.Name] = s
	}
	for _, want := range []string{"zt-alpha", "zt-beta"} {
		s, ok := got[want]
		if !ok {
			t.Fatalf("session %q missing from %d parsed sessions; raw:\n%s", want, len(live), raw)
		}
		// A bare "S" format would parse to nothing, so a populated Path proves
		// the quoted format string survived tmux's own lexer.
		if s.Path == "" {
			t.Errorf("%s has empty Path — format string was split, not quoted", want)
		}
	}

	// The whole reason for owning a hidden session: user sessions must be
	// untouched. Attaching to one would set attached=1 and bump
	// last_attached, which feeds LiveSession recency.
	after := tmuxOut(t, "list-sessions", "-F", attachProbe)
	beforeUser := withoutHiddenLines(before)
	afterUser := withoutHiddenLines(after)
	if beforeUser != afterUser {
		t.Errorf("control client perturbed user sessions:\nbefore: %s\nafter:  %s", beforeUser, afterUser)
	}
	if !strings.Contains(after, HiddenControlSession) {
		t.Errorf("hidden session %q not present; consumers rely on filtering it", HiddenControlSession)
	}
}

func withoutHiddenLines(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" && !strings.HasPrefix(line, HiddenControlSession+" ") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

func TestControlConnEmitsMembershipEvents(t *testing.T) {
	isolatedServer(t)
	tmuxtest.NewSessions(t, "zt-base")
	cc, err := StartControl()
	if err != nil {
		t.Fatalf("StartControl: %v", err)
	}
	defer cc.Close()

	// Drain the connection's own startup notifications.
	drainFor(cc, 300*time.Millisecond)

	tmuxtest.NewSessions(t, "zt-late")

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-cc.Events():
			if strings.HasPrefix(ev, "%sessions-changed") {
				return
			}
		case <-deadline:
			t.Fatal("no sessions-changed notification within 5s of creating a session")
		}
	}
}

// ListLive feeds every exec-path consumer: the picker sources after any
// mutating action (kill/freeze/delete clear the seeded cache and reload
// through here) and the freeze pick list. A leak of the reserved name on this
// path is how __gotomuxd got painted as a regular session right after a kill,
// even though the daemon's IPC payload was filtered correctly.
func TestListLiveDropsHiddenControlSession(t *testing.T) {
	isolatedServer(t)
	tmuxtest.NewSessions(t, "zt-vis")
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", HiddenControlSession).CombinedOutput(); err != nil {
		t.Fatalf("new-session %s: %v: %s", HiddenControlSession, err, strings.TrimSpace(string(out)))
	}

	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	live, err := c.ListLive(context.Background())
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range live {
		if IsHiddenSession(s.Name) {
			t.Errorf("hidden control session leaked past ListLive: %+v", live)
		}
		seen[s.Name] = true
	}
	if !seen["zt-vis"] {
		t.Errorf("visible session zt-vis missing from ListLive output: %+v", live)
	}
}

func drainFor(cc *ControlConn, d time.Duration) {
	timeout := time.After(d)
	for {
		select {
		case <-cc.Events():
		case <-timeout:
			return
		}
	}
}

func TestControlConnErrorThenReconnect(t *testing.T) {
	isolatedServer(t)
	tmuxtest.NewSessions(t, "zt-err")
	cc, err := StartControl()
	if err != nil {
		t.Fatalf("StartControl: %v", err)
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A malformed command must surface as an error, not hang and not be
	// mistaken for empty output. tmux reports it via %error, with the message in
	// the block body — not on the %error line itself.
	if _, err := cc.Send(ctx, "no-such-tmux-command"); err == nil {
		t.Fatal("bogus command returned no error")
	}

	// tmux may drop the client after a parse error; recovery must work.
	if !cc.Alive() {
		if err := cc.Reconnect(); err != nil {
			t.Fatalf("Reconnect after error: %v", err)
		}
	}
	out, err := cc.Send(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		t.Fatalf("Send after recovery: %v", err)
	}
	if !strings.Contains(out, "zt-err") {
		t.Errorf("post-recovery output %q missing zt-err", out)
	}
}
