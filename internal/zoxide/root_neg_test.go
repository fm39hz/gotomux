package zoxide

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeRootNeg counts how often the cache actually saved a walk: every true
// Negative answer is a skipped FindProjectRoot call.
type fakeRootNeg struct {
	neg        map[string]bool
	remembered []string
	hits       int // Negative returned true → walk skipped
	calls      int // Negative consulted
}

func newFakeRootNeg() *fakeRootNeg {
	return &fakeRootNeg{neg: make(map[string]bool)}
}

func (f *fakeRootNeg) Negative(path string) bool {
	f.calls++
	if f.neg[path] {
		f.hits++
		return true
	}
	return false
}

func (f *fakeRootNeg) RememberMiss(path string) {
	f.remembered = append(f.remembered, path)
	f.neg[path] = true
}

// markerlessPaths uses a non-existent prefix outside marked /tmp (same trick
// as zoxide_test.go):
// the walk deterministically exhausts, so these are guaranteed misses.
var markerlessPaths = []string{pfx + "/alpha", pfx + "/beta"}

func rootedTempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "rooted")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRowsCheckedRemembersOnlyTrueMisses(t *testing.T) {
	rooted := rootedTempDir(t)
	paths := append([]string{}, markerlessPaths...)
	paths = append(paths, rooted)

	rc := newFakeRootNeg()
	rows1 := RowsChecked(paths, rc)

	wantMisses := []string{filepath.Clean(pfx + "/alpha"), filepath.Clean(pfx + "/beta")}
	if !reflect.DeepEqual(rc.remembered, wantMisses) {
		t.Fatalf("remembered = %v, want %v (the rooted dir must not count as a miss)", rc.remembered, wantMisses)
	}
	if len(rows1) != len(paths) {
		t.Fatalf("rows = %d, want %d", len(rows1), len(paths))
	}
	for i, row := range rows1[:2] {
		if row.Path != wantMisses[i] {
			t.Errorf("row[%d].Path = %q, want %q", i, row.Path, wantMisses[i])
		}
	}
	// The rooted entry resolves to itself, exactly as without a cache.
	last := rows1[len(rows1)-1]
	if last.Path != rooted || last.Name != "rooted" {
		t.Errorf("rooted row = {%q %q}, want name rooted at its own dir", last.Name, last.Path)
	}
}

func TestRowsCheckedCacheHitSkipsWalkAndMatchesUncached(t *testing.T) {
	rc := newFakeRootNeg()
	rows1 := RowsChecked(markerlessPaths, rc)
	rememberedAfterFirst := len(rc.remembered)

	// Second derive within TTL: every lookup must be served from the cache —
	// zero walks — and produce byte-identical rows to the walked derivation.
	rows2 := RowsChecked(markerlessPaths, rc)
	if !reflect.DeepEqual(rows1, rows2) {
		t.Fatalf("cached rows differ from walked rows:\n%+v\n%+v", rows1, rows2)
	}
	if rc.hits != len(markerlessPaths) {
		t.Fatalf("cache hits = %d, want %d (every path served without a walk)", rc.hits, len(markerlessPaths))
	}
	if len(rc.remembered) != rememberedAfterFirst {
		t.Fatalf("RememberMiss fired on cache hits: %v", rc.remembered)
	}
}

func TestRowsCheckedNilCacheEqualsRows(t *testing.T) {
	paths := append([]string{}, markerlessPaths...)
	paths = append(paths, rootedTempDir(t))
	if got, want := RowsChecked(paths, nil), Rows(paths); !reflect.DeepEqual(got, want) {
		t.Fatalf("RowsChecked(nil) diverged from Rows:\n%+v\n%+v", got, want)
	}
}
