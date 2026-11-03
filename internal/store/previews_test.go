package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// previewOf is the row the preview reconciler writes for pull request number
// of parent, at the head commit sha.
func previewOf(parent model.App, number int32, sha string) *model.App {
	return &model.App{
		Namespace: parent.Namespace, Name: naming.PreviewName(parent.Name, number), Owner: parent.Owner,
		Template: parent.Template, Size: parent.Size, ParentID: parent.ID, PullNumber: number,
		Source: &model.Source{URL: "https://github.com/example-org/site", Ref: sha},
	}
}

func putPreview(t *testing.T, s *store.Store, parent model.App, number int32, sha string) model.App {
	t.Helper()
	child, err := s.Previews().Put(ctx, previewOf(parent, number, sha))
	if err != nil {
		t.Fatalf("put preview %d: %v", number, err)
	}
	return child
}

func TestPutRefusesARowWithoutAParent(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.Previews().Put(ctx, newApp("alice", "plain")); !errors.Is(err, store.ErrNotPreview) {
		t.Fatalf("Put without parent = %v, want ErrNotPreview", err)
	}
	orphan := previewOf(model.App{ID: "missing", Name: "site", Owner: "alice", Namespace: "javo-apps", Template: "jupyterlab"}, 1, "aaaa")
	if _, err := s.Previews().Put(ctx, orphan); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Put under a missing parent = %v, want ErrNotFound", err)
	}
	if all, _ := s.Apps().ListAll(ctx); len(all) != 0 {
		t.Fatalf("a refused Put left rows: %v", names(all))
	}
}

