package reasons

import (
	"fmt"
	"strings"
	"time"
)

// NoticeCode names a storage notice: a banner or report line that says what
// is in use, what it means in plain words and the one action that changes it.
// Notices are the fourth group of the one catalog; handlers and the UI render
// them and never write banner text.
type NoticeCode string

// Tones of a notice. A report line that only states a fact is ToneInfo.
const (
	ToneInfo = "info"
	ToneWarn = "warn"
	ToneErr  = "err"
)

// Notices for users.
const (
	MigrationScheduled    NoticeCode = "MigrationScheduled" // args: where the files move to, date, minutes
	MigrationRunning      NoticeCode = "MigrationRunning"
	NoticeMigrationFailed NoticeCode = "MigrationFailed"
	ScratchPaths          NoticeCode = "ScratchPaths"
	PlacementFits         NoticeCode = "PlacementFits"  // args: machine, apps ("2 apps"), free CPUs
	PlacementWaits        NoticeCode = "PlacementWaits" // args: machine, apps ("2 apps"), free CPUs
)

// Notices for admins. Arguments are numbered in each text, so a notice uses
// only the ones it needs.
const (
	AdminSharedFoundNotVerified NoticeCode = "AdminSharedFoundNotVerified" // args: class, driver
	AdminSharedFoundTested      NoticeCode = "AdminSharedFoundTested"      // args: class, driver
	AdminNoSharedStorage        NoticeCode = "AdminNoSharedStorage"
	AdminSecondNodeJoined       NoticeCode = "AdminSecondNodeJoined"
	AdminModeMismatch           NoticeCode = "AdminModeMismatch" // args: homes left, where they move to
	AdminProbeFailed            NoticeCode = "AdminProbeFailed"  // args: class, failed step
	// AdminSharedHomesNoNfsClient and AdminHomesCannotMount: home folders the
	// machines failed to mount, from the homes or the storage test.
	AdminSharedHomesNoNfsClient NoticeCode = "AdminSharedHomesNoNfsClient" // arg: class
	AdminHomesCannotMount       NoticeCode = "AdminHomesCannotMount"       // arg: class
	AdminNoSnapshots            NoticeCode = "AdminNoSnapshots"
	AdminHomePerUser            NoticeCode = "AdminHomePerUser" // arg: class
	AdminHomeShared             NoticeCode = "AdminHomeShared"  // arg: class
	AdminFoldersOn              NoticeCode = "AdminFoldersOn"   // arg: class
	AdminFoldersOff             NoticeCode = "AdminFoldersOff"  //
	AdminSnapshotsOn            NoticeCode = "AdminSnapshotsOn" // arg: class
	AdminSizeGrows              NoticeCode = "AdminSizeGrows"   // arg: class
	AdminSizeFixed              NoticeCode = "AdminSizeFixed"   // arg: class
	AdminSizeNotEnforced        NoticeCode = "AdminSizeNotEnforced"
	AdminProbePassed            NoticeCode = "AdminProbePassed"   // args: class, nodes tested
	AdminMoveScheduled          NoticeCode = "AdminMoveScheduled" // args: where homes move to, date
	AdminNotDetected            NoticeCode = "AdminNotDetected"

	// The class notes the capability table attaches to a class use the note's
	// own name as their code.
	NeedsNfsClient    NoticeCode = "NeedsNfsClient"    // arg: class
	MinimumSizeBilled NoticeCode = "MinimumSizeBilled" // arg: class
)

// Notice is one rendered catalog entry.
type Notice struct {
	Code   NoticeCode `json:"code"`
	Tone   string     `json:"tone"`
	Title  string     `json:"title"`
	Text   string     `json:"text"`
	Action string     `json:"action"`
	// Detail holds the technical names (classes, access modes, settings and
	// commands) for an admin; the UI folds it away under Details.
	Detail string `json:"detail,omitempty"`
}

