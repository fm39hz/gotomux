package identity

import "testing"

func TestNewReturnsCanonicalUniqueUUIDv4(t *testing.T) {
	seen := make(map[string]bool, 128)
	for range 128 {
		id, err := New()
		if err != nil {
			t.Fatalf("generate ID: %v", err)
		}
		if err := Validate(id); err != nil {
			t.Fatalf("generated ID %q is invalid: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("duplicate generated ID %q", id)
		}
		seen[id] = true
	}
}

func TestValidateRejectsNonCanonicalOrNonV4IDs(t *testing.T) {
	for _, id := range []string{
		"", "not-an-id", "00000000000040008000000000000001",
		"00000000-0000-1000-8000-000000000001",
		"00000000-0000-4000-7000-000000000001",
		"00000000-0000-4000-8000-00000000000A",
	} {
		if err := Validate(id); err == nil {
			t.Errorf("invalid logical ID %q accepted", id)
		}
	}
}
