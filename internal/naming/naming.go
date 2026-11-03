// Package naming maps user identities and display names to Kubernetes names.
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"golang.org/x/text/secure/precis"
	"golang.org/x/text/unicode/norm"

	"github.com/nebari-dev/javo/internal/reasons"
)

const (
	hashLen      = 8
	maxReadable  = 20
	maxLabel     = 63
	fallbackBase = "u"
)

// SubjectHash is the lowercase hex SHA-256 of "issuer\nsubject". The newline
// cannot appear in an issuer URL, so two different pairs never hash the same
// input.
func SubjectHash(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\n" + subject))
	return hex.EncodeToString(sum[:])
}

// IdentityName returns the Identity object name for a person: a readable
// lowercase part of the username (at most 20 characters, "u" when nothing
// readable is left), "-", and the first hexLen hex chars of SubjectHash. The
// result is a DNS-1123 label. The name is frozen at first login, so a later
// username change never renames it.
func IdentityName(username, issuer, subject string, hexLen int) string {
	readable := slugify(username, maxReadable)
	if readable == "" {
		readable = fallbackBase
	}
	return readable + "-" + SubjectHash(issuer, subject)[:hexLen]
}

// IdentityReadable returns the readable part of an Identity name (the name
// without IdentityName's "-<hex>" suffix): lowercase letters, digits and
// dashes, never empty. A name without that suffix is returned whole.
func IdentityReadable(name string) string {
	i := strings.LastIndexByte(name, '-')
	if i <= 0 {
		return name
	}
	if _, err := hex.DecodeString(name[i+1:]); err != nil {
		return name
	}
	return name[:i]
}

// WorkspaceName converts a display name to a DNS-1123 label.
func WorkspaceName(displayName string) (string, error) {
	name := slugify(displayName, maxLabel)
	if name == "" {
		return "", errors.New(reasons.Msg(reasons.MsgNameNeedsLetter))
	}
	return name, nil
}

// ObjectName is the Kubernetes name of a user's app: "<userSlug>-<name>", so
// two users' apps with the same short name never collide in the shared
// namespace. The API and URLs keep the short name. The result is a DNS-1035
// label (a Service carries it): it starts with a letter and, when too long,
// is cut and ends in 8 hex chars of SHA-256 of the full pair.
func ObjectName(userSlug, name string) string {
	return ObjectNameMax(userSlug, name, maxLabel)
}

// ObjectNameMax is ObjectName with a different length limit (a CronJob name
// may be at most 52 characters because its Jobs append 11).
func ObjectNameMax(userSlug, name string, max int) string {
	full := userSlug + "-" + name
	if full[0] < 'a' || full[0] > 'z' {
		full = fallbackBase + full
	}
	if len(full) <= max {
		return full
	}
	sum := sha256.Sum256([]byte(userSlug + "/" + name))
	cut := strings.TrimRight(full[:max-hashLen-1], "-")
	return cut + "-" + hex.EncodeToString(sum[:])[:hashLen]
}

// PreviewName is the short name of the preview of pull request number of the
// app parent: "<parent>--pr-<number>". WorkspaceName never produces "--", so
// a preview's name cannot equal one a person picked. A parent too long to fit
// in a 63-character label is cut and ends in 8 hex chars of SHA-256 of the
// full parent, so two long parents that agree up to the cut stay apart.
func PreviewName(parent string, number int32) string {
	suffix := "--pr-" + strconv.FormatInt(int64(number), 10)
	if len(parent)+len(suffix) <= maxLabel {
		return parent + suffix
	}
	sum := sha256.Sum256([]byte(parent))
	cut := strings.TrimRight(parent[:maxLabel-len(suffix)-hashLen-1], "-")
	return cut + "-" + hex.EncodeToString(sum[:])[:hashLen] + suffix
}

// HomeClaimName is the name of a user's home PersistentVolumeClaim.
func HomeClaimName(userSlug string) string {
	return "home-" + userSlug
}

// HomeClaimNameGen is the name of a home's claim in generation gen. The first
// generation keeps HomeClaimName, so a claim made before generations existed
// is adopted by name; a claim made by a later mode change adds the number.
func HomeClaimNameGen(userSlug string, gen int) string {
	if gen <= 1 {
		return HomeClaimName(userSlug)
	}
	return HomeClaimName(userSlug) + "-" + strconv.Itoa(gen)
}

// HomeClaimGen is the generation of claim, a name HomeClaimNameGen made for
// userSlug; 0 means the name is not one of that user's.
func HomeClaimGen(userSlug, claim string) int {
	rest, ok := strings.CutPrefix(claim, HomeClaimName(userSlug))
	if !ok {
		return 0
	}
	if rest == "" {
		return 1
	}
	gen, err := strconv.Atoi(strings.TrimPrefix(rest, "-"))
	if err != nil || !strings.HasPrefix(rest, "-") || gen < 2 {
		return 0
	}
	return gen
}

// FolderClaimName is the name of a group folder's PersistentVolumeClaim; name
// is the SharedFolder object name.
func FolderClaimName(name string) string {
	return "folder-" + name
}

// NamespaceName is the namespace "<prefix>-<suffix>". A name over 63
// characters is cut and ends in 8 hex chars of SHA-256 of the full name, so two
// long names that agree up to the cut stay apart. The result is a DNS-1123
// label when prefix and suffix are.
func NamespaceName(prefix, suffix string) string {
	full := prefix + "-" + suffix
	if len(full) <= maxLabel {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	cut := strings.TrimRight(full[:maxLabel-hashLen-1], "-")
	return cut + "-" + hex.EncodeToString(sum[:])[:hashLen]
}

// slugify case-maps with PRECIS, folds to ASCII, collapses every run of
// characters outside [a-z0-9] into one "-", and truncates to max.
func slugify(s string, max int) string {
	if mapped, err := precis.UsernameCaseMapped.String(s); err == nil {
		s = mapped
	} else {
		s = strings.ToLower(s)
	}
	var b strings.Builder
	pendingDash := false
	for _, r := range norm.NFKD.String(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		} else if r < 0x80 {
			pendingDash = true
		}
		// non-ASCII (including combining marks) is dropped
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
	}
	return strings.TrimRight(out, "-")
}

// maxGroupDir bounds the readable part of a group folder's directory name.
const maxGroupDir = 30

// GroupDir is the single path segment a group's folder is mounted under
// (/shared/<dir>): the last segment of the group path ("/org/data-science"
// gives "data-science"). A segment that is already a short DNS-1123 label is
// used as is; any other (case, spaces, unicode, too long, empty) is slugged
// and gets 8 hex chars of SHA-256 of the verbatim group, so "/Data Team" and
// "/data-team" stay apart. The mapping depends on the group alone, so a mount
// path never moves. Two paths with the same plain last segment share a dir;
// the folder reconciler offers only the older folder of such a pair.
func GroupDir(group string) string {
	seg := group
	if i := strings.LastIndex(strings.TrimRight(group, "/"), "/"); i >= 0 {
		seg = strings.TrimRight(group, "/")[i+1:]
	}
	seg = strings.TrimRight(seg, "/")
	slug := slugify(seg, maxGroupDir)
	if slug != "" && slug == seg {
		return slug
	}
	sum := sha256.Sum256([]byte(group))
	if slug == "" {
		slug = fallbackBase
	}
	return slug + "-" + hex.EncodeToString(sum[:])[:hashLen]
}
