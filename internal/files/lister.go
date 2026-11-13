// Package files lists the folders of a user's home for the file picker,
// opens files from it and saves the files edited in the UI.
//
// The gateway cannot mount home claims itself, so each user gets a small file
// pod that runs `rclone rcd` as that user, with their home mounted writable
// and their group folders read-only. The gateway asks it for folder listings
// and files over the pod network. A file pod is created on the
// first request and deleted after it has been idle for a while.
//
// Where homes are volumes of a Docker daemon (the Docker executor) the
// file pod is a container: Helpers runs the same rclone, from the same
// plan, through an executor, and the same Lister and Rclone talk to it.
//
// Where homes are folders of the machine Javo runs on (the process
// executor) there is no file pod: the same Lister reads them through
// Direct, and Folders stands in for the file pods.
package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	roleFiles = model.RoleFiles

	rcPort = 5572
	// rcUser and the env vars rclone reads its remote control user and
	// password from.
	rcUser    = "javo"
	envRCUser = "RCLONE_RC_USER"
	envRCPass = "RCLONE_RC_PASS"
	// annotationLogin holds the pod's rclone password sealed with the
	// gateway's key (PodsConfig.Seal); passwordKey is its key in the pod's
	// password Secret, named <pod>-rc-<16 hex>.
	annotationLogin = model.Group + "/rclone-login"
	passwordKey     = "password"
	dataDir         = podstorage.HomeDir
	scratchDir      = podstorage.ScratchDir
	defaultIdle     = 10 * time.Minute
	reapEvery       = time.Minute
	// MaxEntries is the most entries one folder listing returns.
	MaxEntries = 5000
	// maxListing bounds how much of an rclone answer is read: MaxEntries
	// entries of typical size (rclone repeats the full path in each entry).
	maxListing = 8 << 20
	// MaxOpenBytes is the largest file Open serves (50 MiB).
	MaxOpenBytes = 50 << 20
	// MaxEditBytes is the largest file Save writes (2 MiB). The gateway holds
	// the whole text in memory to check it before writing a byte, and a
	// browser editor slows down well before text this long; bigger files are
	// data, edited in an app.
	MaxEditBytes = 2 << 20
)

// Error values are internal; user-facing copy lives in internal/reasons.
var (
	// ErrOutsideHome means the requested path leaves the home folder.
	ErrOutsideHome = errors.New("files: path is outside the home folder")
	// ErrPathTooDeep means the requested path has more than MaxPathDepth
	// names.
	ErrPathTooDeep = errors.New("files: path has too many folders")
	// ErrNotFound means the folder does not exist (or is not a real folder).
	ErrNotFound = errors.New("files: folder not found")
	// ErrTooLarge means the file is bigger than MaxOpenBytes, or an upload
	// bigger than its limit.
	ErrTooLarge = errors.New("files: file is too large to open")
	// ErrChanged means the file is not the version the caller read: it
	// was written since.
	ErrChanged = errors.New("files: file changed since it was read")
	// ErrExists means a new file was asked for where something already is.
	ErrExists = errors.New("files: a file or folder of that name exists")
	// ErrNotText means the content to save is not UTF-8 text.
	ErrNotText = errors.New("files: content is not text")
	// ErrHomeStarting means the home or its file pod is not ready yet; retry
	// shortly.
	ErrHomeStarting = errors.New("file pod is starting")
	// ErrHomeBusy means the home is attached to another node (ReadWriteOnce).
	ErrHomeBusy = errors.New("home is attached to another node")
	// ErrHomeConflict means the home claim records a different owner (two
	// owners' slugs collide); it is never mounted for this user.
	ErrHomeConflict = errors.New("files: home claim belongs to another owner")
	// ErrForeignPod means a pod with the file pod's name is not this user's
	// file pod; it is left untouched.
	ErrForeignPod = errors.New("files: pod with the file pod name is not this user's file pod")
)

