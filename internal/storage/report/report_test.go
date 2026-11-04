package report_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/report"
)

func fact(v bool, p model.Provenance) model.Fact { return model.Fact{Value: v, Provenance: p} }

// hetzner mirrors a cluster with per-machine disks and a Longhorn class that
// could hold shared homes but was never tested.
func hetzner() *model.ClusterStorage {
	return &model.ClusterStorage{Status: model.ClusterStorageStatus{
		Nodes: 3,
		Classes: []model.ClassCapability{
			{Name: "hcloud-volumes", Provisioner: "csi.hetzner.cloud", IsDefault: true,
				RWXFile: fact(false, "known"), PosixOwnership: fact(true, "known"), SizeEnforced: fact(true, "known"),
				Expansion: fact(true, "known"), Snapshots: fact(false, "known")},
			{Name: "longhorn", Provisioner: "driver.longhorn.io",
				RWXFile: fact(true, "known"), PosixOwnership: fact(true, "known"), SizeEnforced: fact(true, "known"),
				Expansion: fact(true, "known"), Snapshots: fact(false, "known"), Notes: []string{"NeedsNfsClient"}},
		},
		Choice: &model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "hcloud-volumes", SharedClass: "longhorn", Source: "auto"},
	}}
}

func byFact(rows []report.Row, f string) *report.Row {
	if i := slices.IndexFunc(rows, func(r report.Row) bool { return r.Fact == f }); i >= 0 {
		return &rows[i]
	}
	return nil
}

func TestEveryRowSaysWhereItsFactComesFrom(t *testing.T) {
	rows := report.Build(hetzner(), nil)
	home := byFact(rows, "home")
	if home == nil || home.Notice.Code != reasons.AdminHomePerUser || home.Provenance != "auto" {
		t.Fatalf("home row = %+v", home)
	}
	shared := byFact(rows, "shared")
	if shared == nil || shared.Notice.Code != reasons.AdminSharedFoundNotVerified || shared.Provenance != "known" {
		t.Fatalf("shared row = %+v", shared)
	}
	if folders := byFact(rows, "folders"); folders == nil || folders.Notice.Code != reasons.AdminFoldersOn || folders.Provenance != "known" {
		t.Fatalf("folders row = %+v", folders)
	}
	if snaps := byFact(rows, "snapshots"); snaps == nil || snaps.Notice.Code != reasons.AdminNoSnapshots {
		t.Fatalf("snapshots row = %+v", snaps)
	}
	if size := byFact(rows, "size"); size == nil || size.Notice.Code != reasons.AdminSizeGrows {
		t.Fatalf("size row = %+v", size)
	}
	if nfs := byFact(rows, "note:NeedsNfsClient"); nfs == nil || !nfs.Attention {
		t.Fatalf("nfs row = %+v", nfs)
	}
}

func TestRowsNeedingAttentionComeFirst(t *testing.T) {
	cs := hetzner()
	cs.Status.Probe = &model.ProbeStatus{StorageClassName: "longhorn", Result: model.ProbeFailed, FailedStep: "check", Evidence: []string{"pod x: FailedMount"}}
	rows := report.Build(cs, nil)
	seenOK := false
	for _, r := range rows {
		if !r.Attention {
			seenOK = true
		} else if seenOK {
			t.Fatalf("attention row %q after a row that needs none: %+v", r.Fact, rows)
		}
	}
	probe := byFact(rows, "probe")
	if probe == nil || probe.Notice.Code != reasons.AdminProbeFailed || probe.Provenance != "verified" || !slices.Contains(probe.Evidence, "pod x: FailedMount") {
		t.Fatalf("probe row = %+v", probe)
	}
	if rows[0].Fact != "probe" {
		t.Errorf("an error comes before a warning: first = %q", rows[0].Fact)
	}
}

func TestClusterWithoutSharedStorageSaysSo(t *testing.T) {
	cs := hetzner()
	cs.Status.Classes = cs.Status.Classes[:1]
	cs.Status.Choice.SharedClass = ""
	rows := report.Build(cs, nil)
	if r := byFact(rows, "shared"); r == nil || r.Notice.Code != reasons.AdminNoSharedStorage || !r.Attention {
		t.Fatalf("shared row = %+v", r)
	}
	if r := byFact(rows, "folders"); r == nil || r.Notice.Code != reasons.AdminFoldersOff {
		t.Fatalf("folders row = %+v", r)
	}
}

