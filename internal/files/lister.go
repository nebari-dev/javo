// Package files lists the folders of a user's home for the file picker,
// opens files from it and saves the files edited in the UI.
//
// The gateway cannot mount home claims itself, so each user gets a small file
// pod that runs `rclone rcd` as that user, with their home mounted writable
// and their group folders read-only. The gateway asks it for folder listings
// and files over the pod network. A file pod is created on the
// first request and deleted after it has been idle for a while.
package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/status"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
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
	// ErrTooLarge means the file is bigger than MaxOpenBytes.
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
	c := path.Clean(strings.TrimLeft(raw, "/"))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", ErrOutsideHome
	}
	if c == "." {
		return "", nil
	}
	if strings.Count(c, "/") >= MaxPathDepth {
		return "", ErrPathTooDeep
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
	hc := r.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	q := url.Values{"fs": {dataDir}, "remote": {dir}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/operations/uploadfile?"+q.Encode(), &body)
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
	if err := l.checkFolders(ctx, addr, fs, rel); err != nil {
		return Listing{}, err
	}
	return l.remote.List(ctx, addr, fs, rel)
}

// checkFolders fails with ErrNotFound unless every folder on rel (inside fs)
// is a real folder (not a symlink). Stat asks about one name, so a folder past
// the listing cap of its parent is still found.
func (l *Lister) checkFolders(ctx context.Context, addr, fs, rel string) error {
	if rel == "" {
		return nil
	}
	at := ""
	for _, part := range strings.Split(rel, "/") {
		at = path.Join(at, part)
		e, err := l.remote.Stat(ctx, addr, fs, at)
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
	if err := l.checkFolders(ctx, addr, dataDir, dir); err != nil {
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
	if err := l.checkFolders(ctx, addr, dataDir, dir); err != nil {
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

// PodsConfig configures NewPods.
type PodsConfig struct {
	// Client must read from the API server, not a cache: Ensure reads a pod
	// right after creating it and lists events by involvedObject.name.
	Client client.Client
	// Namespace is where homes are unless a call's context says otherwise
	// (WithNamespace).
	Namespace string
	// ReapIn lists the namespaces Reap sweeps; nil means only Namespace.
	ReapIn func(ctx context.Context) ([]string, error)
	// ReapAcrossNamespaces makes Reap list file pods cluster-wide in one call
	// and keep those in the ReapIn namespaces, for when there is a namespace
	// per person. It needs a cluster-wide grant on pods.
	ReapAcrossNamespaces bool
	// Image is the rclone image, pinned by digest.
	Image string
	// Seal seals each file pod's rclone password into an annotation of the
	// pod, so the gateway (which cannot read Secrets in workspace namespaces)
	// can read it back after a restart or from another replica; the
	// container gets it from a Secret, never from the pod spec. Required:
	// the sessions key, shared by all replicas.
	Seal tink.AEAD
	// UIDs gives the uid and gid the owner's apps run as; the file pod runs
	// as them too, so a file it writes belongs to the owner. Required;
	// ids.People is the implementation.
	UIDs UserIDs
	// Storage resolves what a file pod mounts: the owner's home (its current
	// claim), group folders and co-location, as for the owner's apps.
	// Required.
	Storage Storage
	// IdleAfter is how long a file pod lives after its user was last
	// active (Ensure or Touch); zero means 10 minutes.
	IdleAfter time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
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

// Pods creates, reports on and reaps file pods.
type Pods struct {
	cfg     PodsConfig
	mu      sync.Mutex
	lastUse map[string]time.Time
}

// NewPods returns a file pod manager. Call Run to delete idle pods.
func NewPods(cfg PodsConfig) (*Pods, error) {
	if cfg.Client == nil || cfg.Namespace == "" || cfg.Image == "" || cfg.Storage == nil || cfg.UIDs == nil || cfg.Seal == nil {
		return nil, errors.New("files: client, namespace, image, storage, uids and seal are required")
	}
	if cfg.IdleAfter == 0 {
		cfg.IdleAfter = defaultIdle
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Pods{cfg: cfg, lastUse: map[string]time.Time{}}, nil
}

// ns is the namespace of the home a call is about.
func (p *Pods) ns(ctx context.Context) string {
	if ns, ok := NamespaceOf(ctx); ok {
		return ns
	}
	return p.cfg.Namespace
}

func podName(userSlug string) string { return model.FilesPodPrefix + userSlug }

// Ensure returns "<user>:<password>@<podIP>:5572", the pod's rclone login
// and address, when the user's file pod is ready. Otherwise it asks for the
// user's home if they have none (as starting an app does), creates the pod
// once the home is prepared, and returns ErrHomeStarting, ErrHomeBusy or
// ErrHomeConflict. The home claim (the one the user's home records, after a
// mode change the new one) must record userID as its owner, checked on every
// call because a colliding slug shares the pod name.
func (p *Pods) Ensure(ctx context.Context, userID string) (string, error) {
	return p.EnsureGroup(ctx, userID, "")
}

// EnsureGroup is Ensure for a pod that also mounts the group folder dir (""
// for none). A pod that exists without that mount, because the folder became
// ready or the user joined the group after it was created, is deleted and
// ErrHomeStarting returned so the next call creates it with the folder.
func (p *Pods) EnsureGroup(ctx context.Context, userID, dir string) (string, error) {
	p.Touch(userID)

	c, ns := p.cfg.Client, p.ns(ctx)
	home, err := p.cfg.Storage.Home(ctx, ns, userID)
	if err != nil {
		return "", err
	}
	if cd := meta.FindStatusCondition(home.Status.Conditions, model.ConditionReady); cd != nil && cd.Reason == string(reasons.HomeClaimForeign) {
		return "", ErrHomeConflict
	}
	if home.Status.ClaimName == "" {
		// The storage controller is making the claim.
		return "", ErrHomeStarting
	}
	in, err := p.cfg.Storage.Input(ctx, ns, userID)
	if err != nil {
		return "", err
	}
	if in.Home == nil {
		return "", ErrHomeStarting
	}
	var claim corev1.PersistentVolumeClaim
	err = c.Get(ctx, client.ObjectKey{Namespace: ns, Name: home.Status.ClaimName}, &claim)
	if apierrors.IsNotFound(err) {
		return "", ErrHomeStarting
	}
	if err != nil {
		return "", err
	}
	if claim.Annotations[model.AnnotationOwnerID] != userID {
		return "", ErrHomeConflict
	}

	uid, gid, err := p.cfg.UIDs.UID(ctx, userID)
	if errors.Is(err, ids.ErrNoUID) {
		return "", ErrHomeStarting
	}
	if err != nil {
		return "", err
	}
	in.UID, in.GID = uid, gid

	var pod corev1.Pod
	err = c.Get(ctx, client.ObjectKey{Namespace: ns, Name: podName(userID)}, &pod)
	if held(in.Home) {
		// A restore or a move waits for the owner's pods to go, the file pod
		// too: it writes to the home.
		if err == nil && isFilePodOf(&pod, userID) {
			if err := deleteExact(ctx, c, &pod); err != nil {
				return "", err
			}
		}
		return "", ErrHomeBusy
	}
	if apierrors.IsNotFound(err) {
		if !in.Home.Status.Prepared {
			// As for an app: no new pod mounts a home its one-time Job has
			// not readied yet.
			return "", ErrHomeStarting
		}
		return "", p.create(ctx, buildPod(userID, ns, p.cfg.Image, in))
	}
	if err != nil {
		return "", err
	}
	if !isFilePodOf(&pod, userID) {
		return "", ErrForeignPod
	}
	if pod.DeletionTimestamp != nil {
		return "", ErrHomeStarting
	}
	login := rcloneLogin(p.cfg.Seal, &pod)
	// A pod without a password (made by an earlier version) is replaced
	// too: its rclone answers anyone who reaches it.
	if login == nil || !writesAs(&pod, uid) || !hasFolders(&pod, in, dir) {
		if err := deleteExact(ctx, c, &pod); err != nil {
			return "", err
		}
		return "", ErrHomeStarting
	}
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		if err := deleteExact(ctx, c, &pod); err != nil {
			return "", err
		}
		return "", ErrHomeStarting
	}

	var evs corev1.EventList
	if err := c.List(ctx, &evs, client.InNamespace(ns), client.MatchingFields{"involvedObject.name": pod.Name}); err != nil {
		return "", err
	}
	mine := evs.Items[:0]
	for _, e := range evs.Items {
		if e.InvolvedObject.UID == pod.UID {
			mine = append(mine, e)
		}
	}
	// Reuse the workspace status rules so the file pod and apps agree on
	// what counts as a busy home.
	phase, conds, _ := status.Derive(&model.App{}, status.Observed{Pod: &pod, Events: mine, Now: p.cfg.Now()})
	if phase == "Running" && pod.Status.PodIP != "" {
		return login.String() + "@" + net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(rcPort)), nil
	}
	for _, cd := range conds {
		if cd.Status == metav1.ConditionFalse && cd.Reason == string(reasons.HomeBusy) {
			return "", ErrHomeBusy
		}
	}
	return "", ErrHomeStarting
}

// create makes pod with a new password: sealed in its annotation, and for
// the container in a Secret the pod owns (deleted with it). It returns
// ErrHomeStarting once both exist.
func (p *Pods) create(ctx context.Context, pod *corev1.Pod) error {
	pass := newPassword()
	secretName, err := sealLogin(p.cfg.Seal, pod, pass)
	if err != nil {
		return err
	}
	c := p.cfg.Client
	if err := c.Create(ctx, pod); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ErrHomeStarting
		}
		return err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName, Namespace: pod.Namespace,
			Labels: map[string]string{model.LabelRole: roleFiles},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID,
				BlockOwnerDeletion: ptr.To(false),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{passwordKey: []byte(pass)},
	}
	if err := c.Create(ctx, sec); err != nil {
		// A pod whose password never got a Secret cannot start; it goes.
		return errors.Join(fmt.Errorf("create the file pod's password: %w", err), deleteExact(ctx, c, pod))
	}
	return ErrHomeStarting
}

// sealLogin points pod's rclone at a new password Secret and records pass,
// sealed with seal and bound to the namespace, the pod and that Secret, in
// the pod's annotation. It returns the Secret's name.
func sealLogin(seal tink.AEAD, pod *corev1.Pod, pass string) (string, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails
	name := pod.Name + "-rc-" + hex.EncodeToString(b)
	sealed, err := seal.Encrypt([]byte(pass), loginContext(pod, name))
	if err != nil {
		return "", fmt.Errorf("seal the file pod's password: %w", err)
	}
	ctr := &pod.Spec.Containers[0]
	ctr.Env = slices.DeleteFunc(ctr.Env, func(e corev1.EnvVar) bool { return e.Name == envRCPass })
	ctr.Env = append(ctr.Env, corev1.EnvVar{Name: envRCPass, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: passwordKey},
	}})
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[annotationLogin] = base64.StdEncoding.EncodeToString(sealed)
	return name, nil
}

// loginContext binds a sealed password to where it belongs, so one copied
// onto another pod does not open.
func loginContext(pod *corev1.Pod, secretName string) []byte {
	return []byte(pod.Namespace + "/" + pod.Name + "/" + secretName)
}

// Touch records that the user is active, so their file pod (if any) is
// kept for another IdleAfter. It never creates a pod.
func (p *Pods) Touch(userID string) {
	p.mu.Lock()
	p.lastUse[userID] = p.cfg.Now()
	p.mu.Unlock()
}

// Reap deletes file pods unused for IdleAfter. A pod this gateway has not
// served (for example after a restart) counts as used when it was created.
func (p *Pods) Reap(ctx context.Context) error {
	namespaces := []string{p.cfg.Namespace}
	if p.cfg.ReapIn != nil {
		var err error
		if namespaces, err = p.cfg.ReapIn(ctx); err != nil {
			return err
		}
	}
	var errs []error
	var pods corev1.PodList
	onlyFiles := client.MatchingLabels{model.LabelRole: roleFiles}
	if p.cfg.ReapAcrossNamespaces && p.cfg.ReapIn != nil {
		var all corev1.PodList
		if err := p.cfg.Client.List(ctx, &all, onlyFiles); err != nil {
			return err
		}
		managed := make(map[string]bool, len(namespaces))
		for _, ns := range namespaces {
			managed[ns] = true
		}
		for _, pod := range all.Items {
			if managed[pod.Namespace] {
				pods.Items = append(pods.Items, pod)
			}
		}
	} else {
		// One namespace failing must not stop the others being swept.
		for _, ns := range namespaces {
			var part corev1.PodList
			if err := p.cfg.Client.List(ctx, &part, client.InNamespace(ns), onlyFiles); err != nil {
				errs = append(errs, fmt.Errorf("list file pods in %s: %w", ns, err))
				continue
			}
			pods.Items = append(pods.Items, part.Items...)
		}
	}
	now := p.cfg.Now()
	var idle []*corev1.Pod
	p.mu.Lock()
	for i := range pods.Items {
		pod := &pods.Items[i]
		last, ok := p.lastUse[pod.Labels[model.LabelOwner]]
		if !ok {
			last = pod.CreationTimestamp.Time
		}
		if now.Sub(last) >= p.cfg.IdleAfter {
			idle = append(idle, pod)
		}
	}
	for s, last := range p.lastUse {
		if now.Sub(last) >= p.cfg.IdleAfter {
			delete(p.lastUse, s)
		}
	}
	p.mu.Unlock()

	for _, pod := range idle {
		if err := deleteExact(ctx, p.cfg.Client, pod); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deleteExact deletes pod only if it is still the same object (same UID), so
// a pod recreated under the same name in between is kept.
func deleteExact(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	err := c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// Run reaps idle file pods every minute until ctx is done.
func (p *Pods) Run(ctx context.Context) {
	t := time.NewTicker(reapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.Reap(ctx); err != nil {
				slog.Error("reap file pods", "err", err)
			}
		}
	}
}

// held reports whether h is being restored or moved to other storage.
func held(h *model.Home) bool {
	return h != nil && (meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionRestoring) ||
		meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionMigrating))
}

// hasFolders reports whether pod mounts exactly the group folders it should:
// dir (when asked for) and no folder outside in.Folders and dir, so a folder
// of a group the user has left is not kept mounted.
func hasFolders(pod *corev1.Pod, in podstorage.Input, dir string) bool {
	want := map[string]bool{}
	for _, f := range in.Folders {
		want[podstorage.GroupVolume(naming.GroupDir(f.Spec.Group))] = true
	}
	if dir != "" {
		want[podstorage.GroupVolume(dir)] = true
	}
	found := dir == ""
	for _, v := range pod.Spec.Volumes {
		if !podstorage.IsGroupVolume(v.Name) {
			continue
		}
		if !want[v.Name] {
			return false
		}
		found = found || v.Name == podstorage.GroupVolume(dir)
	}
	return found
}

// writesAs reports whether pod runs as uid with the home mounted writable,
// as buildPod makes it; a pod made by an older gateway or before the
// owner's uid changed does not.
func writesAs(pod *corev1.Pod, uid int64) bool {
	sc := pod.Spec.SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != uid || len(pod.Spec.Containers) == 0 {
		return false
	}
	return slices.ContainsFunc(pod.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool {
		return m.MountPath == dataDir && !m.ReadOnly
	})
}

// buildPod returns the file pod: rclone running as the owner (in.UID and
// in.GID) with the owner's storage (the layout podstorage builds for in)
// mounted read-only, except the home, which Lister.Save writes to.
func buildPod(userSlug, namespace, image string, in podstorage.Input) *corev1.Pod {
	in.ReadOnly = true
	in.ScratchMax = resource.MustParse("64Mi")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName(userSlug),
			Namespace: namespace,
			Labels:    map[string]string{model.LabelRole: roleFiles, model.LabelOwner: userSlug},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false),
			EnableServiceLinks:           ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:  "rclone",
				Image: image,
				// rclone asks every call for the pod's own password, so the
				// home stays closed where the network plugin does not enforce
				// the gateway-only NetworkPolicy. The password comes from a
				// Secret (create adds it), so whoever may read pods there
				// does not see it.
				Args: []string{"rcd", fmt.Sprintf("--rc-addr=:%d", rcPort), "--rc-serve"},
				Env: []corev1.EnvVar{
					{Name: envRCUser, Value: rcUser},
					{Name: "HOME", Value: scratchDir},
					{Name: "RCLONE_CONFIG", Value: scratchDir + "/rclone.conf"},
					{Name: "RCLONE_CACHE_DIR", Value: scratchDir + "/cache"},
				},
				Ports: []corev1.ContainerPort{{Name: "rc", ContainerPort: rcPort}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					ReadOnlyRootFilesystem:   ptr.To(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				// rclone listens within milliseconds of starting, so the probe
				// runs at once and every second: the picker waits on it.
				ReadinessProbe: &corev1.Probe{
					ProbeHandler:  corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(rcPort)}},
					PeriodSeconds: 1,
				},
			}},
		},
	}
	layout := podstorage.Build(userSlug, in)
	for i := range layout.Volumes {
		if v := &layout.Volumes[i]; v.Name == podstorage.HomeVolume {
			v.PersistentVolumeClaim.ReadOnly = false
		}
	}
	for i := range layout.Mounts {
		if m := &layout.Mounts[i]; m.Name == podstorage.HomeVolume {
			m.ReadOnly = false
		}
	}
	layout.Apply(pod, &pod.Spec.Containers[0])
	return pod
}

