package speaker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// addProfile enrolls name with one deterministic embedding row.
func addProfile(t *testing.T, store *Enrollment, name string) Profile {
	t.Helper()
	p, err := store.Add(context.Background(), name, "rev-1", [][]float32{{0.1, 0.2, 0.3, 0.4}})
	if err != nil {
		t.Fatalf("add %q: %v", name, err)
	}
	return p
}

// TestOwnerIsTheFirstProfileOnly locks the rule the whole identity stack reads:
// one owner, claimed by the first voice on the machine, never invented later.
func TestOwnerIsTheFirstProfileOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Owner(); ok {
		t.Fatal("an empty store must have no owner")
	}
	if err := store.SetOwner("nobody"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("SetOwner on an unenrolled name = %v, want ErrNotEnrolled", err)
	}

	lance := addProfile(t, store, "Lance")
	if !lance.Owner {
		t.Fatal("the first enrolled profile must be the owner")
	}
	alex := addProfile(t, store, "Alex")
	if alex.Owner {
		t.Fatal("a second profile must not claim ownership")
	}
	if owner, ok := store.Owner(); !ok || owner.Name != "Lance" {
		t.Fatalf("owner = %+v (%v), want Lance", owner, ok)
	}

	// Re-enrolling refreshes embeddings, not identity, in both directions.
	if again := addProfile(t, store, "lance"); !again.Owner {
		t.Fatal("re-enrolling the owner must keep ownership")
	}
	if again := addProfile(t, store, "Alex"); again.Owner {
		t.Fatal("re-enrolling a guest must not promote it")
	}

	// Ownership survives a reopen: it is stored, not recomputed from order.
	// (The display name follows the last spelling enrolled — Add's rule.)
	reopened, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := reopened.Owner()
	if !ok || !strings.EqualFold(owner.Name, "lance") {
		t.Fatalf("owner after reload = %+v (%v), want lance", owner, ok)
	}
}

// TestSetOwnerMovesTheFlag proves exactly one profile can own the machine.
func TestSetOwnerMovesTheFlag(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	addProfile(t, store, "Lance")
	addProfile(t, store, "Alex")

	if err := store.SetOwner("Alex"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	owners := 0
	for _, p := range store.List() {
		if p.Owner {
			owners++
			if p.Name != "Alex" {
				t.Errorf("owner = %q, want Alex", p.Name)
			}
		}
	}
	if owners != 1 {
		t.Fatalf("%d owners, want exactly 1", owners)
	}
	// Idempotent, and persisted.
	if err := store.SetOwner("alex"); err != nil {
		t.Fatalf("SetOwner is not idempotent: %v", err)
	}
	reopened, err := OpenEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if owner, ok := reopened.Owner(); !ok || owner.Name != "Alex" {
		t.Fatalf("owner after reload = %+v (%v), want Alex", owner, ok)
	}
}

// TestRemoveOwnerPromotesNobody: an emptied seat stays empty. Auto-promoting
// the remaining voice would silently hand owner_verify to someone else.
func TestRemoveOwnerPromotesNobody(t *testing.T) {
	store, err := OpenEnrollment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addProfile(t, store, "Lance")
	addProfile(t, store, "Alex")

	if err := store.Remove("Lance"); err != nil {
		t.Fatal(err)
	}
	if owner, ok := store.Owner(); ok {
		t.Fatalf("owner after removing the owner = %+v, want none", owner)
	}
	// And a later enrollment does not inherit it either, while a guest remains.
	if again := addProfile(t, store, "Lance"); again.Owner {
		t.Fatal("re-enrolling into a store that already has profiles must not claim ownership")
	}
}
