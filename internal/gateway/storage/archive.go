package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
)

type archiveInput struct {
	Team    string `query:"team" maxLength:"63" doc:"Team whose home to upload into; needed only when namespaces are per team and the user is in several"`
	Path    string `query:"path" maxLength:"4096" doc:"Folder relative to the home folder, for example deploys/sales; made if missing"`
	Replace bool   `query:"replace" doc:"true: if the folder exists, replace its files with the archive's (a redeploy). Otherwise an existing folder is a 409"`

	// body is the request body, read as it arrives (Resolve); length is its
	// Content-Length, -1 when unknown.
	body   io.Reader
	length int64
}

// Resolve keeps the request body for the handler to stream, instead of
// huma reading it all into memory first.
func (in *archiveInput) Resolve(hc huma.Context) []error {
	r, _ := humago.Unwrap(hc)
	in.body, in.length = r.Body, r.ContentLength
	return nil
}

// UploadedFolder is the folder an upload wrote.
type UploadedFolder struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes" doc:"The files' bytes together, unpacked"`
}

type archiveOutput struct {
	Status int
	Body   UploadedFolder
}

// registerArchiveUpload adds PUT /api/files/archive?path=, which unpacks a
// gzipped tar of a folder (as tar czf makes it) into a folder of the
// signed-in user's home: how a coding agent on a laptop deploys a folder.
// The body is streamed to the file pod, never held whole, and is at most
// uploadBytes, as are the files it unpacks to.
func registerArchiveUpload(api huma.API, lister Homes, nss *namespaces.Finder, st *API) {
	huma.Register(api, huma.Operation{
		OperationID: "uploadArchive", Method: http.MethodPut, Path: "/api/files/archive",
		Summary: "Upload a folder to the user's home as a tar.gz", Tags: []string{"files"},
		Description: "Unpacks the body, a gzipped tar (Content-Type: application/gzip), into the folder path. " +
			"201 for a new folder, 200 when replace=true replaced one, 409 if the folder exists without replace=true. " +
			"Only plain files and folders: absolute paths, .., links and devices fail the whole upload (422), " +
			"as does a body or unpacked total over the server's limit (413). A failed upload leaves the folder as it was.",
		DefaultStatus: http.StatusCreated,
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
			"application/gzip": {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}},
		}},
		Errors: []int{http.StatusForbidden, http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *archiveInput) (*archiveOutput, error) {
		if err := st.member(ctx); err != nil {
			return nil, err
		}
		if lister == nil {
			return nil, problem.Internal("upload home folder", errors.New("no home folder lister configured"))
		}
		rel, err := cleanPath(in.Path)
		if err != nil {
			return nil, err
		}
		if rel == "" {
			return nil, problem.New(http.StatusBadRequest, reasons.Msg(reasons.MsgUploadNeedsFolder))
		}
		limit := st.UploadBytes
		tooLarge := problem.New(http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgUploadTooLarge, byteSize(limit)))
		if in.length > limit {
			return nil, tooLarge
		}
		who := authn.User(ctx)
		ns, err := homeNamespace(nss, who, in.Team)
		if err != nil {
			return nil, err
		}
		body := &capReader{r: in.body, left: limit}
		out, err := lister.Unpack(files.WithNamespace(ctx, ns), who.ID, rel, body, limit, in.Replace)
		var bad *files.BadEntryError
		switch {
		case errors.Is(err, files.ErrTooLarge):
			return nil, tooLarge
		case errors.Is(err, files.ErrExists):
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgFolderExists))
		case errors.As(err, &bad):
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgArchiveEntryRefused, bad.Name))
		case errors.Is(err, files.ErrBadArchive):
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgArchiveInvalid))
		case errors.Is(err, files.ErrEmptyArchive):
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgArchiveEmpty))
		case err != nil:
			return nil, HomeError(err, who.ID, "upload home folder", rel, reasons.MsgFolderNotFound, reasons.MsgServerError)
		}
		st.audit(ctx, "file.upload", rel, ns, int(out.Bytes))
		status := http.StatusCreated
		if out.Replaced {
			status = http.StatusOK
		}
		return &archiveOutput{Status: status, Body: UploadedFolder{Path: out.Path, Files: out.Files, Bytes: out.Bytes}}, nil
	})
}

// capReader reads r up to left bytes; one more is files.ErrTooLarge.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, files.ErrTooLarge
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	if c.left -= int64(n); c.left < 0 {
		return n + int(c.left), files.ErrTooLarge
	}
	return n, err
}

// byteSize is n bytes for a person: whole MiB when it is, bytes otherwise.
func byteSize(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
