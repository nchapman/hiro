package uidpool

import (
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	p := New(10000, 10000, 3)

	uid1, gid1, err := p.Acquire("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if uid1 != 10000 || gid1 != 10000 {
		t.Fatalf("expected uid=10000 gid=10000, got uid=%d gid=%d", uid1, gid1)
	}

	uid2, _, err := p.Acquire("session-2")
	if err != nil {
		t.Fatal(err)
	}
	if uid2 != 10001 {
		t.Fatalf("expected uid=10001, got %d", uid2)
	}

	if p.InUse() != 2 {
		t.Fatalf("expected 2 in use, got %d", p.InUse())
	}

	// Release first, acquire again — should reuse UID 10000.
	p.Release("session-1")
	if p.InUse() != 1 {
		t.Fatalf("expected 1 in use after release, got %d", p.InUse())
	}

	uid3, _, err := p.Acquire("session-3")
	if err != nil {
		t.Fatal(err)
	}
	if uid3 != 10000 {
		t.Fatalf("expected reused uid=10000, got %d", uid3)
	}
}

func TestExhaustion(t *testing.T) {
	p := New(10000, 10000, 2)

	if _, _, err := p.Acquire("s1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Acquire("s2"); err != nil {
		t.Fatal(err)
	}

	_, _, err := p.Acquire("s3")
	if err == nil {
		t.Fatal("expected error on exhausted pool")
	}
}

func TestReleaseUnknown(t *testing.T) {
	p := New(10000, 10000, 2)
	// Should not panic.
	p.Release("nonexistent")
}

func TestDoubleRelease(t *testing.T) {
	p := New(10000, 10000, 2)

	p.Acquire("s1")
	p.Release("s1")
	p.Release("s1") // should not panic

	if p.InUse() != 0 {
		t.Fatalf("expected 0 in use, got %d", p.InUse())
	}
}

func TestGroupGID(t *testing.T) {
	p := New(10000, 10000, 2)

	// Default: unknown group returns 0.
	if gid := p.GroupGID("hiro-coordinators"); gid != 0 {
		t.Fatalf("expected 0, got %d", gid)
	}

	// Set and retrieve.
	p.SetGroupGID("hiro-coordinators", 10001)
	if gid := p.GroupGID("hiro-coordinators"); gid != 10001 {
		t.Fatalf("expected 10001, got %d", gid)
	}

	// Different group.
	p.SetGroupGID("custom-group", 20000)
	if gid := p.GroupGID("custom-group"); gid != 20000 {
		t.Fatalf("expected 20000, got %d", gid)
	}
}
