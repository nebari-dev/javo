// Package source holds what runs around an app cloned from Git: the check of
// the clone inside the pod (`javo source-check`) and the reading of its
// failures into catalog reasons.
package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nebari-dev/javo/internal/homepath"
)

// The init containers of a source app, by name.
const (
	CloneContainer    = "clone"
	CheckContainer    = "source-check"
	BuildContainer    = "build"
	SiteCopyContainer = "site-copy"
	// SiteConfigContainer writes the server config of a template that serves a site.
	SiteConfigContainer = "site-config"
)

// IsSiteStep reports whether the init container called name fetches an app's
// code or makes the site it serves: in an app that builds, these steps are
// its Built step.
func IsSiteStep(name string) bool {
	switch name {
	case CloneContainer, CheckContainer, BuildContainer, SiteCopyContainer, SiteConfigContainer:
		return true
	}
	return false
}

// commitPrefix starts the termination message of a successful source-check.
const commitPrefix = "commit="

// ErrFileMissing is the failure the person can fix: the folder or file the
// app opens is not in the clone. Its text starts with "file-missing: ", which
// Classify reads.
var ErrFileMissing = errors.New("file-missing")

var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func missing(what, why string) error {
	if why != "" {
		why = " (" + why + ")"
	}
	return fmt.Errorf("%w: %s%s", ErrFileMissing, what, why)
}

// Check reads the commit git-sync checked out (the basename of what link
// points to, which must be 40 hex digits) and verifies that folder path and
// file inside it exist, both resolved inside the clone: a path with "..", an
// absolute path or a symlink that leaves the clone counts as missing. An empty
// path is the root of the clone and an empty file checks only the folder. It
// reads no network and no Kubernetes API.
func Check(link, path, file string) (commit string, err error) {
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("read the clone link: %w", err)
	}
	commit = filepath.Base(target)
	if !commitRE.MatchString(commit) {
		return "", fmt.Errorf("the clone link points to %q, which is not a commit", commit)
	}
	root, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", fmt.Errorf("resolve the clone: %w", err)
	}
	dir, err := inside(root, root, path)
	if err != nil {
		return "", missing(path, err.Error())
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", missing(path, "not a folder in the repository")
	}
	if file == "" {
		return commit, nil
	}
	f, err := inside(root, dir, file)
	if err != nil {
		return "", missing(file, err.Error())
	}
	if _, err := os.Stat(f); err != nil {
		return "", missing(file, "")
	}
	return commit, nil
}

// inside joins rel under base and returns the symlink-resolved result, or an
// error when rel is unsafe or the result is not inside root.
func inside(root, base, rel string) (string, error) {
	if homepath.Check(rel) != nil {
		return "", errors.New("not a valid path")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("outside the repository")
	}
	joined := filepath.Join(base, rel)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Does not exist; still refuse a lexical escape first.
			if !within(root, joined) {
				return "", errors.New("outside the repository")
			}
			return joined, nil
		}
		return "", err
	}
	if !within(root, resolved) {
		return "", errors.New("outside the repository")
	}
	return resolved, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CommitMessage is the termination message of a successful check.
func CommitMessage(commit string) string { return commitPrefix + commit }

// CommitOf reads the commit from a source-check termination message.
func CommitOf(message string) (string, bool) {
	c, ok := strings.CutPrefix(strings.TrimSpace(message), commitPrefix)
	return c, ok && commitRE.MatchString(c)
}
