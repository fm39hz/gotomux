package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestConfigSchemaGolden pins schema/config.json to the shape GenerateFileSchema
// derives from fileConfig. Adding or renaming a config key without regenerating
// turns this red — the docs-drift gate from docs/_improvements.md P3.1.
//
// GOTOMUX_UPDATE_SCHEMA=1 rewrites the golden file (that is what `make schema`
// runs); otherwise the comparison is byte-for-byte.
func TestConfigSchemaGolden(t *testing.T) {
	got := GenerateFileSchema()

	path := filepath.Join("..", "..", "schema", "config.json")
	if os.Getenv("GOTOMUX_UPDATE_SCHEMA") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("schema/config.json missing (%v); regenerate with: make schema", err)
	}
	if !bytes.Equal(want, got) {
		at := firstDiff(want, got)
		t.Fatalf("schema/config.json is stale (first difference at byte %d);\nregenerate and commit: make schema", at)
	}
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