var notices = []Notice{
	{Code: MigrationScheduled, Tone: ToneWarn, Title: "Your files are moving",
		Text:   "Your files move to %[1]s on %[2]s. Your apps will be stopped for about %[3]d minutes. They start again by themselves when the move is done.",
		Action: "Save your work before then."},
	{Code: MigrationRunning, Tone: ToneWarn, Title: "Your files are being moved",
		Text:   "Your apps stay stopped until the move is done, then they start by themselves.",
		Action: "Nothing to do. Wait for the move to finish."},
	{Code: NoticeMigrationFailed, Tone: ToneErr, Title: "Your files could not be moved",
		Text:   "The move stopped and your files are still where they were. Nothing was lost, and your apps can start.",
		Action: "Ask an admin to look at the move."},
	{Code: ScratchPaths, Tone: ToneInfo, Title: "Some folders are not saved",
		Text:   "Caches and environments are rebuilt each time an app starts and are not saved.",
		Action: "Keep anything you need in your home folder."},
	{Code: PlacementFits, Tone: ToneInfo, Title: "Will run next to your other apps",
		Text:   "You have %[2]s on machine %[1]s with %[3]s CPUs free. This app fits.",
		Action: "Nothing to do."},
	{Code: PlacementWaits, Tone: ToneWarn, Title: "Will run next to your other apps",
		Text:   "You have %[2]s on machine %[1]s with %[3]s CPUs free. This app will wait until you stop something.",
		Action: "Stop an app to make room, or ask an admin to enable shared storage."},

	{Code: AdminSharedFoundNotVerified, Tone: ToneInfo, Title: "Shared storage is available but not tested",
		Text:   "With it, a person's apps could run on any machine instead of all on one.",
		Action: "To use it, test it, then switch home folders to it in the deployment settings.",
		Detail: "StorageClass %[1]s (%[2]s). Test it with javo storage verify %[1]s, then set storage.home.mode to shared and run javo storage migrate."},
	{Code: AdminSharedFoundTested, Tone: ToneInfo, Title: "Shared storage is available and tested",
		Text:   "With it, a person's apps could run on any machine instead of all on one.",
		Action: "To use it, switch home folders to it in the deployment settings.",
		Detail: "StorageClass %[1]s (%[2]s) passed javo storage verify. Set storage.home.mode to shared, then run javo storage migrate."},
	{Code: AdminNoSharedStorage, Tone: ToneWarn, Title: "Each person's apps must run on one machine",
		Text:   "No storage here can be used by several machines at once, so a person's apps all run where their files are, and groups have no shared folders.",
		Action: "To change this, add shared storage to the cluster and test it.",
		Detail: "No StorageClass offers ReadWriteMany files with POSIX ownership. Add one, then run javo storage verify <class>."},
	{Code: AdminSecondNodeJoined, Tone: ToneWarn, Title: "A second machine joined the cluster",
		Text:   "Home folders made before it joined are on one machine's disk, so their owners' apps can only run there.",
		Action: "To change this, add shared storage and move home folders to it.",
		Detail: "Homes made under the single-node assumption are ReadWriteOnce volumes. Set storage.home.mode to shared, then run javo storage migrate."},
	{Code: AdminModeMismatch, Tone: ToneWarn, Title: "Home folders are waiting to move",
		Text:   "The storage setting changed to %[2]s, but %[1]d home folders are still on the old storage.",
		Action: "Run javo storage migrate to move them.",
		Detail: "javo storage migrate plan shows the move and javo storage migrate schedule --at <time> starts it. Each owner's apps stop while their files copy."},
	{Code: AdminSharedHomesNoNfsClient, Tone: ToneErr, Title: "Shared homes cannot be mounted on these machines",
		Text:   "The machines have no file sharing client, so they cannot open home folders on the shared storage and apps do not start.",
		Action: "Keep each home folder on its own machine's disk instead, or install the client on every machine.",
		Detail: "Mounting StorageClass %[1]s failed for want of a mount helper (no NFS client on the node). Set storage.home.mode: perUser, or install nfs-common (or nfs-utils) on every node, then run javo storage verify %[1]s."},
	{Code: AdminHomesCannotMount, Tone: ToneErr, Title: "Home folders cannot be mounted",
		Text:   "The machines failed to mount some home folders, so their owners' apps do not start.",
		Action: "Fix the cause the evidence shows, then run the storage test again.",
		Detail: "Volumes of StorageClass %[1]s failed to attach or mount. Run javo storage verify %[1]s after fixing it."},
	{Code: AdminProbeFailed, Tone: ToneErr, Title: "The shared storage test failed",
		Text:   "This storage is not used for home folders until the test passes.",
		Action: "Fix the cause on the cluster, then run the test again.",
		Detail: "StorageClass %[1]s failed at the %[2]s step. Run it again with javo storage verify %[1]s."},
	{Code: AdminNoSnapshots, Tone: ToneInfo, Title: "Backups copy files one by one",
		Text:   "This storage cannot take instant copies, so backups of home folders are slower.",
		Action: "To change this, add snapshot support to the cluster.",
		Detail: "No VolumeSnapshotClass was found. Install a snapshot controller and a VolumeSnapshotClass for the home StorageClass."},
	{Code: AdminHomePerUser, Tone: ToneInfo, Title: "Each person's files are on one machine's disk",
		Text:   "A person's apps all run on the machine that holds their files.",
		Action: "To let apps run on any machine, add shared storage and switch home folders to it.",
		Detail: "StorageClass %[1]s, one ReadWriteOnce volume per person (storage.home.mode perUser)."},
	{Code: AdminHomeShared, Tone: ToneInfo, Title: "Files are on shared storage",
		Text:   "Every machine can reach every home folder, so apps can run anywhere.",
		Action: "Nothing to do.",
		Detail: "StorageClass %[1]s, one ReadWriteMany drive with a folder per person (storage.home.mode shared)."},
	{Code: AdminFoldersOn, Tone: ToneInfo, Title: "Groups have shared folders",
		Text:   "Each group has a folder its members can open from any app.",
		Action: "Nothing to do.",
		Detail: "Folders at /shared/<group> on StorageClass %[1]s."},
	{Code: AdminFoldersOff, Tone: ToneInfo, Title: "Groups have no shared folders",
		Text:   "Group folders need storage that several machines can use at once.",
		Action: "To change this, add shared storage to the cluster and test it.",
		Detail: "No StorageClass offers ReadWriteMany files with POSIX ownership."},
	{Code: AdminSnapshotsOn, Tone: ToneInfo, Title: "Backups use instant copies",
		Text:   "This storage can copy a home folder in one step, so backups include people's files.",
		Action: "Nothing to do.",
		Detail: "StorageClass %[1]s has a VolumeSnapshotClass."},
	{Code: AdminSizeGrows, Tone: ToneInfo, Title: "Home folders can grow",
		Text:   "A home folder can be made larger, never smaller.",
		Action: "To grow them, raise the home size in the deployment settings.",
		Detail: "StorageClass %[1]s has allowVolumeExpansion true."},
	{Code: AdminSizeFixed, Tone: ToneInfo, Title: "Home folders keep their size",
		Text:   "A home folder stays the size it was made with.",
		Action: "To let them grow, use storage that allows resizing.",
		Detail: "StorageClass %[1]s has allowVolumeExpansion false."},
	{Code: AdminSizeNotEnforced, Tone: ToneInfo, Title: "Home folders have no size limit",
		Text:   "People can fill the disk, and they see no usage bar.",
		Action: "To set limits, use storage that enforces size.",
		Detail: "StorageClass %[1]s does not enforce volume capacity."},
	{Code: AdminProbePassed, Tone: ToneInfo, Title: "The shared storage test passed",
		Text:   "Files were shared correctly across %[2]d machines.",
		Action: "Nothing to do.",
		Detail: "StorageClass %[1]s passed javo storage verify."},
	{Code: AdminMoveScheduled, Tone: ToneWarn, Title: "Home folders will move",
		Text:   "Home folders move to %[1]s on %[2]s. People see a notice, and their apps stop while their files copy.",
		Action: "Run javo storage migrate status to follow it."},
	{Code: AdminNotDetected, Tone: ToneWarn, Title: "Storage not checked yet",
		Text:   "The controller has not looked at the cluster's storage yet, so nothing is known about it.",
		Action: "Check that the javo controller is running."},
	{Code: NeedsNfsClient, Tone: ToneWarn, Title: "Every machine needs a file sharing client",
		Text:   "The shared storage works only on machines that have it, and a machine without it cannot open home folders.",
		Action: "Install it on each machine, then run the storage test again.",
		Detail: "StorageClass %[1]s needs an NFSv4 client on every node. Then run javo storage verify %[1]s."},
	{Code: MinimumSizeBilled, Tone: ToneInfo, Title: "Small home folders cost a minimum size",
		Text:   "The storage provider bills every home folder at a minimum size.",
		Action: "Set the default home size to at least that minimum.",
		Detail: "StorageClass %[1]s bills a minimum size per volume."},
}

