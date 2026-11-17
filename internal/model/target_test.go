package model

import "testing"

func TestSavedTargetOwnershipAndNamespaceResolution(t *testing.T) {
	identity := EntityIdentity{LogicalID: "saved"}
	owner := SessionCandidate{Name: "renamed", Identity: EntityIdentity{LogicalID: "saved", RuntimeID: "$2", ServerKey: "server"}}
	collision := SessionCandidate{Name: "original", Identity: EntityIdentity{LogicalID: "unrelated", RuntimeID: "$1", ServerKey: "server"}}
	tests := []struct {
		name       string
		candidates []SessionCandidate
		want       TargetDisposition
	}{
		{"missing target creates", nil, TargetCreate},
		{"renamed owner attaches", []SessionCandidate{owner}, TargetAttach},
		{"unrelated name collision blocks", []SessionCandidate{collision}, TargetNameCollision},
		{"logical owner wins over old name collision", []SessionCandidate{collision, owner}, TargetAttach},
		{"multiple owners block", []SessionCandidate{owner, {Name: "duplicate", Identity: identity}}, TargetConflictingOwners},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveSavedTarget("original", identity, tt.candidates)
			if result.Disposition != tt.want {
				t.Fatalf("disposition=%v, want %v", result.Disposition, tt.want)
			}
			if tt.want == TargetAttach && result.Owner.Identity.RuntimeID != "$2" {
				t.Fatalf("wrong owner: %+v", result.Owner)
			}
		})
	}
}

func TestSessionComparisonDoesNotInferOwnershipFromSharedProject(t *testing.T) {
	id := EntityIdentity{LogicalID: "saved", ProjectPath: "/work"}
	live := EntityIdentity{RuntimeID: "$1", ServerKey: "server", ProjectPath: "/work"}
	if got := id.CompareSession(live); got != IdentityUnknown {
		t.Fatalf("missing link reported as %v", got)
	}
	if !id.SameProject(live) {
		t.Fatal("project membership evidence was lost")
	}
}

func TestContradictoryLogicalIdentityOnSameRuntimeIsConflict(t *testing.T) {
	a := EntityIdentity{LogicalID: "one", ServerKey: "server", RuntimeID: "$1"}
	b := EntityIdentity{LogicalID: "two", ServerKey: "server", RuntimeID: "$1"}
	if got := a.CompareSession(b); got != IdentityConflict {
		t.Fatalf("contradictory runtime ownership = %v", got)
	}
	result := ResolveSavedTarget("work", a, []SessionCandidate{{Name: "work", Identity: b}})
	if result.Disposition != TargetConflictingOwners {
		t.Fatalf("contradictory owner permitted: %+v", result)
	}
}
