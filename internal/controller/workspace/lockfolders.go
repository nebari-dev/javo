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

// How a hidden place at the top of a home is held (LockPlace.Rule).
const (
	// PlaceRisky is opened only once its owner was warned what it holds
	// and allowed it (model.FolderGrant.Risky).
	PlaceRisky = "risky"
	// PlaceOpen is open to every locked agent already (agent-lock.sh), to
	// LockPlace.Mode, and is not the owner's to add or take away.
	PlaceOpen = "open"
	// PlaceClosed is never opened, and neither is anything in it.
	PlaceClosed = "closed"
	// PlaceAround is a folder that holds a closed place: it is never
	// opened as a whole, and what else is in it has a rule of its own.
	PlaceAround = "around"
)

// LockPlace is a hidden file or folder at the top of a home (a name there
// that starts with a dot) and how a locked agent is held to it, in words
// the person is shown.
type LockPlace struct {
	// Path is relative to the home.
	Path string `json:"path"`
	Rule string `json:"rule" enum:"risky,open,closed,around" doc:"risky: opened only with the owner's acknowledgement on the grant; open: always open to the agent, to mode; closed: never opened, nor anything in it; around: never opened as a whole, as it holds a closed place"`
	// Mode is how a PlaceOpen place is open.
	Mode string `json:"mode,omitempty" enum:"read,write"`
	// What the place holds, as words that follow "holds" (or, for an open
	// place, stand alone).
	What string `json:"what"`
	// Means is what follows from an agent opening it, or why none can, in
	// whole sentences.
	Means string `json:"means,omitempty"`
}

// shellStart is what the files a shell runs at every start hold.
var shellStart = [2]string{"commands your terminal runs every time it opens",
	"They often hold passwords, and an agent that can change them can run commands as you, outside its lock."}

// lockPlaces are the hidden places of a home with words of their own: what
// signs in as the person elsewhere and the files their shell runs at every
// start (risky), what agent-lock.sh grants every agent already (open: the
// agents' own state, the tools' caches, and git's settings to read), and the platform's own
// (closed: ~/.config/javo is the person's sign-in to the platform, and an
// agent that has it lifts its own lock), with every folder around a closed
// one (aroundClosed). No list names every file a tool reads its sign-in
// from or runs at its start (~/.npmrc, ~/.local/bin, ~/.config/autostart),
// so every other hidden name at the top of the home is risky too, in the
// words of hiddenPlace (placeOf). It is the one list: the API hands it to
// the page that marks them.
var lockPlaces = aroundClosed([]LockPlace{
	{Path: ".ssh", Rule: PlaceRisky, What: "your SSH keys", Means: "Anyone who has these keys can sign in to your servers and push code as you."},
	{Path: ".gnupg", Rule: PlaceRisky, What: "your GPG keys", Means: "Anyone who has these keys can sign and decrypt as you."},
	{Path: ".git-credentials", Rule: PlaceRisky, What: "your Git passwords", Means: "Anyone who has them can read and change your repositories as you."},
	{Path: ".netrc", Rule: PlaceRisky, What: "your passwords for other servers", Means: "Anyone who has them can sign in to those servers as you."},
	{Path: ".config/gh", Rule: PlaceRisky, What: "your GitHub sign-in", Means: "Anyone who has it can act as you on GitHub."},
	{Path: ".aws", Rule: PlaceRisky, What: "your cloud login", Means: "Anyone who has it can use your cloud account, and spend money on it."},
	{Path: ".kube", Rule: PlaceRisky, What: "your Kubernetes sign-in", Means: "Anyone who has it can act as you on your clusters."},
	{Path: ".docker", Rule: PlaceRisky, What: "your registry sign-ins", Means: "Anyone who has them can pull and push images as you."},
	{Path: ".bashrc", Rule: PlaceRisky, What: shellStart[0], Means: shellStart[1]},
	{Path: ".bash_profile", Rule: PlaceRisky, What: shellStart[0], Means: shellStart[1]},
	{Path: ".profile", Rule: PlaceRisky, What: shellStart[0], Means: shellStart[1]},
	{Path: ".zshrc", Rule: PlaceRisky, What: shellStart[0], Means: shellStart[1]},
	{Path: ".zshenv", Rule: PlaceRisky, What: shellStart[0], Means: shellStart[1]},
	{Path: ".gitconfig", Rule: PlaceOpen, Mode: model.FolderRead, What: "Git's settings"},
	{Path: ".config/git", Rule: PlaceOpen, Mode: model.FolderRead, What: "Git's settings"},
	{Path: ".claude", Rule: PlaceOpen, Mode: model.FolderWrite, What: "Claude Code's own settings"},
	{Path: ".claude.json", Rule: PlaceOpen, Mode: model.FolderWrite, What: "Claude Code's own settings"},
	{Path: ".pi", Rule: PlaceOpen, Mode: model.FolderWrite, What: "Pi's own settings"},
	{Path: ".harf", Rule: PlaceOpen, Mode: model.FolderWrite, What: "Harf's own chats"},
	{Path: ".config/herdr", Rule: PlaceOpen, Mode: model.FolderWrite, What: "herdr's own settings"},
	{Path: ".cache", Rule: PlaceOpen, Mode: model.FolderWrite, What: "Tool caches"},
	{Path: ".npm", Rule: PlaceOpen, Mode: model.FolderWrite, What: "npm's cache"},
	{Path: ".javo", Rule: PlaceClosed, What: "the platform's own files", Means: "They belong to the platform, not to an agent."},
	{Path: ".config/javo", Rule: PlaceClosed, What: "your sign-in to this platform", Means: "An agent that has it can lift its own lock."},
})