// rcloneLogin is the user and password the file pod's rclone asks for, or
// nil if it has none the gateway can open: an older pod with no password or
// with it in its spec, or one sealed with another key.
func rcloneLogin(seal tink.AEAD, pod *corev1.Pod) *url.Userinfo {
	var user, secretName string
	for _, ctr := range pod.Spec.Containers {
		for _, e := range ctr.Env {
			switch {
			case e.Name == envRCUser:
				user = e.Value
			case e.Name == envRCPass && e.Value == "" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil &&
				e.ValueFrom.SecretKeyRef.Key == passwordKey:
				secretName = e.ValueFrom.SecretKeyRef.Name
			}
		}
	}
	sealed, err := base64.StdEncoding.DecodeString(pod.Annotations[annotationLogin])
	if secretName == "" || err != nil || len(sealed) == 0 {
		return nil
	}
	pass, err := seal.Decrypt(sealed, loginContext(pod, secretName))
	if err != nil || len(pass) == 0 {
		return nil
	}
	return url.UserPassword(user, string(pass))
}

// newPassword is 256 random bits, URL-safe.
func newPassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// Release deletes the user's file pod, if any, so the home claim is free
// for an app (a single-node home attaches to one node at a time).
func (p *Pods) Release(ctx context.Context, userID string) error {
	p.mu.Lock()
	delete(p.lastUse, userID)
	p.mu.Unlock()
	var pod corev1.Pod
	err := p.cfg.Client.Get(ctx, client.ObjectKey{Namespace: p.ns(ctx), Name: podName(userID)}, &pod)
	if err != nil || !isFilePodOf(&pod, userID) {
		// A same-named pod that is not this user's file pod is left alone.
		return client.IgnoreNotFound(err)
	}
	return deleteExact(ctx, p.cfg.Client, &pod)
}

// isFilePodOf reports whether pod carries the file pod labels for userSlug.
func isFilePodOf(pod *corev1.Pod, userSlug string) bool {
	return pod.Labels[model.LabelRole] == roleFiles && pod.Labels[model.LabelOwner] == userSlug
}
