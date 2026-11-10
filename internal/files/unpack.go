package files

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrBadArchive means the upload is not a gzipped tar, or holds an entry
// Unpack refuses (a BadEntryError).
var ErrBadArchive = errors.New("files: not a gzipped tar of plain files and folders")

// ErrEmptyArchive means the archive holds no file.
var ErrEmptyArchive = errors.New("files: archive holds no file")

// ErrTooManyEntries means the archive holds more entries than its limit.
var ErrTooManyEntries = errors.New("files: archive holds too many entries")

// UploadLimits bound what one Unpack writes: Bytes of files together, and
// Entries tar headers (files, folders and the rest), each of which costs a
// call to the file pod or at least a read.
type UploadLimits struct {
	Bytes   int64
	Entries int
}

// BadEntryError is ErrBadArchive about one entry: its path leaves the
// folder, or it is a link, a device or a pipe.
type BadEntryError struct{ Name string }

func (e *BadEntryError) Error() string {
	return "files: archive entry " + strconv.Quote(e.Name) + " refused"
}

func (e *BadEntryError) Unwrap() error { return ErrBadArchive }

// Unpacked is what Unpack wrote: the folder, how many files and bytes, and
// whether it replaced a folder that was there.
type Unpacked struct {
	Path     string
	Files    int
	Bytes    int64
	Replaced bool
}

// Unpack writes the files of archive, a gzipped tar, into the folder rel of
// the home (a non-empty path from CleanPath).
//
// The files go to a hidden folder next to rel first, which is moved to rel
// only once every entry is written; on any failure it is deleted. Entries
// must be plain files or folders whose paths stay inside rel.
func (l *Lister) Unpack(ctx context.Context, userID, rel string, archive io.Reader, limits UploadLimits, replace bool) (Unpacked, error) {
	addr, err := l.pods.Ensure(ctx, userID)
	if err != nil {
		return Unpacked{}, err
	}
	unlock, err := l.saving.lock(ctx, userID+"\x00"+rel)
	if err != nil {
		return Unpacked{}, err
	}
	defer unlock()
	parent, name := path.Split(rel)
	if err := l.checkFolders(ctx, addr, dataDir, strings.TrimSuffix(parent, "/"), true); err != nil {
		return Unpacked{}, err
	}
	exists := false
	switch e, err := l.remote.Stat(ctx, addr, dataDir, rel); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return Unpacked{}, err
	case !replace || !e.IsDir:
		// Only a real folder is replaced, never a file or a symlink.
		return Unpacked{}, ErrExists
	default:
		exists = true
	}
	staging := path.Join(parent, "."+name+".upload-"+randomSuffix())
	body := &readRecorder{r: archive}
	out, err := l.writeArchive(ctx, addr, staging, body, limits)
	if err == nil && out.Files == 0 {
		err = ErrEmptyArchive
	}
	if err == nil {
		err = l.swapIn(ctx, addr, staging, rel, exists)
	}
	if err != nil {
		l.purge(ctx, addr, staging)
		if body.err != nil {
			// The upload itself could not be read: over its limit, or the
			// client went away. That is the error, whatever it broke.
			return Unpacked{}, body.err
		}
		return Unpacked{}, err
	}
	out.Path, out.Replaced = rel, exists
	return out, nil
}

// swapIn moves the folder staging to rel. With replace, the folder at rel
// is moved aside first and deleted once staging is in its place, or moved
// back if that fails.
func (l *Lister) swapIn(ctx context.Context, addr, staging, rel string, replace bool) error {
	if !replace {
		return l.remote.MoveDir(ctx, addr, staging, rel)
	}
	parent, name := path.Split(rel)
	old := path.Join(parent, "."+name+".old-"+randomSuffix())
	if err := l.remote.MoveDir(ctx, addr, rel, old); err != nil {
		return err
	}
	if err := l.remote.MoveDir(ctx, addr, staging, rel); err != nil {
		back, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if berr := l.remote.MoveDir(back, addr, old, rel); berr != nil {
			slog.Error("move folder back after a failed upload", "path", rel, "from", old, "err", berr)
		}
		return err
	}
	l.purge(ctx, addr, old)
	return nil
}