func TestPutKeepsOneRowPerPullRequestAndBumpsOnANewSHA(t *testing.T) {
	s, _ := startedStore(t)
	parent := createApp(t, s, "alice", "site")
	first := putPreview(t, s, parent, 12, "aaaa")
	if first.Name != "site--pr-12" || first.Version != 1 || first.Source.Ref != "aaaa" {
		t.Fatalf("created = %+v", first)
	}
	drain(s)

	same, err := s.Previews().Put(ctx, previewOf(parent, 12, "aaaa"))
	if err != nil || same.ID != first.ID || same.Version != 1 {
		t.Fatalf("same SHA: %+v, %v; want the same row at version 1", same, err)
	}
	requireNoChange(t, s, "a Put that changed nothing")

	moved, err := s.Previews().Put(ctx, previewOf(parent, 12, "bbbb"))
	if err != nil || moved.ID != first.ID || moved.Version != 2 || moved.Source.Ref != "bbbb" {
		t.Fatalf("new SHA: %+v, %v; want the same row at version 2 on bbbb", moved, err)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != first.ID {
		t.Fatalf("change = %+v", c)
	}

	kids, err := s.Previews().Children(ctx, parent.ID)
	if err != nil || len(kids) != 1 {
		t.Fatalf("Children = %v, %v; want one row", names(kids), err)
	}
	putPreview(t, s, parent, 13, "cccc")
	if kids, _ = s.Previews().Children(ctx, parent.ID); len(kids) != 2 {
		t.Fatalf("Children = %v, want two", names(kids))
	}
	other := createApp(t, s, "alice", "other")
	if kids, _ = s.Previews().Children(ctx, other.ID); len(kids) != 0 {
		t.Fatalf("another app's Children = %v", names(kids))
	}
}

func TestPutAuditsAsTheController(t *testing.T) {
	s := storetest.New(t)
	parent := createApp(t, s, "alice", "site")
	putPreview(t, s, parent, 1, "aaaa")
	events, _ := s.Audit().List(ctx, 10)
	if len(events) < 2 || events[0].Actor != model.SystemController || events[0].Action != "app.create" || events[0].ObjectName != "site--pr-1" {
		t.Fatalf("newest audit event = %+v", events[0])
	}
}

func TestRemoveMarksOnlyPreviewRows(t *testing.T) {
	s := storetest.New(t)
	parent := createApp(t, s, "alice", "site")
	child := putPreview(t, s, parent, 3, "aaaa")
	if err := s.Previews().Remove(ctx, parent.ID); !errors.Is(err, store.ErrNotPreview) {
		t.Fatalf("Remove of an ordinary app = %v, want ErrNotPreview", err)
	}
	if got, _ := s.Apps().Get(ctx, parent.ID); !got.DeletedAt.IsZero() {
		t.Fatal("Remove marked an ordinary app deleted")
	}
	if err := s.Previews().Remove(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Apps().Get(ctx, child.ID); got.DeletedAt.IsZero() {
		t.Fatal("preview not marked deleted")
	}
	if err := s.Previews().Remove(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Remove of a missing row = %v, want ErrNotFound", err)
	}
	// A PR that reopens before the purge cannot take the row's name back.
	if _, err := s.Previews().Put(ctx, previewOf(parent, 3, "bbbb")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("Put over a deleted, unpurged preview = %v, want ErrExists", err)
	}
}

func TestRecordSyncIsNotAnnouncedAndIsBounded(t *testing.T) {
	s, _ := startedStore(t)
	parent := createApp(t, s, "alice", "site")
	drain(s)
	if _, _, err := s.Previews().Sync(ctx, parent.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Sync before any sync = %v, want ErrNotFound", err)
	}

	items := make([]model.PreviewItem, 60)
	for i := range items {
		items[i] = model.PreviewItem{Number: int32(i + 1), Title: strings.Repeat("é", 300), Reason: "PreviewLimitReached"}
	}
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if err := s.Previews().RecordSync(ctx, parent.ID, model.PreviewSync{Items: items, Reason: "PreviewsUnavailable", Message: "boom"}, at); err != nil {
		t.Fatal(err)
	}
	requireNoChange(t, s, "RecordSync")

	got, gotAt, err := s.Previews().Sync(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != model.MaxPreviewItems || got.Items[0].Number != 1 || got.Reason != "PreviewsUnavailable" || got.Message != "boom" {
		t.Fatalf("sync = %d items, %+v", len(got.Items), got)
	}
	if n := len([]rune(got.Items[0].Title)); n != model.MaxPreviewTitleLength {
		t.Fatalf("title kept %d characters, want %d", n, model.MaxPreviewTitleLength)
	}
	if !gotAt.Equal(at) {
		t.Fatalf("synced at %v, want %v", gotAt, at)
	}
	// Recording again replaces the result.
	if err := s.Previews().RecordSync(ctx, parent.ID, model.PreviewSync{}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = s.Previews().Sync(ctx, parent.ID); len(got.Items) != 0 || got.Reason != "" {
		t.Fatalf("second sync = %+v", got)
	}
}

func TestWebhookHookSecretAndPoke(t *testing.T) {
	s, _ := startedStore(t)
	parent := createApp(t, s, "alice", "site")
	if _, _, err := s.Previews().ByHook(ctx, "unknown"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ByHook of an unknown id = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Previews().ByHook(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ByHook of an empty id = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Previews().SetHook(ctx, alice, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetHook of a missing app = %v, want ErrNotFound", err)
	}

	if set, err := s.Previews().HasHook(ctx, parent.ID); err != nil || set {
		t.Fatalf("HasHook before SetHook = %v, %v, want false", set, err)
	}
	hookID, secret, err := s.Previews().SetHook(ctx, alice, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := s.Previews().HasHook(ctx, parent.ID); err != nil || !set {
		t.Fatalf("HasHook after SetHook = %v, %v, want true", set, err)
	}
	// 16 and 32 random bytes, base64url without padding.
	if len(hookID) != 22 || len(secret) != 43 || strings.ContainsAny(hookID+secret, "+/=") {
		t.Fatalf("hook id %q (%d), secret %q (%d)", hookID, len(hookID), secret, len(secret))
	}
	gotParent, gotSecret, err := s.Previews().ByHook(ctx, hookID)
	if err != nil || gotParent != parent.ID || string(gotSecret) != secret {
		t.Fatalf("ByHook = %q, %q, %v", gotParent, gotSecret, err)
	}

	if got, err := s.Previews().PokedAt(ctx, parent.ID); err != nil || !got.IsZero() {
		t.Fatalf("PokedAt before any delivery = %v, %v, want the zero time", got, err)
	}
	if got, err := s.Previews().PokedAt(ctx, "missing"); err != nil || !got.IsZero() {
		t.Fatalf("PokedAt of an app without a row = %v, %v, want the zero time", got, err)
	}
	drain(s)
	at := time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)
	if err := s.Previews().Poke(ctx, hookID, at); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "app_previews" || c.ID != parent.ID {
		t.Fatalf("poke change = %+v, want app_previews/%s", c, parent.ID)
	}
	if got, err := s.Previews().PokedAt(ctx, parent.ID); err != nil || !got.Equal(at) {
		t.Fatalf("PokedAt = %v, %v, want %v", got, err, at)
	}
	if err := s.Previews().Poke(ctx, "unknown", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Poke of an unknown hook = %v, want ErrNotFound", err)
	}

	// A new secret replaces the old hook completely.
	newID, newSecret, err := s.Previews().SetHook(ctx, alice, parent.ID)
	if err != nil || newID == hookID || newSecret == secret {
		t.Fatalf("rotated hook = %q, %q, %v", newID, newSecret, err)
	}
	if _, _, err := s.Previews().ByHook(ctx, hookID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old hook still resolves: %v", err)
	}

	// The secret is in the table for the signature check and nowhere else.
	events, _ := s.Audit().List(ctx, 100)
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), newSecret) {
		t.Fatal("a webhook secret reached the audit log")
	}
	if len(events) == 0 || events[0].Action != "app.preview_hook_set" {
		t.Fatalf("newest audit event = %+v", events[0])
	}
}

// A webhook made before its app (in the New app form) is stored with the app,
// in one transaction: a refused app leaves no hook behind, and the address
// resolves only once the app exists.
func TestCreateFromRepoStoresTheWebhookWithTheApp(t *testing.T) {
	s := storetest.New(t)
	hookID, secret, err := store.NewHook()
	if err != nil || len(hookID) != 22 || len(secret) != 43 {
		t.Fatalf("NewHook = %q, %q, %v", hookID, secret, err)
	}
	in := newApp("alice", "site")
	in.Source = &model.Source{URL: "https://github.com/example-org/site"}
	app, err := s.Apps().CreateFromRepo(ctx, alice, in, store.RepoSecrets{HookID: hookID, HookSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if got, gotSecret, err := s.Previews().ByHook(ctx, hookID); err != nil || got != app.ID || string(gotSecret) != secret {
		t.Fatalf("ByHook = %q, %q, %v", got, gotSecret, err)
	}
	if _, _, has, _ := s.GitTokens().Pending(ctx, app.ID); has {
		t.Fatal("an app made without a token has one")
	}

	// The same address for another app is refused, and that app is not stored.
	other := newApp("alice", "docs")
	other.Source = &model.Source{URL: "https://github.com/example-org/docs"}
	if _, err := s.Apps().CreateFromRepo(ctx, alice, other, store.RepoSecrets{HookID: hookID, HookSecret: secret}); err == nil {
		t.Fatal("a second app took the same webhook address")
	}
	if _, err := s.Apps().GetByOwnerName(ctx, "alice", "docs"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused app was stored: %v", err)
	}
	events, _ := s.Audit().List(ctx, 100)
	if raw, _ := json.Marshal(events); strings.Contains(string(raw), secret) || strings.Contains(string(raw), hookID) {
		t.Fatal("the webhook reached the audit log")
	}
}

func TestPurgingTheParentRemovesItsPreviewTables(t *testing.T) {
	s := storetest.New(t)
	parent := createApp(t, s, "alice", "site")
	hookID, _, err := s.Previews().SetHook(ctx, alice, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().MarkDeleted(ctx, alice, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, alice, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Previews().ByHook(ctx, hookID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hook of a purged app = %v, want ErrNotFound", err)
	}
}

func TestPutRacingTheParentsDeletionLeavesNoLivePreview(t *testing.T) {
	s := storetest.New(t)
	for i := 0; i < 40; i++ {
		parent := createApp(t, s, "alice", fmt.Sprintf("site-%d", i))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = s.Previews().Put(ctx, previewOf(parent, 1, "aaaa"))
		}()
		go func() {
			defer wg.Done()
			_ = s.Apps().MarkDeleted(ctx, alice, parent.ID)
		}()
		wg.Wait()
		kids, err := s.Previews().Children(ctx, parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range kids {
			if k.DeletedAt.IsZero() {
				t.Fatalf("round %d: preview %s is live under a deleted parent", i, k.Name)
			}
		}
	}
}
