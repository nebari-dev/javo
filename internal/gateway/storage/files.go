package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Places GET /api/files can list.
const (
	placeHome  = "home"
	placeGroup = "group:"
)

// Homes lists a folder of a user's home, opens a file of it, frees the home
// for an app and keeps it open while its user is active; files.Lister
// implements it.
type Homes interface {
	List(ctx context.Context, userID, path string) (files.Listing, error)
	// ListGroup lists a folder inside /shared/<dir>, a group folder the
	// caller has been checked to belong to.
	ListGroup(ctx context.Context, userID, dir, path string) (files.Listing, error)
	Open(ctx context.Context, userID, path string) (io.ReadCloser, files.FileInfo, error)
	// Save writes a text file: over version (FileInfo.Version) of it, or as
	// a new file when version is "".
	Save(ctx context.Context, userID, path string, data []byte, version string) (files.FileInfo, error)
	Release(ctx context.Context, userID string) error
	Touch(userID string)
}

// FileEntry is one entry of a home folder.
type FileEntry struct {
	Name     string    `json:"name"`
	Kind     string    `json:"kind" enum:"file,folder"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// FileListing is one folder of the caller's home.
type FileListing struct {
	// Path is relative to the home folder; "" is the home folder itself.
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
	// Count is len(Entries). Truncated means the folder holds more than
	// files.MaxEntries entries and only the first ones are listed.
	Count     int  `json:"count"`
	Truncated bool `json:"truncated"`
	// Stale means this is the last listing the gateway saw, AgeSeconds old,
	// and a fresh one is being fetched: ask again shortly to get it.
	Stale      bool `json:"stale"`
	AgeSeconds int  `json:"ageSeconds"`
}

type filesInput struct {
	Team  string `query:"team" maxLength:"63" doc:"Team whose home to list; needed only when namespaces are per team and the user is in several"`
	Path  string `query:"path" maxLength:"4096" doc:"Folder relative to the place; empty is the place itself"`
	Place string `query:"place" maxLength:"128" doc:"home (default) or group:<dir>, a group folder from GET /api/storage"`
}

type filesOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         FileListing
}

// RegisterFiles adds GET /api/files?path=, listing one folder of the
// signed-in user's home: folders first, then files, each by name ignoring case.
func RegisterFiles(api huma.API, listings *Listings, nss *namespaces.Finder, st *API) {
	var lister Homes
	if listings != nil {
		lister = listings
	}
	registerRawFile(api, lister, nss, st)
	registerSaveFile(api, lister, nss, st)
	registerWarm(api, listings, nss)
	huma.Register(api, huma.Operation{
		OperationID: "listFiles", Method: http.MethodGet, Path: "/api/files",
		Summary: "List a folder of the user's home", Tags: []string{"files"},
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound,
			http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *filesInput) (*filesOutput, error) {
		if listings == nil {
			return nil, problem.Internal("list home folder", errors.New("no home folder lister configured"))
		}
		rel, err := cleanPath(in.Path)
		if err != nil {
			return nil, err
		}
		who := authn.User(ctx)
		user := who.ID
		ns, err := homeNamespace(nss, who, in.Team)
		if err != nil {
			return nil, err
		}
		var (
			listing files.Listing
			age     time.Duration
			stale   bool
		)
		if in.Place != "" && in.Place != placeHome {
			dir, ok := strings.CutPrefix(in.Place, placeGroup)
			if !ok || dir == "" || dir == "." || dir == ".." || strings.ContainsAny(dir, `/\`) {
				return nil, problem.New(http.StatusBadRequest, reasons.Msg(reasons.MsgPlaceInvalid))
			}
			folders, err := st.folders(ctx, ns, user)
			if err != nil {
				return nil, problem.Internal("list shared folders", err)
			}
			i := slices.IndexFunc(folders, func(f StorageFolder) bool { return f.Dir == dir })
			if i < 0 {
				return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgFolderNotFound))
			}
			if f := folders[i]; !f.Ready {
				return nil, problem.Reason(http.StatusConflict, reasons.Code(f.Reason.Code))
			}
			// Group folders are listed fresh: the home's listing cache is the
			// home's alone.
			listing, err = listings.ListGroup(files.WithNamespace(ctx, ns), user, dir, rel)
			if err != nil {
				return nil, HomeError(err, user, "list group folder", rel, reasons.MsgFolderNotFound, reasons.MsgFolderListFailed)
			}
		} else {
			var err error
			listing, age, stale, err = listings.Lookup(ctx, user, ns, rel)
			if err != nil {
				return nil, HomeError(err, user, "list home folder", rel, reasons.MsgFolderNotFound, reasons.MsgFolderListFailed)
			}
		}

		out := &filesOutput{CacheControl: "no-store", Body: FileListing{
			Path: rel, Entries: make([]FileEntry, 0, len(listing.Entries)),
			Count: len(listing.Entries), Truncated: listing.Truncated,
			Stale: stale, AgeSeconds: int(age / time.Second),
		}}
		for _, e := range listing.Entries {
			kind := "file"
			if e.IsDir {
				kind = "folder"
			}
			out.Body.Entries = append(out.Body.Entries, FileEntry{Name: e.Name, Kind: kind, Size: e.Size, Modified: e.ModTime})
		}
		sort.SliceStable(out.Body.Entries, func(i, j int) bool {
			a, b := out.Body.Entries[i], out.Body.Entries[j]
			if a.Kind != b.Kind {
				return a.Kind == "folder"
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		})
		return out, nil
	})
}

type warmInput struct {
	Team string `query:"team" maxLength:"63" doc:"Team whose home to warm up; needed only when namespaces are per team and the user is in several"`
}

// registerWarm adds POST /api/files/warm: pages that may open the file
// picker call it on load, so the home folder is listed before it is needed.
func registerWarm(api huma.API, listings *Listings, nss *namespaces.Finder) {
	huma.Register(api, huma.Operation{
		OperationID: "warmFiles", Method: http.MethodPost, Path: "/api/files/warm",
		Summary: "Get the user's home folder ready to list", Tags: []string{"files"},
		Description: "Starts what lists the home folder and lists it, so the file picker opens at once. " +
			"Answers right away; calls closer together than a few seconds do nothing.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *warmInput) (*struct{}, error) {
		if listings == nil {
			return nil, problem.Internal("warm home folder", errors.New("no home folder lister configured"))
		}
		who := authn.User(ctx)
		ns, err := homeNamespace(nss, who, in.Team)
		if err != nil {
			return nil, err
		}
		listings.Warm(who.ID, ns)
		return nil, nil
	})
}

// homeNamespace is where who's home is: their own namespace, or the team's
// when namespaces are per team.
func homeNamespace(nss *namespaces.Finder, who authz.Principal, team string) (string, error) {
	ns, err := nss.For(who, team)
	if err != nil {
		var r *placement.Refusal
		if errors.As(err, &r) {
			return "", problem.New(http.StatusUnprocessableEntity, reasons.Msg(r.Code))
		}
		return "", problem.Internal("choose namespace", err)
	}
	return ns, nil
}

// HomeError maps a failure of a home call to the API error every files
// endpoint answers with: the home states (starting, busy, owned by another
// user) are the same everywhere, notFound and failed are the endpoint's own
// messages for a missing path and for any other failure.
func HomeError(err error, user, op, rel string, notFound, failed reasons.MessageCode) error {
	switch {
	case errors.Is(err, files.ErrHomeStarting):
		return problem.Reason(http.StatusServiceUnavailable, reasons.HomeStarting).WithHeader("Retry-After", "2")
	case errors.Is(err, files.ErrHomeBusy):
		return problem.Reason(http.StatusConflict, reasons.HomeBusy)
	case errors.Is(err, files.ErrHomeConflict):
		slog.Error("home claim records another owner", "user", user)
		return problem.Reason(http.StatusConflict, reasons.HomeConflict)
	case errors.Is(err, files.ErrNotFound):
		return problem.New(http.StatusNotFound, reasons.Msg(notFound))
	}
	slog.Error(op, "user", user, "path", rel, "err", err)
	return problem.New(http.StatusInternalServerError, reasons.Msg(failed))
}

// rawTypes is the allow-list of content types served inline. Everything
// else, notebooks included, is an octet-stream attachment.
var rawTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".pdf":  "application/pdf",
	".log":  "text/plain; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".json": "text/plain; charset=utf-8",
}

// rawSandbox lets a run's HTML script itself but gives it an opaque origin
// (no allow-same-origin): it cannot read the session or call the API as the
// user, nor reach another user's files.
const rawSandbox = "sandbox allow-scripts allow-popups"

// rawDocumentPolicy is the policy for files a browser runs as a page (HTML,
// SVG). The page sits on the platform's own address, so it could pass for a
// sign-in prompt; it gets no network at all: no fetch or websocket, no form
// post, no external script, style, font, image or frame that could carry what
// the viewer types elsewhere, and no other site may frame it. Inline script
// and data: or blob: resources still work, which is what self-contained
// reports use.
const rawDocumentPolicy = rawSandbox + "; default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval' data: blob:; " +
	"style-src 'unsafe-inline' data:; img-src data: blob:; font-src data:; media-src data: blob:; " +
	"connect-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"

type rawInput struct {
	Team string `query:"team" maxLength:"63" doc:"Team whose home the file is in; needed only when namespaces are per team and the user is in several"`
	Path string `query:"path" maxLength:"4096" doc:"File relative to the home folder"`
}

// registerRawFile adds GET /api/files/raw?path=, streaming one file of the
// signed-in user's home. Files are user content, so every response is
// sandboxed, never sniffed and never cached, so a file cannot run script in
// the platform's origin.
func registerRawFile(api huma.API, lister Homes, nss *namespaces.Finder, st *API) {
	huma.Register(api, huma.Operation{
		OperationID: "getRawFile", Method: http.MethodGet, Path: "/api/files/raw",
		Summary: "Download or show one file of the user's home", Tags: []string{"files"},
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound,
			http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *rawInput) (*huma.StreamResponse, error) {
		if lister == nil {
			return nil, problem.Internal("open home file", errors.New("no home folder lister configured"))
		}
		rel, err := cleanPath(in.Path)
		if err != nil {
			return nil, err
		}
		if rel == "" {
			return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgFileNotFound))
		}
		who := authn.User(ctx)
		user := who.ID
		ns, err := homeNamespace(nss, who, in.Team)
		if err != nil {
			return nil, err
		}
		body, info, err := lister.Open(files.WithNamespace(ctx, ns), user, rel)
		if errors.Is(err, files.ErrTooLarge) {
			return nil, problem.New(http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgOutputTooLarge))
		}
		if err != nil {
			return nil, HomeError(err, user, "open home file", rel, reasons.MsgFileNotFound, reasons.MsgServerError)
		}
		st.audit(ctx, "file.read", rel, ns, int(info.Size))
		typ, inline := rawTypes[strings.ToLower(path.Ext(info.Name))]
		return &huma.StreamResponse{Body: func(hc huma.Context) {
			defer body.Close()
			if inline {
				hc.SetHeader("Content-Type", typ)
			} else {
				hc.SetHeader("Content-Type", "application/octet-stream")
				hc.SetHeader("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
			}
			csp := rawSandbox
			if typ == rawTypes[".html"] || typ == rawTypes[".svg"] {
				csp = rawDocumentPolicy
			}
			hc.SetHeader("Content-Security-Policy", csp)
			hc.SetHeader("X-Content-Type-Options", "nosniff")
			hc.SetHeader("Cross-Origin-Resource-Policy", "same-origin")
			hc.SetHeader("Cache-Control", "private, no-store")
			hc.SetHeader("ETag", etag(info))
			hc.SetHeader("Content-Length", strconv.FormatInt(info.Size, 10))
			hc.SetStatus(http.StatusOK)
			// The status line is already sent, so a broken stream can only be logged.
			if _, err := io.Copy(hc.BodyWriter(), body); err != nil {
				slog.Warn("stream home file", "user", user, "path", rel, "err", err)
			}
		}}, nil
	})
}

// etag is the ETag of info: its version, quoted. A save sends it back in
// If-Match.
func etag(info files.FileInfo) string { return strconv.Quote(info.Version()) }

type saveInput struct {
	Team        string `query:"team" maxLength:"63" doc:"Team whose home the file is in; needed only when namespaces are per team and the user is in several"`
	Path        string `query:"path" maxLength:"4096" doc:"File relative to the home folder"`
	IfMatch     string `header:"If-Match" maxLength:"128" doc:"The ETag of the version this replaces, from GET /api/files/raw"`
	IfNoneMatch string `header:"If-None-Match" maxLength:"8" doc:"* to create a new file"`
	RawBody     []byte `contentType:"text/plain" doc:"The file's new content: UTF-8 text without NUL bytes"`
}

// SavedFile is a file as a save left it.
type SavedFile struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// ETag names this version, for the next save's If-Match.
	ETag string `json:"etag"`
}

type saveOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   SavedFile
}

// registerSaveFile adds PUT /api/files/raw?path=, which writes a text file
// of the signed-in user's home from the file editor. It must name the
// version it replaces (If-Match with the ETag GET /api/files/raw sent) or
// ask for a new file (If-None-Match: *), so a save never overwrites a
// change it has not seen.
func registerSaveFile(api huma.API, lister Homes, nss *namespaces.Finder, st *API) {
	huma.Register(api, huma.Operation{
		OperationID: "saveRawFile", Method: http.MethodPut, Path: "/api/files/raw",
		Summary: "Save a text file of the user's home", Tags: []string{"files"},
		Description: "Writes the body as the file. Send If-Match with the ETag from GET /api/files/raw to replace " +
			"that version (409 if the file changed since), or If-None-Match: * to create a new file (409 if the " +
			"name is taken). Only UTF-8 text of at most 2 MiB; never through a symlink.",
		DefaultStatus: http.StatusOK,
		MaxBodyBytes:  files.MaxEditBytes,
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict,
			http.StatusRequestEntityTooLarge, http.StatusPreconditionRequired, http.StatusUnprocessableEntity,
			http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *saveInput) (*saveOutput, error) {
		if lister == nil {
			return nil, problem.Internal("save home file", errors.New("no home folder lister configured"))
		}
		rel, err := cleanPath(in.Path)
		if err != nil {
			return nil, err
		}
		if rel == "" {
			return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgFileNotFound))
		}
		var version string
		create := in.IfNoneMatch == "*" && in.IfMatch == ""
		if !create {
			v, err := strconv.Unquote(in.IfMatch)
			if err != nil || v == "" || in.IfNoneMatch != "" {
				return nil, problem.New(http.StatusPreconditionRequired, reasons.Msg(reasons.MsgFileVersionNeeded))
			}
			version = v
		}
		who := authn.User(ctx)
		ns, err := homeNamespace(nss, who, in.Team)
		if err != nil {
			return nil, err
		}
		info, err := lister.Save(files.WithNamespace(ctx, ns), who.ID, rel, in.RawBody, version)
		switch {
		case errors.Is(err, files.ErrChanged):
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgFileChanged))
		case errors.Is(err, files.ErrExists):
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgFileExists))
		case errors.Is(err, files.ErrNotText):
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgFileNotText))
		case errors.Is(err, files.ErrTooLarge):
			return nil, problem.New(http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgFileTooLargeToEdit))
		case err != nil:
			return nil, HomeError(err, who.ID, "save home file", rel, reasons.MsgFileNotFound, reasons.MsgServerError)
		}
		action, status := "file.save", http.StatusOK
		if create {
			action, status = "file.create", http.StatusCreated
		}
		st.audit(ctx, action, rel, ns, len(in.RawBody))
		tag := etag(info)
		return &saveOutput{Status: status, ETag: tag, Body: SavedFile{Path: rel, Size: info.Size, Modified: info.ModTime, ETag: tag}}, nil
	})
	// huma requires a RawBody, but an empty file is a file: a new one, or
	// one emptied in the editor. The request body is shared with the
	// registered handler, so this lets an empty body through.
	api.OpenAPI().Paths["/api/files/raw"].Put.RequestBody.Required = false
}

// audit records that the caller read or wrote the file rel of their home.
// The call has already happened, so a failure to record it is logged, not
// answered.
func (s *API) audit(ctx context.Context, action, rel, ns string, size int) {
	who, via := store.ActorStrings(authn.Actor(ctx))
	detail, _ := json.Marshal(map[string]int{"bytes": size})
	err := s.DB.Audit().Append(ctx, store.AuditEvent{
		Actor: who, Via: via, Action: action, ObjectKind: "file", ObjectName: rel,
		ObjectNamespace: ns, ObjectOwner: who, Allowed: true, Detail: detail,
	})
	if err != nil {
		slog.Error("audit file "+action, "user", who, "path", rel, "err", err)
	}
}

// cleanPath is files.CleanPath with its errors as the 400 problem to answer.
func cleanPath(raw string) (string, error) {
	rel, err := files.CleanPath(raw)
	switch {
	case errors.Is(err, files.ErrPathTooDeep):
		return "", problem.New(http.StatusBadRequest, reasons.Msg(reasons.MsgPathTooDeep, files.MaxPathDepth))
	case err != nil:
		return "", problem.New(http.StatusBadRequest, reasons.Msg(reasons.MsgFolderOutsideHome))
	}
	return rel, nil
}