// writeArchive writes the files of archive into the folder dir of the home,
// at most limits.Bytes of them together (ErrTooLarge) out of at most
// limits.Entries headers (ErrTooManyEntries).
func (l *Lister) writeArchive(ctx context.Context, addr, dir string, archive io.Reader, limits UploadLimits) (Unpacked, error) {
	zr, err := gzip.NewReader(archive)
	if err != nil {
		return Unpacked{}, badArchive(err)
	}
	// archive/tar reads some headers (pax, GNU long names) inside Next
	// without returning them, so the entry count cannot bound them: the
	// stream itself is bounded, at the files' bytes plus room for each
	// allowed entry's headers.
	stream := &io.LimitedReader{R: zr, N: limits.Bytes + int64(limits.Entries)*entryOverhead + 1}
	out, err := l.writeEntries(ctx, addr, dir, tar.NewReader(stream), limits)
	if stream.N <= 0 {
		return out, ErrTooLarge
	}
	return out, err
}

// entryOverhead is how many bytes of tar headers and padding each allowed
// entry may add to an archive's unpacked stream.
const entryOverhead = 64 << 10

// writeEntries writes the files of tr into the folder dir of the home.
func (l *Lister) writeEntries(ctx context.Context, addr, dir string, tr *tar.Reader, limits UploadLimits) (Unpacked, error) {
	var out Unpacked
	for entries := 1; ; entries++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, badArchive(err)
		}
		if entries > limits.Entries {
			return out, ErrTooManyEntries
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			// Metadata for the whole archive (git archive writes the
			// commit there), not a file.
			continue
		}
		p, err := entryPath(h)
		if err != nil {
			return out, err
		}
		if h.Typeflag == tar.TypeDir {
			// Folders are made by the files in them.
			continue
		}
		if h.Size > limits.Bytes-out.Bytes {
			return out, ErrTooLarge
		}
		at, file := path.Split(path.Join(dir, p))
		f := &readRecorder{r: tr}
		if err := l.remote.UploadStream(ctx, addr, strings.TrimSuffix(at, "/"), file, f); err != nil {
			if f.err != nil {
				// The archive ends inside this file.
				return out, badArchive(f.err)
			}
			return out, err
		}
		out.Files++
		out.Bytes += h.Size
	}
}

// entryPath is where the entry h goes, relative to the folder unpacked
// into. Only plain files and folders are allowed, and a path may be
// neither absolute nor hold "..", even one that would stay inside.
func entryPath(h *tar.Header) (string, error) {
	bad := &BadEntryError{Name: h.Name}
	if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
		return "", bad
	}
	if strings.HasPrefix(h.Name, "/") || slices.Contains(strings.Split(h.Name, "/"), "..") {
		return "", bad
	}
	p, err := CleanPath(h.Name)
	if err != nil || p == "" && h.Typeflag == tar.TypeReg {
		return "", bad
	}
	return p, nil
}

// purge deletes the folder dir of the home, even after ctx ended (the
// client went away): a failed upload, or a replaced folder, leaves nothing
// behind. A failure is only logged; the upload's outcome stands.
func (l *Lister) purge(ctx context.Context, addr, dir string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := l.remote.Purge(ctx, addr, dir); err != nil {
		slog.Warn("delete upload folder", "path", dir, "err", err)
	}
}

// badArchive is err, from reading the archive, as ErrBadArchive.
func badArchive(err error) error { return fmt.Errorf("%w: %w", ErrBadArchive, err) }

// readRecorder keeps the first error other than io.EOF that reading r gave.
type readRecorder struct {
	r   io.Reader
	err error
}

func (rr *readRecorder) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && rr.err == nil {
		rr.err = err
	}
	return n, err
}

// randomSuffix names a staging folder no other upload uses.
func randomSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