// NoticeFor returns the notice for code with args filled in. Texts number
// their arguments, so a notice that needs fewer is not given junk. An unknown
// code returns the zero Notice.
func NoticeFor(code NoticeCode, args ...any) Notice {
	for _, n := range notices {
		if n.Code != code {
			continue
		}
		if len(args) > 0 {
			n.Title, n.Text, n.Action, n.Detail = fill(n.Title, args), fill(n.Text, args), fill(n.Action, args), fill(n.Detail, args)
		}
		return n
	}
	return Notice{}
}

// AllNotices returns every notice with its argument verbs unfilled.
func AllNotices() []Notice { return append([]Notice(nil), notices...) }

// fill formats s only when it has verbs, so a field that needs no argument is
// not given a stray "%!(EXTRA" tail.
func fill(s string, args []any) string {
	if !strings.Contains(s, "%") {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// WhenFormat is how a moment is written for people, always in UTC.
const WhenFormat = "2 Jan 2006, 15:04 UTC"

// FormatWhen writes t for a notice: "2 Nov 2026, 22:00 UTC".
func FormatWhen(t time.Time) string { return t.UTC().Format(WhenFormat) }

// ModeWords says where files are in plain words, for sentences such as
// "Your files move to shared storage".
func ModeWords(shared bool) string {
	if shared {
		return Msg(MsgModeWordsShared)
	}
	return Msg(MsgModeWordsPerUser)
}
