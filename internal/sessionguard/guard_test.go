package sessionguard

import "testing"

func TestGuardSerializesOneIncarnationAndReleases(t *testing.T) {
	key := t.TempDir()
	release, err := Acquire(key, "$1")
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Acquire(key, "$1"); err == nil {
		_ = other()
		t.Fatal("concurrent operation acquired same incarnation")
	}
	other, err := Acquire(key, "$2")
	if err != nil {
		t.Fatal(err)
	}
	_ = other()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	again, err := Acquire(key, "$1")
	if err != nil {
		t.Fatal(err)
	}
	_ = again()
}
