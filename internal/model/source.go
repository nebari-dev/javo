package model

import (
	"net/url"
	"regexp"
	"strings"
)

// Limits of a Source, and the sizes a preview sync result is held to.
const (
	MaxSourceURLLength = 2048
	MaxSourceRefLength = 255
	MaxSourcePathLen   = 1024
	MaxBuildLength     = 1024
	// DefaultPreviewMax is the number of previews an app gets when its creator
	// names none; MaxPreviewMax is the most they may ask for.
	DefaultPreviewMax = 3
	MaxPreviewMax     = 10
	// MaxPreviewItems is how many items a stored sync result keeps; the rest are cut.
	MaxPreviewItems = 50
	// MaxPreviewTitleLength is the longest pull request title a sync result keeps.
	MaxPreviewTitleLength = 256
)

// Source is the Git repository an app runs from. The app's File is then
// relative to <clone>/<Path>.
type Source struct {
	// URL is an https address with a host and no credentials, query or fragment.
	URL string `json:"url"`
	// Ref is a branch, tag or commit; empty means the repository's default branch.
	Ref string `json:"ref,omitempty"`
	// Path is a folder inside the repository, relative, empty for its root.
	Path string `json:"path,omitempty"`
	// Build overrides the template's build step; nil keeps the template's.
	Build *SourceBuild `json:"build,omitempty"`
	// Previews runs one preview app per open pull request; nil means off.
	Previews *Previews `json:"previews,omitempty"`
	// Fork is set by the controller on the preview of a pull request from a
	// fork. The API never takes it.
	Fork bool `json:"fork,omitempty"`
}

// SourceBuild is the command that turns the repository into the files to
// serve, and the folder it writes them to (relative to the repository folder).
type SourceBuild struct {
	Command string `json:"command,omitempty"`
	Output  string `json:"output,omitempty"`
}

// Previews turns on one preview app per open pull request.
type Previews struct {
	Enabled bool `json:"enabled"`
	// Max is how many open pull requests get a preview, 1 to MaxPreviewMax;
	// 0 means DefaultPreviewMax.
	Max int32 `json:"max,omitempty"`
	// Forks also previews pull requests from forks, which run code the
	// owner has not written.
	Forks bool `json:"forks,omitempty"`
}

// PreviewSync is what the last look at the pull requests found, kept for the
// Previews page. Reason is empty when the look worked.
type PreviewSync struct {
	Items   []PreviewItem `json:"items,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Message string        `json:"message,omitempty"`
}

// PreviewItem is one pull request: the preview it has (App is the child's
// short name) or the reason it has none.
type PreviewItem struct {
	Number  int32  `json:"number"`
	Title   string `json:"title,omitempty"`
	HeadSHA string `json:"headSha,omitempty"`
	App     string `json:"app,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Bounded is the result cut to what the store keeps: MaxPreviewItems items,
// each title at most MaxPreviewTitleLength characters.
func (s PreviewSync) Bounded() PreviewSync {
	if len(s.Items) > MaxPreviewItems {
		s.Items = s.Items[:MaxPreviewItems]
	}
	items := make([]PreviewItem, len(s.Items))
	for i, it := range s.Items {
		if r := []rune(it.Title); len(r) > MaxPreviewTitleLength {
			it.Title = string(r[:MaxPreviewTitleLength])
		}
		items[i] = it
	}
	s.Items = items
	return s
}

func (s *Source) validate(v *validator, field string) {
	urlOK := s.validateURL(v, field+".url")
	v.maxLen(field+".ref", s.Ref, MaxSourceRefLength)
	validateRelative(v, field+".path", s.Path, MaxSourcePathLen)
	if b := s.Build; b != nil {
		v.maxLen(field+".build.command", b.Command, MaxBuildLength)
		validateRelative(v, field+".build.output", b.Output, MaxBuildLength)
	}
	if p := s.Previews; p != nil {
		if p.Max != 0 && (p.Max < 1 || p.Max > MaxPreviewMax) {
			v.add(field+".previews.max", "max must be between 1 and 10")
		}
		if _, _, github := GitHubRepo(s.URL); p.Enabled && urlOK && !github {
			v.add(field+".previews", "previews need a repository on github.com")
		}
	}
}

// validateURL reports whether the URL passed. Its messages never repeat the
// address, which may hold a password a person pasted by mistake.
func (s *Source) validateURL(v *validator, field string) bool {
	if s.URL == "" {
		v.add(field, "url is required")
		return false
	}
	if charLen(s.URL) > MaxSourceURLLength {
		v.add(field, "must be at most 2048 characters")
		return false
	}
	u, err := url.Parse(s.URL)
	switch {
	case err != nil || u.Scheme != "https":
		v.add(field, "url must be an https address such as https://github.com/example-org/site")
	case u.Host == "" || u.Hostname() == "":
		v.add(field, "url must name a host")
	case u.User != nil:
		v.add(field, "url must not contain a user name or password")
	case u.RawQuery != "" || u.ForceQuery || strings.Contains(s.URL, "#"):
		v.add(field, "url must not contain a query or fragment")
	default:
		return true
	}
	return false
}

// validateRelative requires an empty value or a relative path of at most max
// characters that never steps out of its folder.
func validateRelative(v *validator, field, p string, max int) {
	if p == "" {
		return
	}
	v.maxLen(field, p, max)
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		v.add(field, "must be a relative path")
		return
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			v.add(field, `must not contain ".."`)
			return
		}
	}
}

var (
	githubOwner = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubRepo  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// GitHubRepo parses owner and repository out of the address of a repository
// on github.com, such as https://github.com/example-org/site.git. It is pure
// text: it does not check that the repository exists. Owner and repository
// must use only the characters GitHub allows, so a value built from them can
// never reach another API path or carry a query string.
func GitHubRepo(rawURL string) (owner, repo string, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner, repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !githubOwner.MatchString(owner) || !githubRepo.MatchString(repo) || repo == "." || repo == ".." {
		return "", "", false
	}
	return owner, repo, true
}
