package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Thumbnails is the app_thumbnails table: the picture an app's owner chose
// for its card. The store keeps whatever bytes it is given; the gateway
// checks and re-encodes an upload (internal/thumbnail) before Set.
type Thumbnails struct{ s *Store }

// Thumbnails returns the app_thumbnails table.
func (s *Store) Thumbnails() *Thumbnails { return &Thumbnails{s} }

// Thumbnail is one stored picture. Digest changes whenever Data does.
type Thumbnail struct {
	ContentType string
	Data        []byte
	Digest      string
}

// thumbnailsTable is what a change to a picture announces; nothing
// reconciles on it, so the controller does no work for a new picture.
const thumbnailsTable = "app_thumbnails"

// Set stores the app's picture, replacing any earlier one. A missing or
// deleted app is ErrNotFound. It is audited.
func (t *Thumbnails) Set(ctx context.Context, actor model.Actor, appID, contentType string, data []byte) error {
	sum := sha256.Sum256(data)
	return t.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		app, err := liveApp(ctx, q, appID)
		if err != nil {
			return effect{}, err
		}
		err = q.UpsertThumbnail(ctx, sqlcgen.UpsertThumbnailParams{
			AppID: appID, ContentType: contentType, Data: data,
			Digest: hex.EncodeToString(sum[:8]), SetAt: dbtime.From(now()),
		})
		return appKind.effect("app.thumbnail_set", app, Change{thumbnailsTable, appID}), err
	})
}

// Remove deletes the app's picture; an app without one is ErrNotFound. It is
// audited.
func (t *Thumbnails) Remove(ctx context.Context, actor model.Actor, appID string) error {
	return t.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		app, err := readApp(ctx, q, appID)
		if err != nil {
			return effect{}, err
		}
		n, err := q.DeleteThumbnail(ctx, appID)
		if err == nil && n == 0 {
			err = ErrNotFound
		}
		return appKind.effect("app.thumbnail_remove", app, Change{thumbnailsTable, appID}), err
	})
}

// Get returns the app's picture; ErrNotFound when it has none.
func (t *Thumbnails) Get(ctx context.Context, appID string) (Thumbnail, error) {
	row, err := t.s.q.GetThumbnail(ctx, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return Thumbnail{}, ErrNotFound
	}
	if err != nil {
		return Thumbnail{}, err
	}
	return Thumbnail{ContentType: row.ContentType, Data: row.Data, Digest: row.Digest}, nil
}

// Digest is the digest of the app's picture without reading the picture;
// empty when it has none.
func (t *Thumbnails) Digest(ctx context.Context, appID string) (string, error) {
	d, err := t.s.q.GetThumbnailDigest(ctx, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return d, err
}
