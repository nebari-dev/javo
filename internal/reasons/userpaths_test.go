package reasons

import (
	"strings"
	"testing"
)

// Reasons, messages and notices are read by admins and users, who have no
// checkout of the repository: they point at the admin guide by name, never by
// a file path in the source tree.
func TestUserFacingCopyHasNoRepositoryPaths(t *testing.T) {
	check := func(code, s string) {
		if strings.Contains(s, "site/src") || strings.Contains(s, ".mdx") {
			t.Errorf("%s names a repository file: %q", code, s)
		}
	}
	for _, r := range All() {
		for _, s := range []string{r.Title, r.What, r.Todo, r.Blame, r.Who} {
			check(string(r.Code), s)
		}
	}
	for code, text := range AllMessages() {
		check(string(code), text)
	}
	for _, n := range AllNotices() {
		for _, s := range []string{n.Title, n.Text, n.Action, n.Detail} {
			check(string(n.Code), s)
		}
	}
}

// Home folders are backed up only by the backup bucket, which works on any
// disk, so the reason shown when it is not set up names the bucket's setting
// and nothing else to install.
func TestNoBackupToolNamesTheBucket(t *testing.T) {
	r, _ := Lookup(NoBackupTool)
	copy := r.What + " " + r.Todo
	for _, want := range []string{"S3-compatible bucket", "backupTarget", "any disk"} {
		if !strings.Contains(copy, want) {
			t.Errorf("NoBackupTool copy lacks %q: %q", want, copy)
		}
	}
	if strings.Contains(copy, "install ") || strings.Contains(copy, "Or ") {
		t.Errorf("NoBackupTool copy offers another tool: %q", copy)
	}
}