// aroundClosed is places with one more for every folder around a closed
// one: opened as a whole it would open the closed place too.
func aroundClosed(places []LockPlace) []LockPlace {
	out := slices.Clone(places)
	for _, n := range places {
		if n.Rule != PlaceClosed {
			continue
		}
		for dir := path.Dir(n.Path); dir != "."; dir = path.Dir(dir) {
			if !slices.ContainsFunc(out, func(o LockPlace) bool { return o.Path == dir }) {
				out = append(out, LockPlace{Path: dir, Rule: PlaceAround, What: n.What + ", in ~/" + n.Path,
					Means: n.Means + " To open something else in ~/" + dir + ", tick it there, not the whole folder."})
			}
		}
	}
	return out
}

// hiddenPlace is how every hidden name at the top of a home that
// lockPlaces has no words for is held. Its Path is no path but what every
// such path starts with: the dot.
var hiddenPlace = LockPlace{Path: ".", Rule: PlaceRisky, What: "a tool's settings or sign-ins",
	Means: "Anyone who has a sign-in kept there can act as you with that tool, and an agent that can change a tool's settings can make it run commands as you."}

// LockPlaces are the hidden places of a home with words of their own.
func LockPlaces() []LockPlace { return slices.Clone(lockPlaces) }

// HiddenPlace is how every other hidden name at the top of a home is held.
func HiddenPlace() LockPlace { return hiddenPlace }

// MaxLockFolders is how many folders a person may add to one agent.
const MaxLockFolders = 16

// maxLockFolderPath is the longest path of one of them.
const maxLockFolderPath = 1024

// LockFolders are the grants of asked that a locked agent app ws may have,
// with their paths cleaned and relative to the home, and why the first one
// left out is refused (nil when all are kept). The API refuses a list with
// an error; the planner grants what is kept and never the rest, so a row
// written past the API, or before a place was closed in lockPlaces, opens
// nothing.
//
// A path is a folder or file inside the home: relative to it, or from / in
// the home as a container has it. It names no "..", is not the home or a
// folder the home is in, is not in the app's project (open already), is
// hidden at the top of the home (a name there that starts with a dot, or
// anything in one) only where lockPlaces lets it be and its owner was
// warned (placeProblem: the grant's Risky, which is kept on such a grant
// and on no other), and is on the list once. A
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
			err = placeProblem(model.FolderGrant{Path: p, Risky: g.Risky})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", g.Path, err))
			continue
		}
		kept = append(kept, model.FolderGrant{Path: p, Mode: g.Mode, Risky: hiddenAtTop(p)})
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

// placeOf is how the path p of a home is held when its first name starts
// with a dot, and false when it does not: a hidden file or folder at the
// top of a home holds a tool's settings or sign-in, where a hidden name
// further down (projects/api/.github) is a project's own. It is the
// nearest of lockPlaces that p is or is in (a folder around a closed place
// speaks for itself only), else hiddenPlace.
func placeOf(p string) (LockPlace, bool) {
	if !hiddenAtTop(p) {
		return LockPlace{}, false
	}
	at := hiddenPlace
	for _, n := range lockPlaces {
		if (p == n.Path || n.Rule != PlaceAround && within(n.Path, p)) && len(n.Path) > len(at.Path) {
			at = n
		}
	}
	return at, true
}

// hiddenAtTop reports whether the path p of a home is, or is in, a hidden
// file or folder at the top of it.
func hiddenAtTop(p string) bool { return strings.HasPrefix(p, hiddenPlace.Path) }

// placeProblem is why the grant g of the cleaned path g.Path is not one
// to keep; nil for a path that is not hidden at the top of the home, and
// for a risky one its owner acknowledged.
func placeProblem(g model.FolderGrant) error {
	at, hidden := placeOf(g.Path)
	if !hidden {
		return nil
	}
	means := strings.TrimSuffix(at.Means, ".")
	switch {
	case at.Rule == PlaceOpen:
		return fmt.Errorf("~/%s is already open to every agent", at.Path)
	case at.Rule != PlaceRisky:
		return fmt.Errorf("no agent can open ~/%s: it holds %s. %s", at.Path, at.What, means)
	case !g.Risky:
		return fmt.Errorf("it holds %s, and opens only once you accept its warning. %s", at.What, means)
	}
	return nil
}

// within reports whether p is dir or a path inside it.
func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}
