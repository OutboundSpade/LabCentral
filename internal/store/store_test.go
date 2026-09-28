package store

import (
	"context"
	"testing"
)

func TestAllocationClaimsRemainDistinctAcrossOwners(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	a, e := s.Reserve(ctx, "vmid", "lab-a", 100, 104, 2)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Reserve(ctx, "vmid", "lab-b", 100, 104, 2)
	if e != nil {
		t.Fatal(e)
	}
	if a[0] == b[0] || a[1] == b[1] {
		t.Fatal("overlap")
	}
	again, e := s.Reserve(ctx, "vmid", "lab-a", 100, 104, 2)
	if e != nil || again[0] != a[0] {
		t.Fatal("reservation is not stable")
	}
	if _, e = s.Reserve(ctx, "vmid", "lab-c", 100, 104, 2); e == nil {
		t.Fatal("exhaustion not enforced")
	}
	if e = s.Release(ctx, "lab-a"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reserve(ctx, "vmid", "lab-c", 100, 104, 2); e != nil {
		t.Fatal(e)
	}
}

func TestRelativeStatePathWithURICharacters(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := Open("state ?#plus+")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Put(context.Background(), "test", "one", "value"); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = s.Get(context.Background(), "test", "one", &got); err != nil || got != "value" {
		t.Fatalf("%q %v", got, err)
	}
}
