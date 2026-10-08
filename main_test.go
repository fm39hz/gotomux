package main

import (
	"strings"
	"testing"
)

func TestParseReconcileArgs(t *testing.T) {
	name, hard, force, err := parseReconcileArgs([]string{"kho-cong", "--hard"})
	if err != nil || name != "kho-cong" || !hard || force {
		t.Fatalf("parsed name=%q hard=%v force=%v err=%v", name, hard, force, err)
	}
	name, hard, force, err = parseReconcileArgs([]string{"--force", "--hard", "kho-cong"})
	if err != nil || name != "kho-cong" || !hard || !force {
		t.Fatalf("parsed name=%q hard=%v force=%v err=%v", name, hard, force, err)
	}
	if _, _, _, err := parseReconcileArgs([]string{"--force", "kho-cong"}); err == nil {
		t.Fatal("--force without --hard must be rejected")
	}
	if _, _, _, err := parseReconcileArgs([]string{"a", "b"}); err == nil {
		t.Fatal("multiple session names must be rejected")
	}
}

func TestHardReconcileConfirmationDefaultsToNo(t *testing.T) {
	for _, answer := range []string{"", "n\n", "no\n", "anything\n"} {
		if acceptHardReconcile(strings.NewReader(answer)) {
			t.Errorf("answer %q unexpectedly approved hard reconcile", answer)
		}
	}
	for _, answer := range []string{"y\n", "yes\n", "YES\n"} {
		if !acceptHardReconcile(strings.NewReader(answer)) {
			t.Errorf("answer %q did not approve hard reconcile", answer)
		}
	}
}