// One machine with its local disk limits nobody: only the size row asks for
// attention, since a home that fills the disk takes the database and every
// other pod on that disk down with it.
func TestOneMachineWithALocalDiskNeedsOnlyASizeLimit(t *testing.T) {
	cs := &model.ClusterStorage{Status: model.ClusterStorageStatus{
		Nodes: 1,
		Classes: []model.ClassCapability{{Name: "local-path", Provisioner: "rancher.io/local-path", IsDefault: true,
			RWXFile: fact(false, "known"), SizeEnforced: fact(false, "known"), Snapshots: fact(false, "known")}},
		Choice: &model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "local-path", Source: "auto", SingleNode: true},
	}}
	rows := report.Build(cs, nil)
	for _, r := range rows {
		if r.Attention != (r.Fact == "size") {
			t.Errorf("row %q attention %v on one machine: %+v", r.Fact, r.Attention, r)
		}
	}
	if r := byFact(rows, "size"); r == nil || r.Notice.Code != reasons.AdminSizeNotEnforced || r.Notice.Tone != reasons.ToneWarn {
		t.Errorf("size row = %+v", r)
	}
	if s := report.Summary(cs, rows); !contains(s, "this machine's disk") {
		t.Errorf("summary = %q", s)
	}
}

// Several machines with shared storage nobody uses yet: the shared row is
// what an admin can act on, so it asks for attention.
func TestUnusedSharedStorageOnSeveralMachinesAsksForAttention(t *testing.T) {
	cs := hetzner()
	rows := report.Build(cs, nil)
	if r := byFact(rows, "shared"); r == nil || !r.Attention {
		t.Fatalf("shared row = %+v", r)
	}
	if s := report.Summary(cs, rows); s == "" || contains(s, "Nothing needs attention") {
		t.Errorf("summary = %q", s)
	}
}

// A passed probe on a class the admin named for homes, or with the mode
// overridden, leaves per-user mode with a shared class that is verified; the
// row must not say "not tested" next to [verified].
func TestVerifiedSharedStorageInPerUserModeIsNotCalledUntested(t *testing.T) {
	cs := hetzner()
	cs.Status.Classes[1].RWXFile = fact(true, model.ProvenanceVerified)
	r := byFact(report.Build(cs, nil), "shared")
	if r == nil || r.Notice.Code != reasons.AdminSharedFoundTested || r.Provenance != string(model.ProvenanceVerified) {
		t.Fatalf("shared row = %+v", r)
	}
}

func TestConditionsBecomeRows(t *testing.T) {
	cs := hetzner()
	cs.Status.Conditions = []metav1.Condition{
		{Type: model.ConditionSingleNodeAssumption, Status: metav1.ConditionTrue, Reason: string(reasons.SecondNodeJoined), Message: "2 perUser homes made on one node, 3 nodes now"},
		{Type: model.ConditionHomeModeMismatch, Status: metav1.ConditionTrue, Reason: string(reasons.HomeModeMismatch)},
		{Type: model.ConditionHomeStorageReady, Status: metav1.ConditionFalse, Reason: string(reasons.NoDefaultStorageClass), Message: "no class (0 found)"},
	}
	cs.Status.Migration = &model.MigrationStatus{From: model.HomeModePerUser, To: model.HomeModeShared, Users: 4, Done: 1}
	rows := report.Build(cs, nil)
	if r := byFact(rows, "nodes"); r == nil || r.Notice.Code != reasons.AdminSecondNodeJoined || !r.Attention {
		t.Fatalf("nodes row = %+v", r)
	}
	if r := byFact(rows, "mismatch"); r == nil || r.Notice.Code != reasons.AdminModeMismatch {
		t.Fatalf("mismatch row = %+v", r)
	} else if want := "3 home folders are still on the old storage"; !contains(r.Notice.Text, want) {
		t.Errorf("mismatch text = %q, want it to say %q", r.Notice.Text, want)
	}
	ready := byFact(rows, "ready")
	if ready == nil || ready.Notice.Title != mustReason(t, reasons.NoDefaultStorageClass).Title || !slices.Contains(ready.Evidence, "no class (0 found)") {
		t.Fatalf("ready row = %+v", ready)
	}
}

func TestAScheduledMoveIsReported(t *testing.T) {
	cs := hetzner()
	at := metav1.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)
	cs.Spec.Migration = model.MigrationSpec{StartAt: &at, To: model.HomeModeShared}
	if r := byFact(report.Build(cs, nil), "move"); r == nil || r.Notice.Code != reasons.AdminMoveScheduled || !contains(r.Notice.Text, "2 Nov 2026") {
		t.Fatalf("move row = %+v", r)
	}
}

func TestNoChoiceYetHasOneRowAndNoCrash(t *testing.T) {
	rows := report.Build(&model.ClusterStorage{}, nil)
	if len(rows) != 1 || rows[0].Fact != "home" || !rows[0].Attention {
		t.Fatalf("rows = %+v", rows)
	}
}

