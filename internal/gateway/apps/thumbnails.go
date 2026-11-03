package apps

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/thumbnail"
)

// Thumbnail is an app's picture as app views carry it.
type Thumbnail struct {
	// URL is where the picture is served; empty when the app has none, and
	// the card shows the app kind's logo instead.
	URL string `json:"url,omitempty"`
}

type thumbnailPutInput struct {
	Name    string `path:"name" maxLength:"63"`
	Owner   string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	RawBody []byte `contentType:"image/png" doc:"A PNG or JPEG image, at most 4 MB and 16 megapixels"`
}

type thumbnailGetInput struct {
	Name        string `path:"name" maxLength:"63"`
	Owner       string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	V           string `query:"v" maxLength:"64" doc:"The picture's digest, from the app view's thumbnail URL; it only keys browser caches"`
	IfNoneMatch string `header:"If-None-Match" maxLength:"128"`
}

// thumbnailSandbox lets the response be nothing but an image: no script,
// no plugin, no frame, even if a browser were asked to render it as a page.
const thumbnailSandbox = "default-src 'none'; img-src 'self'; sandbox"

func (s *Workspaces) registerThumbnails(api huma.API) {
	if !s.thumbnails {
		return
	}
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"workspaces"}
		op.Errors = append(op.Errors, http.StatusUnauthorized, http.StatusNotFound)
		return op
	}
	huma.Register(api, tagged(huma.Operation{
		OperationID: "setWorkspaceThumbnail", Method: http.MethodPut, Path: "/api/workspaces/{name}/thumbnail",
		Summary: "Set the picture shown on the app's card (owner or platform admin)",
		Description: "The image is checked by its bytes, not the Content-Type, decoded and encoded again, " +
			"and shrunk to at most " + strconv.Itoa(thumbnail.MaxSide) + " pixels on its longest side.",
		Errors:       []int{http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity},
		MaxBodyBytes: thumbnail.MaxBytes,
	}), func(ctx context.Context, in *thumbnailPutInput) (*workspaceOutput, error) {
		app, err := s.authorized(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		data, typ, err := thumbnail.Normalize(in.RawBody)
		switch {
		case errors.Is(err, thumbnail.ErrTooLarge):
			return nil, problem.New(http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgThumbnailTooLarge))
		case errors.Is(err, thumbnail.ErrUnsupported):
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgThumbnailNotImage))
		case err != nil:
			return nil, problem.Internal("encode thumbnail", err)
		}
		if err := s.db.Thumbnails().Set(ctx, authn.Actor(ctx), app.ID, typ, data); err != nil {
			return nil, thumbnailStoreError(err)
		}
		return s.output(ctx, app)
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "removeWorkspaceThumbnail", Method: http.MethodDelete, Path: "/api/workspaces/{name}/thumbnail",
		Summary: "Remove the app's picture; its card shows the app kind's logo again",
	}), func(ctx context.Context, in *nameInput) (*workspaceOutput, error) {
		app, err := s.authorized(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		if err := s.db.Thumbnails().Remove(ctx, authn.Actor(ctx), app.ID); err != nil {
			return nil, thumbnailStoreError(err)
		}
		return s.output(ctx, app)
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "getWorkspaceThumbnail", Method: http.MethodGet, Path: "/api/workspaces/{name}/thumbnail",
		Summary: "The app's picture, for anyone who may open the app",
		Responses: map[string]*huma.Response{"200": {
			Description: "The picture",
			Content: map[string]*huma.MediaType{
				"image/png":  {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/jpeg": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
			},
		}},
	}), s.getThumbnail)
}

// getThumbnail serves the picture to whoever may open the app: its owner, a
// platform admin, and the people it is shared with, within its template's
// cap. Anyone else is told the app does not exist.
func (s *Workspaces) getThumbnail(ctx context.Context, in *thumbnailGetInput) (*huma.StreamResponse, error) {
	app, err := s.byName(ctx, &nameInput{Name: in.Name, Owner: in.Owner})
	if err != nil {
		return nil, err
	}
	tpl, err := s.db.Templates().Get(ctx, app.Template)
	var t *model.Template
	switch {
	case err == nil:
		t = &tpl
	case !errors.Is(err, store.ErrNotFound):
		return nil, problem.Internal("read template", err)
	}
	p := authn.User(ctx)
	if !authz.Decide(p, authz.RolesOf(p, s.Roles), authz.ActOpenApp, authz.Object{App: app, Template: t}).Allowed {
		return nil, errNotFound()
	}
	pic, err := s.db.Thumbnails().Get(ctx, app.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgThumbnailNone))
	case err != nil:
		return nil, problem.Internal("read thumbnail", err)
	}
	etag := `"` + pic.Digest + `"`
	return &huma.StreamResponse{Body: func(hc huma.Context) {
		// The stored bytes are always a PNG or JPEG the gateway encoded; the
		// headers keep a browser from reading them as anything else.
		hc.SetHeader("Content-Type", pic.ContentType)
		hc.SetHeader("X-Content-Type-Options", "nosniff")
		hc.SetHeader("Content-Disposition", "inline")
		hc.SetHeader("Content-Security-Policy", thumbnailSandbox)
		hc.SetHeader("Cross-Origin-Resource-Policy", "same-origin")
		// Asked again each time, so a picture stops showing once the app
		// is no longer shared with the viewer.
		hc.SetHeader("Cache-Control", "private, no-cache")
		hc.SetHeader("ETag", etag)
		if in.IfNoneMatch == etag {
			hc.SetStatus(http.StatusNotModified)
			return
		}
		hc.SetHeader("Content-Length", strconv.Itoa(len(pic.Data)))
		hc.SetStatus(http.StatusOK)
		if _, err := hc.BodyWriter().Write(pic.Data); err != nil {
			slog.Warn("write thumbnail", "app", app.ID, "err", err)
		}
	}}, nil
}

func thumbnailStoreError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgThumbnailNone))
	}
	return problem.Internal("store thumbnail", err)
}

// thumbnailOf is the app's picture for its view: nil when thumbnails are
// off, no URL when it has none. The URL names the owner, so it works for
// people the app is shared with, and the digest, so a new picture is a new
// address.
func (s *Workspaces) thumbnailOf(ctx context.Context, app *model.App) *Thumbnail {
	if !s.thumbnails {
		return nil
	}
	digest, err := s.db.Thumbnails().Digest(ctx, app.ID)
	if err != nil {
		slog.Warn("read thumbnail digest", "app", app.ID, "err", err)
		return &Thumbnail{}
	}
	if digest == "" {
		return &Thumbnail{}
	}
	q := url.Values{"owner": {app.Owner}, "v": {digest}}
	return &Thumbnail{URL: "/api/workspaces/" + url.PathEscape(app.Name) + "/thumbnail?" + q.Encode()}
}
