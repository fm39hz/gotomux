package reconcile

import "testing"

func TestPolicyValuesAndValidation(t *testing.T) {
	for _, policy := range []Policy{PolicyPreserve, PolicyExact} {
		if err := policy.Validate(); err != nil {
			t.Errorf("policy %q rejected: %v", policy, err)
		}
	}
	if err := Policy("unknown").Validate(); err == nil {
		t.Fatal("unknown policy was accepted")
	}
}

func TestRuntimeRefRequiresGenerationKindAndID(t *testing.T) {
	valid := RuntimeRef{ServerGeneration: "server-1", Kind: ObjectWindow, ID: "@4"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid runtime reference rejected: %v", err)
	}
	for _, ref := range []RuntimeRef{
		{Kind: ObjectWindow, ID: "@4"},
		{ServerGeneration: "server-1", ID: "@4"},
		{ServerGeneration: "server-1", Kind: ObjectWindow},
		{ServerGeneration: "server-1", Kind: "unknown", ID: "@4"},
		{ServerGeneration: "server-1", Kind: ObjectWindow, ID: "@abc"},
	} {
		if err := ref.Validate(); err == nil {
			t.Errorf("invalid runtime reference accepted: %+v", ref)
		}
	}
}
