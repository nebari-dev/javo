package workspace

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/homepath"
	"github.com/nebari-dev/javo/internal/model"
)

// NeverFolder is a place in a home no coding agent is let into, and why in
// words the person is shown.
type NeverFolder struct {
	// Path is relative to the home.
	Path string `json:"path"`
	Why  string `json:"why"`
}

// neverFolders are places of a home a person cannot let a locked agent
// open, to read or to change, each with a reason of its own to show: what
// signs in as them elsewhere, the files their shell and git run or read at
// every start (an agent that wrote one would run commands outside its
// lock), the platform's own folder, and what the agents keep, which
// agent-lock.sh grants already. No list names every such file (~/.npmrc,
// ~/.local/bin, ~/.config/autostart...), so nothing hidden at the top of
// the home is granted at all (neverProblem): each of these is hidden, and
// the list only says why. It is the one list: the API hands it to the page
// that marks them.
var neverFolders = []NeverFolder{
	{".ssh", "holds your SSH keys"},
	{".gnupg", "holds your GPG keys"},
	{".git-credentials", "holds your Git passwords"},
	{".netrc", "holds your passwords for other servers"},
	{".config/gh", "holds your GitHub sign-in"},
	{".aws", "holds your AWS keys"},
	{".kube", "holds your Kubernetes sign-in"},
	{".docker", "holds your registry sign-ins"},
	{".bashrc", "runs every time you open a terminal"},
	{".bash_profile", "runs every time you open a terminal"},
	{".profile", "runs every time you open a terminal"},
	{".zshrc", "runs every time you open a terminal"},
	{".zshenv", "runs every time you open a terminal"},
	{".gitconfig", "decides what Git runs for you"},
	{".config/git", "decides what Git runs for you"},
	{".javo", "belongs to the platform"},
	{".config/javo", "holds your sign-in to this platform"},
	{".claude", "the agent's own settings, already open"},
	{".claude.json", "the agent's own settings, already open"},
	{".pi", "the agent's own settings, already open"},
	{".harf", "the agent's own chats, already open"},
	{".config/herdr", "the agent's own settings, already open"},
}

// HiddenFolderWhy is why nothing hidden at the top of a home (a name that
// starts with a dot) can be added, in words the person is shown, when
// neverFolders has no reason of its own for it.
const HiddenFolderWhy = "hidden files and folders hold settings and sign-ins"

// NeverFolders are the places of a home no coding agent is let into.
func NeverFolders() []NeverFolder { return slices.Clone(neverFolders) }

// MaxLockFolders is how many folders a person may add to one agent.
const MaxLockFolders = 16

// maxLockFolderPath is the longest path of one of them.
const maxLockFolderPath = 1024

// LockFolders are the grants of asked that a locked agent app ws may have,
// with their paths cleaned and relative to the home, and why the first one
// left out is refused (nil when all are kept). The API refuses a list with
// an error; the planner grants what is kept and never the rest, so a row
// written past the API, or before a place was added to neverFolders, opens
// nothing.
//
// A path is a folder or file inside the home: relative to it, or from / in
// the home as a container has it. It names no "..", is not the home or a
// folder the home is in, is not in the app's project (open already), is
// nothing hidden at the top of the home (a name there that starts with a
// dot, or anything in one: neverProblem), and is on the list once. A
// folder inside another granted one is kept: the wider of the two modes
// then holds inside it. Whether the path is, or is reached through, a
// symbolic link is known only where the agent starts, and refused there
// (agent-lock.sh, javo-sandbox --no-links-under).
func LockFolders(ws *model.App, asked []model.FolderGrant) ([]model.FolderGrant, error) {
	project, _ := strings.CutPrefix(containerPlaces.lockFolder(ws), homeDir+"/")
	var kept []model.FolderGrant
	var errs []error
	for _, g := range asked {
		p, err := lockFolderPath(g.Path)
		switch {
		case err != nil:
		case g.Mode != model.FolderRead && g.Mode != model.FolderWrite:
			err = errors.New("pick read or write")
		case within(project, p):
			err = errors.New("it is in the app's project, which is already open")
		case slices.ContainsFunc(kept, func(k model.FolderGrant) bool { return k.Path == p }):
			err = errors.New("it is already on the list")
		case len(kept) == MaxLockFolders:
			err = fmt.Errorf("an agent can open at most %d added folders and files", MaxLockFolders)
		default:
			err = neverProblem(p)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", g.Path, err))
			continue
		}
		kept = append(kept, model.FolderGrant{Path: p, Mode: g.Mode})
	}
	if len(errs) > 0 {
		return kept, errs[0]
	}
	return kept, nil
}

// lockFolderPath is asked as a clean path relative to the home, or why it
// is not one inside it.
func lockFolderPath(asked string) (string, error) {
	if err := homepath.Check(asked); err != nil {
		return "", err
	}
	if len(asked) > maxLockFolderPath {
		return "", errors.New("the path is too long")
	}
	if slices.Contains(strings.Split(asked, "/"), "..") {
		return "", errors.New(`a path with ".." in it is not the folder it reads as`)
	}
	p := path.Clean(asked)
	if path.IsAbs(p) {
		in, ok := strings.CutPrefix(p, homeDir+"/")
		if !ok {
			return "", errors.New("pick a folder or file inside your home")
		}
		p = in
	}
	if p == "." {
		return "", errors.New("pick a folder or file inside your home, not the home itself")
	}
	return p, nil
}

// neverProblem is why the path p of a home is not one to grant: its first
// name starts with a dot. Such a file or folder at the top of a home holds
// a tool's settings or sign-in, and the reason is the one neverFolders
// gives for it, else HiddenFolderWhy. A hidden name further down
// (projects/api/.github) is a project's own and is granted.
func neverProblem(p string) error {
	if !strings.HasPrefix(p, ".") {
		return nil
	}
	top, _, _ := strings.Cut(p, "/")
	why := HiddenFolderWhy
	for _, n := range neverFolders {
		if within(n.Path, p) {
			top, why = n.Path, n.Why
		}
	}
	return fmt.Errorf("no agent can open ~/%s: %s", top, why)
}

// within reports whether p is dir or a path inside it.
func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}
