package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLabelRegularRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/master\n")
	if got := Label(dir); got != "master" {
		t.Errorf("Label = %q, want master", got)
	}
}

func TestLabelLinkedWorktree(t *testing.T) {
	// The capability the daemon's own reader lacked: a linked worktree has .git as
	// a FILE pointing at the real git dir. Reading only .git/HEAD returns nothing,
	// so worktrees silently showed no branch whenever a daemon served the payload.
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wt := filepath.Join(root, "wt")

	writeFile(t, filepath.Join(repo, ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/feature\n")
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")

	got := Label(wt)
	if got != "feature | worktree" {
		t.Errorf("Label = %q, want %q", got, "feature | worktree")
	}
}

func TestLabelRelativeGitdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "HEAD"), "ref: refs/heads/rel\n")
	writeFile(t, filepath.Join(root, "wt", ".git"), "gitdir: ../real\n")

	if got := Label(filepath.Join(root, "wt")); got != "rel | worktree" {
		t.Errorf("Label = %q, want %q", got, "rel | worktree")
	}
}

func TestLabelDetachedHead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678\n")
	if got := Label(dir); got != "" {
		t.Errorf("detached HEAD gave %q, want empty", got)
	}
}

func TestLabelNotARepo(t *testing.T) {
	if got := Label(t.TempDir()); got != "" {
		t.Errorf("non-repo gave %q, want empty", got)
	}
	if got := Label(""); got != "" {
		t.Errorf("empty path gave %q, want empty", got)
	}
	if got := Label("/definitely/not/here/at/all"); got != "" {
		t.Errorf("missing path gave %q, want empty", got)
	}
}

func TestLabels(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	plain := filepath.Join(root, "plain")
	writeFile(t, filepath.Join(a, ".git", "HEAD"), "ref: refs/heads/one\n")
	writeFile(t, filepath.Join(b, ".git", "HEAD"), "ref: refs/heads/two\n")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	// Duplicates and empties must not produce extra work or extra entries.
	got := Labels([]string{a, b, plain, a, "", b}, 4)
	if len(got) != 2 {
		t.Fatalf("Labels = %v, want 2 entries", got)
	}
	if got[a] != "one" || got[b] != "two" {
		t.Errorf("Labels = %v", got)
	}
	if _, ok := got[plain]; ok {
		t.Error("non-repo present in Labels; absence is how misses are represented")
	}
}