// Entry is one item of a folder listing.
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// Listing is one folder's entries, cut at MaxEntries (or at the byte cap).
type Listing struct {
	Entries   []Entry
	Truncated bool
}

// MaxPathDepth is the most names a path may have. Each folder on a path
// costs one stat call to the file pod, so this bounds what one call costs.
const MaxPathDepth = 64

// CleanPath turns a requested path into a clean path relative to the home
// folder ("" is the home folder itself). Paths that climb above it fail with
// ErrOutsideHome.
func CleanPath(raw string) (string, error) {
	c, err := insideHome(raw)
	if err == nil && strings.Count(c, "/") >= MaxPathDepth {
		return "", ErrPathTooDeep
	}
	return c, err
}

// insideHome is CleanPath without the limit on depth, for the paths the
// Lister itself builds below one it was given (an archive's entries).
func insideHome(raw string) (string, error) {
	c := path.Clean(strings.TrimLeft(raw, "/"))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", ErrOutsideHome
	}
	if c == "." {
		return "", nil
	}
	return c, nil
}

// Remote reads the home and group folders mounted in the file pod at addr
// (host:port, with user:password@ in front when rclone asks for a login).
// fs is the directory a call is about: the home mount or a group folder's.
type Remote interface {
	// List lists remote, a path inside fs.
	List(ctx context.Context, addr, fs, remote string) (Listing, error)
	// Open streams one file; remote is relative to the home mount.
	Open(ctx context.Context, addr, remote string) (io.ReadCloser, error)
	// Stat describes one entry inside fs without symlink following for the
	// last name; a missing entry gives ErrNotFound.
	Stat(ctx context.Context, addr, fs, remote string) (Entry, error)
	// FindFile returns the real file called name in folder dir of the home,
	// however many entries the folder holds; ErrNotFound if there is none.
	FindFile(ctx context.Context, addr, dir, name string) (Entry, error)
	// Upload writes data as the file name in folder dir of the home.
	Upload(ctx context.Context, addr, dir, name string, data []byte) error
	// UploadStream is Upload of what r holds, read as it is sent.
	UploadStream(ctx context.Context, addr, dir, name string, r io.Reader) error
	// MoveDir moves folder src of the home to dst, which should not exist.
	MoveDir(ctx context.Context, addr, src, dst string) error
	// Purge deletes folder dir of the home and everything in it.
	Purge(ctx context.Context, addr, dir string) error
	// About reports the space used on the volume the home is on.
	About(ctx context.Context, addr string) (Usage, error)
}

// Usage is the space used and the size of the volume under the home, in bytes.
type Usage struct {
	Used, Total int64
}

// HomeUsage is what the gateway reports for a home. Used and Total are set
// only when Enforced: where the storage does not hold the home to its size,
// the volume's numbers describe a disk shared with others and are not shown.
type HomeUsage struct {
	Enforced    bool
	Used, Total int64
}

// FileInfo describes a file returned by Lister.Open.
type FileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Version names this content of the file: its size and modification time
// to the nanosecond. Any write changes the modification time, so a save
// that names the version it read fails if the file was written since.
func (f FileInfo) Version() string {
	return strconv.FormatInt(f.Size, 10) + "-" + strconv.FormatInt(f.ModTime.UnixNano(), 10)
}

func infoOf(e Entry) FileInfo { return FileInfo{Name: e.Name, Size: e.Size, ModTime: e.ModTime} }

// Rclone calls the rclone remote control API.
type Rclone struct {
	// HTTP is the client to use; nil means one with a 10 second timeout.
	HTTP *http.Client
}

// post sends one rclone remote control call; args name the fs. The caller
// closes the body.
func (r Rclone) post(ctx context.Context, addr, op string, args map[string]any) (*http.Response, error) {
	hc := r.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/"+op, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(hc, req, op)
}