func mustReason(t *testing.T, c reasons.Code) reasons.Reason {
	t.Helper()
	r, ok := reasons.Lookup(c)
	if !ok {
		t.Fatalf("no reason %s", c)
	}
	return r
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// The attach limit caps homes per node only when each home is its own disk.
func TestHomesPerNodeIsShownOnlyForPerUserDisks(t *testing.T) {
	perNode := int32(16)
	cs := hetzner()
	cs.Status.Classes[0].VolumesPerNode = &perNode
	if r := byFact(report.Build(cs, nil), "home"); r == nil || !slices.Contains(r.Evidence, "Up to 16 user homes per node (CSINode allocatable count for csi.hetzner.cloud).") {
		t.Fatalf("per-user home row = %+v", r)
	}
	cs.Status.Classes[0].RWXFile = fact(true, "known")
	cs.Status.Choice.Mode = model.HomeModeShared
	if r := byFact(report.Build(cs, nil), "home"); r == nil || len(r.Evidence) != 0 {
		t.Fatalf("shared home row = %+v", r)
	}
}

// The shared row names its class, so a reader of the rows need not parse
// it out of the text.
func TestTheSharedRowNamesTheClassToVerify(t *testing.T) {
	r := byFact(report.Build(hetzner(), nil), "shared")
	if r == nil || r.Class != "longhorn" {
		t.Fatalf("shared row = %+v", r)
	}
	// A cost note names its class too.
	if n := byFact(report.Build(hetzner(), nil), "note:NeedsNfsClient"); n == nil || n.Class != "longhorn" {
		t.Fatalf("note row = %+v", n)
	}
}

// sharedLonghorn is hetzner with homes chosen on the shared Longhorn class.
func sharedLonghorn() *model.ClusterStorage {
	cs := hetzner()
	cs.Status.Choice = &model.StorageChoice{Mode: model.HomeModeShared, HomeClass: "longhorn", SharedClass: "longhorn", Source: "auto"}
	return cs
}

func homeFailing(owner string, code reasons.Code, msg string) model.Home {
	return model.Home{Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{Conditions: []metav1.Condition{
		{Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: string(code), Message: msg},
	}}}
}

const noHelper = "FailedMount javo-prepare-home-u-1-x1: MountVolume.MountDevice failed: mount: bad option; for several filesystems (e.g. nfs, cifs) you might need a /sbin/mount.<type> helper program."

// A shared home the machines cannot mount for want of an NFS client says so
// and names the setting that avoids it, instead of the guess the driver table made.
func TestSharedHomesTheMachinesCannotMountAskForPerUser(t *testing.T) {
	homes := []model.Home{homeFailing("u-1", reasons.HomeMountFailed, noHelper), homeFailing("u-2", reasons.HomePreparing, "")}
	rows := report.Build(sharedLonghorn(), homes)
	r := byFact(rows, "mount")
	if r == nil || r.Notice.Code != reasons.AdminSharedHomesNoNfsClient || !r.Attention || rows[0].Fact != "mount" {
		t.Fatalf("mount row = %+v (first row %q)", r, rows[0].Fact)
	}
	if !contains(r.Notice.Detail, "storage.home.mode: perUser") || !slices.ContainsFunc(r.Evidence, func(l string) bool { return contains(l, "u-1") && contains(l, "helper program") }) {
		t.Fatalf("mount row = %+v", r)
	}
}

// The same finding from the opt-in storage test, before any home failed.
func TestAProbeThatCouldNotMountForWantOfAHelperAsksForPerUser(t *testing.T) {
	cs := sharedLonghorn()
	cs.Status.ProbeResults = []model.ProbeStatus{{StorageClassName: "longhorn", Result: model.ProbeFailed, FailedStep: "mount", Evidence: []string{noHelper}}}
	if r := byFact(report.Build(cs, nil), "mount"); r == nil || r.Notice.Code != reasons.AdminSharedHomesNoNfsClient {
		t.Fatalf("mount row = %+v", r)
	}
}

// A mount failure with another cause is reported as it is, with no advice
// the evidence does not support.
func TestHomesThatCannotBeMountedForAnotherReasonAreReportedPlainly(t *testing.T) {
	homes := []model.Home{homeFailing("u-1", reasons.HomeMountFailed, "FailedAttachVolume x: AttachVolume.Attach failed: volume is not ready")}
	r := byFact(report.Build(sharedLonghorn(), homes), "mount")
	if r == nil || r.Notice.Code != reasons.AdminHomesCannotMount || contains(r.Notice.Detail, "perUser") {
		t.Fatalf("mount row = %+v", r)
	}
}

func TestNoMountRowWhenEveryHomeMounts(t *testing.T) {
	if r := byFact(report.Build(sharedLonghorn(), []model.Home{homeFailing("u-2", reasons.HomePreparing, "")}), "mount"); r != nil {
		t.Fatalf("mount row = %+v", r)
	}
}
