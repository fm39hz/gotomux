package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// row builds one S line in the exact shape ListSessFmt produces.
func row(id, name, windows, path, lastAttached, activity, created, attached string) string {
	return "S\t" + id + "\t" + name + "\t" + windows + "\t" + path + "\t" +
		lastAttached + "\t" + activity + "\t" + created + "\t" + attached + "\n"
}

func TestParseLiveOutputFields(t *testing.T) {
	out := row("$0", "alpha", "3", "/home/u/alpha", "1785069096", "1785069100", "1785069000", "1") +
		row("$1", "beta", "1", "/home/u/beta", "", "1785069050", "1785069050", "0") +
		"P\talpha\tnvim\t1\t0\n" +
		"P\tbeta\tzsh\t1\t0\n"

	live := ParseLiveOutput(out)
	if len(live) != 2 {
		t.Fatalf("parsed %d sessions, want 2: %+v", len(live), live)
	}

	a := live[0]
	if a.ID != "$0" || a.Name != "alpha" {
		t.Errorf("first row = id %q name %q, want $0/alpha", a.ID, a.Name)
	}
	if a.Windows != 3 || a.Path != "/home/u/alpha" {
		t.Errorf("first row = %d windows, path %q", a.Windows, a.Path)
	}
	if a.LastAttached != 1785069096 || a.Activity != 1785069100 || a.Created != 1785069000 {
		t.Errorf("timestamps = %d/%d/%d", a.LastAttached, a.Activity, a.Created)
	}
	if a.Attached != 1 {
		t.Errorf("Attached = %d, want 1", a.Attached)
	}
	if a.ActiveCmd != "nvim" {
		t.Errorf("ActiveCmd = %q, want nvim", a.ActiveCmd)
	}

	// A never-attached session reports an EMPTY session_last_attached, not 0, so
	// the numeric fields have to tolerate "" or the whole row is lost.
	b := live[1]
	if b.Name != "beta" {
		t.Fatalf("second row = %q, want beta", b.Name)
	}
	if b.LastAttached != 0 {
		t.Errorf("empty last_attached parsed as %d, want 0", b.LastAttached)
	}
	if b.Path != "/home/u/beta" {
		t.Errorf("second row Path = %q — an empty numeric field shifted the parse", b.Path)
	}
}

func TestParseLiveOutputIgnoresJunk(t *testing.T) {
	out := "%some-notification\n" +
		"\n" +
		"S\ttoo\tfew\tfields\n" +
		row("$2", "gamma", "1", "/g", "0", "0", "0", "0")
	live := ParseLiveOutput(out)
	if len(live) != 1 || live[0].Name != "gamma" {
		t.Errorf("parsed %+v, want just gamma", live)
	}
}

func TestSessionIDFromEnv(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/tmp/tmux-1000/default,2150,0", "$0"},
		{"/tmp/tmux-1000/default,2150,7", "$7"},
		{"", ""},
		{"garbage", ""},
		{"/tmp/s,123", ""},
		{"/tmp/s,123,notanumber", ""},
	}
	for _, c := range cases {
		if got := SessionIDFromEnv(c.in); got != c.want {
			t.Errorf("SessionIDFromEnv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFindByID(t *testing.T) {
	live := []LiveSession{{ID: "$0", Name: "a"}, {ID: "$3", Name: "b"}}
	if s, ok := FindByID(live, "$3"); !ok || s.Name != "b" {
		t.Errorf("FindByID($3) = %+v,%v; want b,true", s, ok)
	}
	if _, ok := FindByID(live, "$9"); ok {
		t.Error("FindByID matched a missing id")
	}
	if _, ok := FindByID(live, ""); ok {
		t.Error("FindByID matched an empty id")
	}
}

// TestIsNoServerErrorReadsStderr pins a dead branch that mattered.
//
// tmux reports "no server running on …" on stderr. exec.Cmd.Output() surfaces that
// through ExitError.Stderr, but err.Error() is only "exit status 1" — so matching
// on the message alone made IsNoServerError always false for ListLive, leaving its
// (nil, nil) branch unreachable and `gotomux -f` reporting a raw exit status
// instead of "no active sessions".
func TestIsNoServerErrorReadsStderr(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")

	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	live, err := c.ListLive(context.Background())
	if err != nil {
		t.Fatalf("ListLive with no server = %v; want (nil, nil) via IsNoServerError", err)
	}
	if len(live) != 0 {
		t.Errorf("ListLive returned %d sessions with no server", len(live))
	}
}

func TestIsNoServerErrorClassification(t *testing.T) {
	if IsNoServerError(nil) {
		t.Error("nil classified as no-server")
	}
	if !IsNoServerError(errors.New("no server running on /tmp/tmux-1000/default")) {
		t.Error("plain message not classified")
	}
	if IsNoServerError(errors.New("exit status 1")) {
		t.Error("bare exit status classified as no-server")
	}
	// The shape Output() actually produces: message says nothing, stderr says it.
	wrapped := fmt.Errorf("tmux list: %w", &exec.ExitError{
		ProcessState: nil,
		Stderr:       []byte("no server running on /tmp/tmux-1000/default\n"),
	})
	if !IsNoServerError(wrapped) {
		t.Error("stderr-carried no-server not classified through a wrap")
	}
}