// do sends one rclone remote control request: a 404 is ErrNotFound, any
// other answer but 200 an error carrying the start of rclone's message.
func do(hc *http.Client, req *http.Request, op string) (*http.Response, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rclone %s: %w", op, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, ErrNotFound
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return nil, fmt.Errorf("rclone %s: status %d: %s", op, resp.StatusCode, data)
}

// Upload writes data as the file name in folder dir of the home mount, with
// operations/uploadfile. rclone writes over an existing file in place (its
// mode and owner stay) and follows a symlink it meets, so callers check the
// path first (see Lister.Save).
func (r Rclone) Upload(ctx context.Context, addr, dir, name string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return r.UploadStream(ctx, addr, dir, name, bytes.NewReader(data))
}

// UploadStream is Upload of what body holds: it is copied into the request
// as rclone reads it, so a large file is never held in memory.
func (r Rclone) UploadStream(ctx context.Context, addr, dir, name string, body io.Reader) error {
	hc := r.HTTP
	if hc == nil {
		// No overall timeout: a large file takes as long as it takes. The
		// call is bounded by the request context.
		hc = &http.Client{}
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		part, err := mw.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, body)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	// However the request ends, the writer above stops at its next write,
	// and it has stopped reading body before this returns: the caller may
	// read body's state or close it next.
	defer func() {
		pr.Close()
		<-copied
	}()
	q := url.Values{"fs": {dataDir}, "remote": {dir}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/operations/uploadfile?"+q.Encode(), pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := do(hc, req, "operations/uploadfile")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// MoveDir moves folder src of the home mount to dst with sync/move, which
// renames it when it can. Empty folders left behind in src are removed.
func (r Rclone) MoveDir(ctx context.Context, addr, src, dst string) error {
	resp, err := r.post(ctx, addr, "sync/move", map[string]any{
		"srcFs": dataDir + "/" + src, "dstFs": dataDir + "/" + dst, "deleteEmptySrcDirs": true,
	})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Purge deletes folder dir of the home mount and all it holds, with
// operations/purge.
func (r Rclone) Purge(ctx context.Context, addr, dir string) error {
	resp, err := r.post(ctx, addr, "operations/purge", map[string]any{"fs": dataDir, "remote": dir})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// list calls operations/list on remote inside fs, with extra request fields
// (nil for none).
func (r Rclone) list(ctx context.Context, addr, fs, remote string, extra map[string]any) (Listing, error) {
	args := map[string]any{"fs": fs, "remote": remote}
	for k, v := range extra {
		args[k] = v
	}
	resp, err := r.post(ctx, addr, "operations/list", args)
	if err != nil {
		return Listing{}, err
	}
	defer resp.Body.Close()
	out, err := decodeListing(resp.Body, maxListing)
	if err != nil {
		return Listing{}, fmt.Errorf("rclone operations/list: %w", err)
	}
	return out, nil
}

// List calls operations/list for remote (relative to the mount fs). It
// stops at MaxEntries entries or maxListing bytes and marks the result
// Truncated.
func (r Rclone) List(ctx context.Context, addr, fs, remote string) (Listing, error) {
	return r.list(ctx, addr, fs, remote, nil)
}

// About calls operations/about for the home mount: the used and total bytes
// of the volume under it.
func (r Rclone) About(ctx context.Context, addr string) (Usage, error) {
	resp, err := r.post(ctx, addr, "operations/about", map[string]any{"fs": dataDir})
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Used  int64 `json:"used"`
		Total int64 `json:"total"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return Usage{}, fmt.Errorf("rclone operations/about: %w", err)
	}
	return Usage{Used: out.Used, Total: out.Total}, nil
}

// Stat calls operations/stat for remote. A name that does not exist gives
// ErrNotFound. stat does not follow a symlink named last (it reports a
// non-folder), but it does follow symlinked folders earlier in the path, so
// callers check each folder on the way.
func (r Rclone) Stat(ctx context.Context, addr, fs, remote string) (Entry, error) {
	resp, err := r.post(ctx, addr, "operations/stat", map[string]any{"fs": fs, "remote": remote})
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Item *Entry `json:"item"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return Entry{}, fmt.Errorf("rclone operations/stat: %w", err)
	}
	if out.Item == nil {
		return Entry{}, ErrNotFound
	}
	return *out.Item, nil
}

// globEscaper escapes the characters rclone filter rules treat as patterns.
var globEscaper = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`)

// FindFile returns the file called name in folder dir (relative to the home
// mount) without listing the whole folder: the listing asks rclone for that
// one name. Like every listing it skips symlinks, so a symlink is not found.
// A folder, a missing name or a different name gives ErrNotFound.
func (r Rclone) FindFile(ctx context.Context, addr, dir, name string) (Entry, error) {
	l, err := r.list(ctx, addr, dataDir, dir, map[string]any{
		"opt":     map[string]any{"filesOnly": true},
		"_filter": map[string]any{"IncludeRule": []string{globEscaper.Replace(name)}},
	})
	if err != nil {
		return Entry{}, err
	}
	i := slices.IndexFunc(l.Entries, func(e Entry) bool { return !e.IsDir && e.Name == name })
	if i < 0 {
		return Entry{}, ErrNotFound
	}
	return l.Entries[i], nil
}

// Open serves remote from the rclone object server (rcd --rc-serve). A file
// that does not exist gives ErrNotFound. The caller closes the body.
func (r Rclone) Open(ctx context.Context, addr, remote string) (io.ReadCloser, error) {
	hc := r.HTTP
	if hc == nil {
		// No overall timeout: a large file takes as long as it takes. The
		// header wait is bounded by the request context.
		hc = &http.Client{}
	}
	u, err := url.Parse("http://" + addr)
	if err != nil {
		return nil, fmt.Errorf("rclone serve: %w", err)
	}
	u.Path = "/[" + dataDir + "]/" + remote
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rclone serve: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("rclone serve: status %d: %s", resp.StatusCode, data)
	}
	return resp.Body, nil
}

// decodeListing streams {"list":[...]} one entry at a time, reading at most
// limit bytes. If the cap is reached before the list closes, the cut can fall
// anywhere (inside an entry, between entries, before "]"), so running out of
// input once the cap is reached means truncated: the entries read so far
// are returned without an error.
func decodeListing(r io.Reader, limit int64) (Listing, error) {
	lr := &io.LimitedReader{R: r, N: limit}
	out := Listing{Entries: []Entry{}}
	err := decodeList(json.NewDecoder(lr), &out)
	if lr.N <= 0 && (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)) {
		out.Truncated = true
		return out, nil
	}
	return out, err
}

func decodeList(dec *json.Decoder, out *Listing) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		if key != "list" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return err
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return err
		}
		for dec.More() {
			if len(out.Entries) == MaxEntries {
				out.Truncated = true
				return nil
			}
			var e Entry
			if err := dec.Decode(&e); err != nil {
				return err
			}
			out.Entries = append(out.Entries, e)
		}
		// The list is complete only once its "]" is read.
		return expectDelim(dec, ']')
	}
	return errors.New("answer has no list")
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != want {
		return fmt.Errorf("unexpected %v, want %v", t, want)
	}
	return nil
}

// HomePods returns the address of a user's ready file pod and deletes it
// when an app needs the home; Pods implements it. Users are named by
// their full ID.
type HomePods interface {
	Ensure(ctx context.Context, userID string) (string, error)
	// EnsureGroup is Ensure for a pod that also mounts the group folder dir.
	EnsureGroup(ctx context.Context, userID, dir string) (string, error)
	Release(ctx context.Context, userID string) error
	Touch(userID string)
}

// Lister lists folders of a user's home.
type Lister struct {
	pods   HomePods
	remote Remote
	// saving serializes saves of one file, so two saves of the same version
	// cannot both pass the version check. Keyed by user and path, so one
	// person's slow saves never hold up another's.
	saving fileLocks
}

// fileLocks holds one lock per key while someone holds or waits for it.
type fileLocks struct {
	mu    sync.Mutex
	locks map[string]*fileLock
}

type fileLock struct {
	held  chan struct{} // a value in it means the lock is taken
	users int           // holders and waiters
}

// lock takes the lock for key, or gives up with ctx's error when ctx ends
// first. The returned func releases it.
func (f *fileLocks) lock(ctx context.Context, key string) (func(), error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]*fileLock{}
	}
	l := f.locks[key]
	if l == nil {
		l = &fileLock{held: make(chan struct{}, 1)}
		f.locks[key] = l
	}
	l.users++
	f.mu.Unlock()
	done := func() {
		f.mu.Lock()
		if l.users--; l.users == 0 {
			delete(f.locks, key)
		}
		f.mu.Unlock()
	}
	select {
	case l.held <- struct{}{}:
		return func() { <-l.held; done() }, nil
	case <-ctx.Done():
		done()
		return nil, ctx.Err()
	}
}

// NewLister returns a Lister that reaches homes through pods and remote.
func NewLister(pods HomePods, remote Remote) *Lister {
	return &Lister{pods: pods, remote: remote}
}

// List returns the entries of rel, a path from CleanPath.
//
// rclone skips symlinks when listing a folder but follows one that is named
// directly, so a symlink in the home could otherwise list the file pod's own
// filesystem. Each folder on the path is therefore checked with stat, which
// reports a symlink as a non-folder. A swap between two of these calls is not
// prevented; it could only list names in the file pod's container, which holds
// no secrets.
func (l *Lister) List(ctx context.Context, userID, rel string) (Listing, error) {
	return l.list(ctx, userID, dataDir, "", rel)
}

// ListGroup lists rel, a path from CleanPath, inside the folder of a group
// the file pod mounts at /shared/<dir> (dir is naming.GroupDir of the group).
// The caller has checked that the user is in the group and the folder is ready.
func (l *Lister) ListGroup(ctx context.Context, userID, dir, rel string) (Listing, error) {
	return l.list(ctx, userID, path.Join(podstorage.SharedRoot, dir), dir, rel)
}

// list lists rel inside fs; group is the folder fs is, or "" for the home.
func (l *Lister) list(ctx context.Context, userID, fs, group, rel string) (Listing, error) {
	addr, err := l.pods.EnsureGroup(ctx, userID, group)
	if err != nil {
		return Listing{}, err
	}
	if err := l.checkFolders(ctx, addr, fs, rel, false); err != nil {
		return Listing{}, err
	}
	return l.remote.List(ctx, addr, fs, rel)
}

// checkFolders fails with ErrNotFound unless every folder on rel (inside fs)
// is a real folder (not a symlink). Stat asks about one name, so a folder past
// the listing cap of its parent is still found. With missingOK the end of rel
// may not exist yet (rclone makes it when it writes there); the folders that
// do exist are still checked.
func (l *Lister) checkFolders(ctx context.Context, addr, fs, rel string, missingOK bool) error {
	if rel == "" {
		return nil
	}
	at := ""
	for _, part := range strings.Split(rel, "/") {
		at = path.Join(at, part)
		e, err := l.remote.Stat(ctx, addr, fs, at)
		if missingOK && errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !e.IsDir {
			return ErrNotFound
		}
	}
	return nil
}

// Open streams the file at rel, a non-empty path from CleanPath.
//
// rclone serves a symlink named directly but never lists one, so the file
// must be found by a listing of its folder (FindFile), the folders on the way
// must be real folders (checkFolders), and it must not exceed MaxOpenBytes. At
// most MaxOpenBytes are read even if the file grows after the check.
func (l *Lister) Open(ctx context.Context, userID, rel string) (io.ReadCloser, FileInfo, error) {
	dir, name := path.Split(rel)
	if name == "" {
		return nil, FileInfo{}, ErrNotFound
	}
	addr, err := l.pods.Ensure(ctx, userID)
	if err != nil {
		return nil, FileInfo{}, err
	}
	dir = strings.TrimSuffix(dir, "/")
	if err := l.checkFolders(ctx, addr, dataDir, dir, false); err != nil {
		return nil, FileInfo{}, err
	}
	e, err := l.remote.FindFile(ctx, addr, dir, name)
	if err != nil {
		return nil, FileInfo{}, err
	}
	if e.Size > MaxOpenBytes {
		return nil, FileInfo{}, ErrTooLarge
	}
	body, err := l.remote.Open(ctx, addr, rel)
	if err != nil {
		return nil, FileInfo{}, err
	}
	capped := struct {
		io.Reader
		io.Closer
	}{io.LimitReader(body, MaxOpenBytes), body}
	return capped, infoOf(e), nil
}

// Save writes data as the file at rel, a non-empty path from CleanPath, and
// returns what the file is now. With version (from FileInfo.Version) it
// replaces that file and fails with ErrChanged if the file is no longer
// that version; with version "" it creates the file and fails with
// ErrExists if anything has that name. data must be UTF-8 text without NUL
// bytes (ErrNotText) of at most MaxEditBytes (ErrTooLarge).
//
// Saves of one file through this Lister run one at a time. A write that
// lands between the version check and the upload (an app, or a save
// through another gateway replica) is still overwritten: the window is the
// one call to rclone.
//
// rclone writes through a symlink, so the path is checked as Open checks
// it: the folders on the way must be real folders, the file a real file
// (FindFile) or, for a new file, no entry at all (Stat, which sees
// symlinks). A symlink swapped in between the check and the write is not
// prevented; through it the write reaches only what the file pod can write,
// the home and its own empty scratch folders.
func (l *Lister) Save(ctx context.Context, userID, rel string, data []byte, version string) (FileInfo, error) {
	if len(data) > MaxEditBytes {
		return FileInfo{}, ErrTooLarge
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return FileInfo{}, ErrNotText
	}
	dir, name := path.Split(rel)
	if name == "" {
		return FileInfo{}, ErrNotFound
	}
	addr, err := l.pods.Ensure(ctx, userID)
	if err != nil {
		return FileInfo{}, err
	}
	dir = strings.TrimSuffix(dir, "/")
	unlock, err := l.saving.lock(ctx, userID+"\x00"+rel)
	if err != nil {
		return FileInfo{}, err
	}
	defer unlock()
	if err := l.checkFolders(ctx, addr, dataDir, dir, false); err != nil {
		return FileInfo{}, err
	}
	if version == "" {
		_, err := l.remote.Stat(ctx, addr, dataDir, rel)
		if err == nil {
			return FileInfo{}, ErrExists
		}
		if !errors.Is(err, ErrNotFound) {
			return FileInfo{}, err
		}
	} else {
		e, err := l.remote.FindFile(ctx, addr, dir, name)
		if err != nil {
			return FileInfo{}, err
		}
		if infoOf(e).Version() != version {
			return FileInfo{}, ErrChanged
		}
	}
	if err := l.remote.Upload(ctx, addr, dir, name, data); err != nil {
		return FileInfo{}, err
	}
	e, err := l.remote.FindFile(ctx, addr, dir, name)
	if err != nil {
		return FileInfo{}, err
	}
	return infoOf(e), nil
}

// Touch keeps the user's file pod, if any, for another idle period.
func (l *Lister) Touch(userID string) { l.pods.Touch(userID) }

// Usage reports how full the user's home is, but only when sizeEnforced (the
// home's status.sizeEnforced): otherwise it answers without starting a file
// pod and without numbers.
func (l *Lister) Usage(ctx context.Context, userID string, sizeEnforced bool) (HomeUsage, error) {
	if !sizeEnforced {
		return HomeUsage{}, nil
	}
	addr, err := l.pods.Ensure(ctx, userID)
	if err != nil {
		return HomeUsage{}, err
	}
	u, err := l.remote.About(ctx, addr)
	if err != nil {
		return HomeUsage{}, err
	}
	return HomeUsage{Enforced: true, Used: u.Used, Total: u.Total}, nil
}

// Release frees the user's home for an app by deleting the file pod.
func (l *Lister) Release(ctx context.Context, userID string) error {
	return l.pods.Release(ctx, userID)
}

type nsKey struct{}

// WithNamespace says which namespace the user's home is in for calls made
// with the returned context; without it Pods uses PodsConfig.Namespace.
func WithNamespace(ctx context.Context, ns string) context.Context {
	return context.WithValue(ctx, nsKey{}, ns)
}

// NamespaceOf is the namespace WithNamespace put in ctx.
func NamespaceOf(ctx context.Context) (string, bool) {
	ns, ok := ctx.Value(nsKey{}).(string)
	return ns, ok && ns != ""
}

// UserIDs gives the uid and gid of an owner's pods; ids.ErrNoUID means none
// is allocated yet.
type UserIDs interface {
	UID(ctx context.Context, ownerID string) (uid, gid int64, err error)
}

// Storage resolves the storage input of an owner's pods and makes a missing
// home; podstorage.Provisioner is the implementation. The file pod mounts all
// of it read-only but the home.
type Storage interface {
	Input(ctx context.Context, namespace, owner string) (podstorage.Input, error)
	// Home returns the owner's home, asking for it as an app does when there
	// is none.
	Home(ctx context.Context, namespace, owner string) (model.Home, error)
}

// namespaceOr is the namespace WithNamespace put in ctx, else fallback.
func namespaceOr(ctx context.Context, fallback string) string {
	if ns, ok := NamespaceOf(ctx); ok {
		return ns
	}
	return fallback
}

// held reports whether h is being restored or moved to other storage.
func held(h *model.Home) bool {
	return h != nil && (meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionRestoring) ||
		meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionMigrating))
}

// fileSpec is the plan of what serves owner's files, called name: rclone
// running as the owner (in.UID and in.GID) with the owner's storage (the
// layout podstorage builds for in) mounted read-only, except the home,
// which Lister.Save writes to. Whoever runs it adds rclone's password
// (envRCPass) as a value no description of the workload holds.
func fileSpec(name, userSlug, namespace, image string, in podstorage.Input) workload.Spec {
	in.ReadOnly = true
	in.ScratchMax = resource.MustParse("64Mi")
	s := workload.Spec{
		Ref: workload.Ref{Namespace: namespace, Name: name, Owner: userSlug},
		Main: workload.Unit{
			Name:  "rclone",
			Image: image,
			// rclone asks every call for its own password, so the home
			// stays closed even where the network does not keep everyone
			// but the gateway out.
			Args: []string{"rcd", fmt.Sprintf("--rc-addr=:%d", rcPort), "--rc-serve"},
			Env: []workload.Env{
				{Name: envRCUser, Value: rcUser},
				{Name: "HOME", Value: scratchDir},
				{Name: "RCLONE_CONFIG", Value: scratchDir + "/rclone.conf"},
				{Name: "RCLONE_CACHE_DIR", Value: scratchDir + "/cache"},
			},
			ReadOnlyRoot: true,
			// rclone listens within milliseconds of starting, so the check
			// runs at once and every second: the picker waits on it.
			Ready: &workload.Check{TCP: true, Every: time.Second},
		},
		Port: rcPort,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
		},
	}
	layout := podstorage.Build(userSlug, in)
	for i := range layout.Volumes {
		if v := &layout.Volumes[i]; v.Name == podstorage.HomeVolume {
			v.ReadOnly = false
		}
	}
	for i := range layout.Mounts {
		if m := &layout.Mounts[i]; m.Volume == podstorage.HomeVolume {
			m.ReadOnly = false
		}
	}
	layout.AddTo(&s)
	return s
}

// newPassword is 256 random bits, URL-safe.
func newPassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails
	return base64.RawURLEncoding.EncodeToString(b)
}
