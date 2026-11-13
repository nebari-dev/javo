package store_test

import (
	"bytes"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// A workload's sealed bundle is kept under its namespace and name with its
// notes, and replaced in place by the same workload.
func TestBundlePutGetReplaceDelete(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	if _, found, err := s.Bundles().Get(ctx, "javo", "alice-sales.app-git"); found || err != nil {
		t.Fatalf("Get before Put = %v, %v", found, err)
	}
	first := store.SealedBundle{WorkloadID: "app-1", Owner: "alice", Meta: map[string]string{"generation": "3"}, Sealed: []byte("sealed-1")}
	if ok, err := s.Bundles().Put(ctx, "javo", "alice-sales.app-git", first); !ok || err != nil {
		t.Fatalf("Put = %v, %v", ok, err)
	}
	got, found, err := s.Bundles().Get(ctx, "javo", "alice-sales.app-git")
	if err != nil || !found {
		t.Fatalf("Get = %v, %v", found, err)
	}
	if got.WorkloadID != "app-1" || got.Owner != "alice" || got.Meta["generation"] != "3" || len(got.Meta) != 1 || !bytes.Equal(got.Sealed, []byte("sealed-1")) {
		t.Fatalf("Get = %+v", got)
	}
	// The same name in another namespace is another bundle.
	if _, found, _ := s.Bundles().Get(ctx, "team-a", "alice-sales.app-git"); found {
		t.Fatal("the bundle is found in another namespace")
	}

	second := store.SealedBundle{WorkloadID: "app-1", Owner: "alice", Meta: map[string]string{"generation": "4"}, Sealed: []byte("sealed-2")}
	if ok, err := s.Bundles().Put(ctx, "javo", "alice-sales.app-git", second); !ok || err != nil {
		t.Fatalf("second Put = %v, %v", ok, err)
	}
	if got, _, _ := s.Bundles().Get(ctx, "javo", "alice-sales.app-git"); got.Meta["generation"] != "4" || !bytes.Equal(got.Sealed, []byte("sealed-2")) {
		t.Fatalf("after replace = %+v", got)
	}

	if err := s.Bundles().Delete(ctx, "javo", "alice-sales.app-git", "app-1"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.Bundles().Get(ctx, "javo", "alice-sales.app-git"); found || err != nil {
		t.Fatalf("after Delete = %v, %v", found, err)
	}
	// Deleting what is not there is not an error.
	if err := s.Bundles().Delete(ctx, "javo", "alice-sales.app-git", "app-1"); err != nil {
		t.Fatal(err)
	}
}

// A bundle kept for one workload is never replaced or deleted for another.
func TestBundleOfAnotherWorkloadIsLeftAlone(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	theirs := store.SealedBundle{WorkloadID: "app-2", Owner: "bob", Meta: map[string]string{"k": "theirs"}, Sealed: []byte("theirs")}
	if ok, err := s.Bundles().Put(ctx, "javo", "alice-sales.app-api", theirs); !ok || err != nil {
		t.Fatalf("Put = %v, %v", ok, err)
	}
	mine := store.SealedBundle{WorkloadID: "app-1", Owner: "alice", Sealed: []byte("mine")}
	if ok, err := s.Bundles().Put(ctx, "javo", "alice-sales.app-api", mine); ok || err != nil {
		t.Fatalf("Put over another workload's bundle = %v, %v", ok, err)
	}
	if err := s.Bundles().Delete(ctx, "javo", "alice-sales.app-api", "app-1"); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.Bundles().Get(ctx, "javo", "alice-sales.app-api")
	if err != nil || !found || got.WorkloadID != "app-2" || got.Owner != "bob" || got.Meta["k"] != "theirs" || !bytes.Equal(got.Sealed, []byte("theirs")) {
		t.Fatalf("their bundle is now %+v, %v, %v", got, found, err)
	}
}

// Rotation stores a bundle's values sealed again only while the row still
// holds what it read: a bundle replaced meanwhile keeps its newer values,
// and nothing else in the row changes.
func TestBundleResealLosesToANewerBundle(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	first := store.SealedBundle{WorkloadID: "app-1", Owner: "alice", Meta: map[string]string{"generation": "3"}, Sealed: []byte("sealed-1")}
	if ok, err := s.Bundles().Put(ctx, "javo", "alice-sales.app-git", first); !ok || err != nil {
		t.Fatalf("Put = %v, %v", ok, err)
	}
	other := store.SealedBundle{WorkloadID: "app-2", Owner: "bob", Sealed: []byte("theirs")}
	if ok, err := s.Bundles().Put(ctx, "team-a", "bob-sales.app-api", other); !ok || err != nil {
		t.Fatalf("Put = %v, %v", ok, err)
	}
	all, err := s.Bundles().AllSealed(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("AllSealed = %d rows, %v", len(all), err)
	}
	if got := all[0]; got.Namespace != "javo" || got.Name != "alice-sales.app-git" || got.WorkloadID != "app-1" || !bytes.Equal(got.Sealed, []byte("sealed-1")) {
		t.Fatalf("AllSealed[0] is %s/%s of %s", got.Namespace, got.Name, got.WorkloadID)
	}

	// The workload stores new values between the rotation's read and its write.
	second := store.SealedBundle{WorkloadID: "app-1", Owner: "alice", Meta: map[string]string{"generation": "4"}, Sealed: []byte("sealed-2")}
	if ok, err := s.Bundles().Put(ctx, "javo", "alice-sales.app-git", second); !ok || err != nil {
		t.Fatalf("second Put = %v, %v", ok, err)
	}
	if ok, err := s.Bundles().Reseal(ctx, "javo", "alice-sales.app-git", []byte("sealed-1"), []byte("resealed")); ok || err != nil {
		t.Fatalf("reseal over a newer bundle: %v, %v", ok, err)
	}
	if got, _, _ := s.Bundles().Get(ctx, "javo", "alice-sales.app-git"); !bytes.Equal(got.Sealed, []byte("sealed-2")) {
		t.Fatal("a stale reseal replaced the newer bundle")
	}
	if ok, err := s.Bundles().Reseal(ctx, "javo", "alice-sales.app-git", []byte("sealed-2"), []byte("resealed")); !ok || err != nil {
		t.Fatalf("reseal: %v, %v", ok, err)
	}
	got, _, _ := s.Bundles().Get(ctx, "javo", "alice-sales.app-git")
	if got.WorkloadID != "app-1" || got.Owner != "alice" || got.Meta["generation"] != "4" || !bytes.Equal(got.Sealed, []byte("resealed")) {
		t.Fatalf("after reseal = %s of %s, meta %v", got.WorkloadID, got.Owner, got.Meta)
	}
	// The same bytes under another name are not that row's to replace.
	if ok, err := s.Bundles().Reseal(ctx, "javo", "bob-sales.app-api", []byte("theirs"), []byte("resealed")); ok || err != nil {
		t.Fatalf("reseal in another namespace: %v, %v", ok, err)
	}
	if got, _, _ := s.Bundles().Get(ctx, "team-a", "bob-sales.app-api"); !bytes.Equal(got.Sealed, []byte("theirs")) {
		t.Fatal("another row changed")
	}
}
