package zoxide

import "testing"

// Paths under a non-existent prefix: FindProjectRoot returns its argument when
// it walks up without finding a project marker, so these are deterministic
// regardless of where the test runs.
const pfx = "/tmp/gotomux-zoxide-test-nonexistent"

func TestRowsRecencyIsListOrder(t *testing.T) {
	rows := Rows([]string{pfx + "/alpha", pfx + "/beta", pfx + "/gamma"})
	if len(rows) != 3 {
		t.Fatalf("Rows = %d rows, want 3", len(rows))
	}
	// Recency must be derived from position, not from a clock. A timestamp here
	// puts every zoxide directory ahead of every live tmux session, because the
	// ranking tuple compares recency before kind.
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Recency <= rows[i].Recency {
			t.Errorf("recency not strictly descending at %d: %d <= %d",
				i, rows[i-1].Recency, rows[i].Recency)
		}
	}
	if rows[0].Recency != 3 || rows[2].Recency != 1 {
		t.Errorf("recency = %d..%d, want 3..1 (len-index)", rows[0].Recency, rows[2].Recency)
	}
	if rows[0].Recency > 1_000_000 {
		t.Errorf("recency %d looks like a timestamp, want a list index", rows[0].Recency)
	}
}

func TestRowsDedupByName(t *testing.T) {
	// Two different parents, same leaf → same session name → one row.
	rows := Rows([]string{pfx + "/one/proj", pfx + "/two/proj"})
	if len(rows) != 1 {
		t.Fatalf("Rows = %d rows, want 1 (name dedup)", len(rows))
	}
	if rows[0].Path != pfx+"/one/proj" {
		t.Errorf("kept %q, want the first occurrence", rows[0].Path)
	}
}

func TestRowsDedupByPath(t *testing.T) {
	rows := Rows([]string{pfx + "/dup", pfx + "/dup/", pfx + "/./dup"})
	if len(rows) != 1 {
		t.Fatalf("Rows = %d rows, want 1 (cleaned-path dedup)", len(rows))
	}
}

func TestRowsShape(t *testing.T) {
	rows := Rows([]string{pfx + "/alpha"})
	if len(rows) != 1 {
		t.Fatalf("Rows = %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Name != "alpha" {
		t.Errorf("Name = %q, want alpha", r.Name)
	}
	if r.Title != "[Zoxide] alpha" {
		t.Errorf("Title = %q, want [Zoxide] alpha", r.Title)
	}
	if r.Path != pfx+"/alpha" {
		t.Errorf("Path = %q, want the project root", r.Path)
	}
}

func TestRowsEmpty(t *testing.T) {
	if got := Rows(nil); len(got) != 0 {
		t.Errorf("Rows(nil) = %d rows, want 0", len(got))
	}
}

func TestSplitLines(t *testing.T) {
	got := SplitLines("  /a  \n\n/b\n\t/c\t\n")
	want := []string{"/a", "/b", "/c"}
	if len(got) != len(want) {
		t.Fatalf("SplitLines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SplitLines[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
