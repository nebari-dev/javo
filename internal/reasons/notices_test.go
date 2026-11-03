package reasons

import (
	"strings"
	"testing"
)

func TestEveryNoticeHasCopyWithoutDashes(t *testing.T) {
	all := AllNotices()
	if len(all) == 0 {
		t.Fatal("no notices")
	}
	for _, n := range all {
		if strings.TrimSpace(n.Title) == "" || strings.TrimSpace(n.Text) == "" || strings.TrimSpace(n.Action) == "" {
			t.Errorf("%s: title, text and action must all be set: %+v", n.Code, n)
		}
		if n.Tone != ToneInfo && n.Tone != ToneWarn && n.Tone != ToneErr {
			t.Errorf("%s: tone %q", n.Code, n.Tone)
		}
		if strings.ContainsAny(n.Title+n.Text+n.Action+n.Detail, "—–") {
			t.Errorf("%s contains an em or en dash", n.Code)
		}
	}
}

func TestNoticeForFormatsEachFieldFromTheSameArguments(t *testing.T) {
	n := NoticeFor(AdminProbeFailed, "longhorn", "check")
	if !strings.Contains(n.Detail, "longhorn") || !strings.Contains(n.Detail, "check") {
		t.Errorf("detail = %q", n.Detail)
	}
	if strings.Contains(n.Title+n.Text+n.Action+n.Detail, "%!") {
		t.Errorf("a verb was left unformatted: %+v", n)
	}
}

func TestNoticeForUnknownCodeIsNotInvented(t *testing.T) {
	if n := NoticeFor(NoticeCode("Nope")); n.Title != "" || n.Text != "" {
		t.Fatalf("got %+v", n)
	}
}

func TestNoticeForWithoutArgsLeavesNoStrayVerbsInUserNotices(t *testing.T) {
	for _, c := range []NoticeCode{MigrationRunning, NoticeMigrationFailed, ScratchPaths} {
		n := NoticeFor(c)
		if strings.Contains(n.Title+n.Text+n.Action, "%") {
			t.Errorf("%s takes no arguments but has a verb: %+v", c, n)
		}
	}
}

// Admin notices are read by people who may not know Kubernetes: technical
// names belong in Detail, which the UI folds away.
func TestAdminNoticesKeepTechnicalNamesInDetail(t *testing.T) {
	jargon := []string{"StorageClass", "POSIX", "ReadWrite", "VolumeSnapshot", "->", "storage.home.mode", "allowVolumeExpansion", "NFS", "node", "perUser", "%q"}
	for _, n := range AllNotices() {
		if !strings.HasPrefix(string(n.Code), "Admin") && n.Code != NeedsNfsClient && n.Code != MinimumSizeBilled {
			continue
		}
		for _, j := range jargon {
			if strings.Contains(n.Title+" "+n.Text+" "+n.Action, j) {
				t.Errorf("%s says %q outside Detail: %+v", n.Code, j, n)
			}
		}
	}
	if d := NoticeFor(AdminHomePerUser, "local-path").Detail; !strings.Contains(d, "local-path") || !strings.Contains(d, "ReadWriteOnce") {
		t.Errorf("home detail = %q, want the class and access mode", d)
	}
}
