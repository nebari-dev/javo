package model

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// HomeMode is how users' home directories are stored.
type HomeMode string

const (
	// HomeModeAuto lets the platform pick from what the cluster offers.
	HomeModeAuto HomeMode = "auto"
	// HomeModePerUser gives each user a disk of their own.
	HomeModePerUser HomeMode = "perUser"
	// HomeModeShared puts every home on one shared drive.
	HomeModeShared HomeMode = "shared"
)

// Provenance says where a capability fact comes from.
type Provenance string

const (
	// ProvenanceKnown: read from the known-driver table.
	ProvenanceKnown Provenance = "known"
	// ProvenanceOverride: set by an admin in storage.classOverrides.
	ProvenanceOverride Provenance = "override"
	// ProvenanceVerified: proven by the probe.
	ProvenanceVerified Provenance = "verified"
	// ProvenanceUnknown: nothing is known.
	ProvenanceUnknown Provenance = "unknown"
)

// Condition types of the cluster's storage status.
const (
	ConditionHomeStorageReady     = "HomeStorageReady"
	ConditionSharedFoldersReady   = "SharedFoldersReady"
	ConditionProbePassed          = "ProbePassed"
	ConditionHomeModeMismatch     = "HomeModeMismatch"
	ConditionSingleNodeAssumption = "SingleNodeAssumption"
)

// HomeStorageSpec is the admin's choice for home directories.
// +kubebuilder:object:generate=true
type HomeStorageSpec struct {
	Mode HomeMode `json:"mode,omitempty"`
	// StorageClassName overrides the class picked for homes.
	StorageClassName string `json:"storageClassName,omitempty"`
	// Size is the default home size.
	Size *resource.Quantity `json:"size,omitempty"`
	// MaxSize caps the size a home can be given.
	MaxSize *resource.Quantity `json:"maxSize,omitempty"`
}

// SharedFoldersSpec is the admin's choice for group folders.
// +kubebuilder:object:generate=true
type SharedFoldersSpec struct {
	StorageClassName string             `json:"storageClassName,omitempty"`
	DefaultSize      *resource.Quantity `json:"defaultSize,omitempty"`
}

// TeamStorageSpec is the storage allowance of a team namespace (per-team
// namespace mode only).
// +kubebuilder:object:generate=true
type TeamStorageSpec struct {
	// Limit is the total storage a team's claims may request, for a team
	// whose own limit is not set. Empty means no allowance is enforced.
	Limit *resource.Quantity `json:"limit,omitempty"`
}

// ScratchSpec is the node-local scratch space of every app pod.
// +kubebuilder:object:generate=true
type ScratchSpec struct {
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`
}

// ClassOverride lets an admin state a fact the platform cannot detect.
// +kubebuilder:object:generate=true
type ClassOverride struct {
	Name           string `json:"name"`
	RWXFile        *bool  `json:"rwxFile,omitempty"`
	PosixOwnership *bool  `json:"posixOwnership,omitempty"`
	SizeEnforced   *bool  `json:"sizeEnforced,omitempty"`
}

// ProbeSpec asks for the opt-in two-pod probe. The probe never runs unless
// an admin sets a nonce.
// +kubebuilder:object:generate=true
type ProbeSpec struct {
	StorageClassName string `json:"storageClassName,omitempty"`
	// Nonce starts a probe run when it changes.
	Nonce   string           `json:"nonce,omitempty"`
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

// MigrationSpec schedules a mode change.
// +kubebuilder:object:generate=true
type MigrationSpec struct {
	// StartAt schedules the copy of every home to the mode named in To.
	StartAt *metav1.Time `json:"startAt,omitempty"`
	// To is the mode the schedule is for (set when the move is scheduled, to
	// the cluster's chosen mode). The schedule applies only while the chosen
	// mode is still this one, so a later mode change never moves homes
	// without a new schedule.
	To HomeMode `json:"to,omitempty"`
	// KeepOldFor is how long the old claim is kept after the switch.
	KeepOldFor *metav1.Duration `json:"keepOldFor,omitempty"`
	// Attempt is raised by an admin to retry a failed migration.
	Attempt int32 `json:"attempt,omitempty"`
}

// ClusterStorageSpec is what the admin chose. Home, SharedFolders, Scratch,
// TeamStorage, ClassOverrides and Migration.KeepOldFor come from the
// platform config (Helm values); Probe and the rest of Migration are the
// requests an admin makes at run time, kept in the database
// (StorageRequests). Every block defaults, so an empty spec is valid.
// +kubebuilder:object:generate=true
type ClusterStorageSpec struct {
	Home          HomeStorageSpec   `json:"home,omitempty"`
	SharedFolders SharedFoldersSpec `json:"sharedFolders,omitempty"`
	Scratch       ScratchSpec       `json:"scratch,omitempty"`
	// TeamStorage is the default allowance of each team namespace.
	TeamStorage    TeamStorageSpec `json:"teamStorage,omitempty"`
	ClassOverrides []ClassOverride `json:"classOverrides,omitempty"`
	Probe          ProbeSpec       `json:"probe,omitempty"`
	Migration      MigrationSpec   `json:"migration,omitempty"`
}

// Storage defaults, applied by ApplyDefaults.
const (
	DefaultHomeSize      = "10Gi"
	DefaultFolderSize    = "20Gi"
	DefaultScratchSize   = "20Gi"
	DefaultProbeTimeout  = 5 * time.Minute
	DefaultKeepOldClaims = 168 * time.Hour
)

// ApplyDefaults fills every unset field with its default.
func (s *ClusterStorageSpec) ApplyDefaults() {
	if s.Home.Mode == "" {
		s.Home.Mode = HomeModeAuto
	}
	setQuantity(&s.Home.Size, DefaultHomeSize)
	setQuantity(&s.SharedFolders.DefaultSize, DefaultFolderSize)
	setQuantity(&s.Scratch.SizeLimit, DefaultScratchSize)
	if s.Probe.Timeout == nil {
		s.Probe.Timeout = &metav1.Duration{Duration: DefaultProbeTimeout}
	}
	if s.Migration.KeepOldFor == nil {
		s.Migration.KeepOldFor = &metav1.Duration{Duration: DefaultKeepOldClaims}
	}
}

func setQuantity(q **resource.Quantity, def string) {
	if *q == nil {
		v := resource.MustParse(def)
		*q = &v
	}
}

// Validate checks the admin's settings; each problem is addressed by its
// path under storage.
func (s *ClusterStorageSpec) Validate() error {
	var errs []error
	switch s.Home.Mode {
	case "", HomeModeAuto, HomeModePerUser, HomeModeShared:
	default:
		errs = append(errs, fmt.Errorf("storage.home.mode: %q is not one of auto, perUser, shared", s.Home.Mode))
	}
	if s.Home.Size != nil && s.Home.MaxSize != nil && s.Home.Size.Cmp(*s.Home.MaxSize) > 0 {
		errs = append(errs, errors.New("storage.home.size: size is larger than maxSize"))
	}
	for i, o := range s.ClassOverrides {
		if o.Name == "" {
			errs = append(errs, fmt.Errorf("storage.classOverrides[%d].name: must not be empty", i))
		}
	}
	if d := s.Probe.Timeout; d != nil && d.Duration < 0 {
		errs = append(errs, errors.New("storage.probe.timeout: must be a duration such as 5m"))
	}
	if d := s.Migration.KeepOldFor; d != nil && d.Duration < 0 {
		errs = append(errs, errors.New("storage.migration.keepOldFor: must be a duration such as 168h"))
	}
	return errors.Join(errs...)
}

// StorageRequests is what an admin asks of the storage at run time: a probe
// of a class, and the scheduled move of homes. The database holds it; the
// platform config holds everything else in ClusterStorageSpec.
// +kubebuilder:object:generate=true
type StorageRequests struct {
	Probe     ProbeSpec     `json:"probe,omitempty"`
	Migration MigrationSpec `json:"migration,omitempty"`
}

// WithRequests returns s with the admin's run-time requests in place. The
// probe timeout falls back to the configured one; the time old claims are
// kept is always the configured one.
func (s ClusterStorageSpec) WithRequests(r StorageRequests) ClusterStorageSpec {
	timeout, keep := s.Probe.Timeout, s.Migration.KeepOldFor
	s.Probe, s.Migration = r.Probe, r.Migration
	if s.Probe.Timeout == nil {
		s.Probe.Timeout = timeout
	}
	s.Migration.KeepOldFor = keep
	return s
}

// Fact is one capability value and where it comes from.
// +kubebuilder:object:generate=true
type Fact struct {
	Value      bool       `json:"value"`
	Provenance Provenance `json:"provenance"`
}

// ClassCapability is what the platform knows about one StorageClass.
// +kubebuilder:object:generate=true
type ClassCapability struct {
	Name           string `json:"name"`
	Provisioner    string `json:"provisioner,omitempty"`
	IsDefault      bool   `json:"isDefault,omitempty"`
	RWXFile        Fact   `json:"rwxFile"`
	PosixOwnership Fact   `json:"posixOwnership"`
	SizeEnforced   Fact   `json:"sizeEnforced"`
	Expansion      Fact   `json:"expansion"`
	Snapshots      Fact   `json:"snapshots"`
	Zonal          bool   `json:"zonal,omitempty"`
	BindingMode    string `json:"bindingMode,omitempty"`
	// VolumesPerNode is the CSI attach limit, when the driver reports one.
	VolumesPerNode *int32   `json:"volumesPerNode,omitempty"`
	Notes          []string `json:"notes,omitempty"`
}

// ChoiceSource says who made the mode choice.
type ChoiceSource string

// StorageChoice is the mode and classes in effect.
// +kubebuilder:object:generate=true
type StorageChoice struct {
	Mode        HomeMode     `json:"mode,omitempty"`
	SingleNode  bool         `json:"singleNode,omitempty"`
	HomeClass   string       `json:"homeClass,omitempty"`
	SharedClass string       `json:"sharedClass,omitempty"`
	Source      ChoiceSource `json:"source,omitempty"`
}

// ProbeResult is the outcome of a probe run.
type ProbeResult string

const (
	ProbePassed  ProbeResult = "Passed"
	ProbeFailed  ProbeResult = "Failed"
	ProbeRunning ProbeResult = "Running"
)

// ProbeStatus is the last probe run.
// +kubebuilder:object:generate=true
type ProbeStatus struct {
	StorageClassName string      `json:"storageClassName,omitempty"`
	Nonce            string      `json:"nonce,omitempty"`
	Result           ProbeResult `json:"result,omitempty"`
	// Step is the probe step in progress while Result is Running.
	Step        string       `json:"step,omitempty"`
	FailedStep  string       `json:"failedStep,omitempty"`
	Evidence    []string     `json:"evidence,omitempty"`
	FinishedAt  *metav1.Time `json:"finishedAt,omitempty"`
	NodesTested int32        `json:"nodesTested,omitempty"`
}

// MigrationStatus is the progress of a scheduled mode change.
// +kubebuilder:object:generate=true
type MigrationStatus struct {
	From           HomeMode `json:"from,omitempty"`
	To             HomeMode `json:"to,omitempty"`
	Users          int32    `json:"users,omitempty"`
	RequestedBytes int64    `json:"requestedBytes,omitempty"`
	Done           int32    `json:"done,omitempty"`
	Failed         int32    `json:"failed,omitempty"`
}

// ClusterStorageStatus is what the platform found and did. It is derived and
// can be rebuilt; nothing here is the source of truth. The cluster storage
// controller writes everything but Probe and ProbeResults, which the probe
// controller writes, so the two never overwrite each other.
// +kubebuilder:object:generate=true
type ClusterStorageStatus struct {
	Nodes   int32             `json:"nodes,omitempty"`
	Zones   []string          `json:"zones,omitempty"`
	Classes []ClassCapability `json:"classes,omitempty"`
	Choice  *StorageChoice    `json:"choice,omitempty"`
	Probe   *ProbeStatus      `json:"probe,omitempty"`
	// ProbeResults is the last finished probe of each class. Classification
	// reads these, not Probe, so a run in progress changes no fact.
	ProbeResults []ProbeStatus      `json:"probeResults,omitempty"`
	Migration    *MigrationStatus   `json:"migration,omitempty"`
	Conditions   []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterStorage is the cluster's storage settings and findings, put
// together from the platform config, the admin's requests and the observed
// status.
// +kubebuilder:object:generate=true
type ClusterStorage struct {
	Spec   ClusterStorageSpec
	Status ClusterStorageStatus
}

// MigrationPhase is where a home's copy to another mode stands.
type MigrationPhase string

const (
	// MigrationScheduled: an admin set a start time that has not come yet.
	MigrationScheduled MigrationPhase = "Scheduled"
	// MigrationWaiting: the start time has come; apps are held until the
	// owner has no pod left.
	MigrationWaiting MigrationPhase = "Waiting"
	// MigrationCopying: the new claim is made and the copy Job runs.
	MigrationCopying MigrationPhase = "Copying"
	// MigrationVerifying is reserved: the copy Job verifies in the same
	// container, so the phase is not told apart from Copying.
	MigrationVerifying MigrationPhase = "Verifying"
	// MigrationSwitched: the home uses the new claim; the old one is kept
	// until RetainUntil.
	MigrationSwitched MigrationPhase = "Switched"
	// MigrationFailed: the home stays on its old claim.
	MigrationFailed MigrationPhase = "Failed"
)

// ConditionMigrating is the Home condition type that is true while its
// files are being copied to another mode. A Home also uses ConditionReady.
const ConditionMigrating = "Migrating"

// ConditionRestoring is the Home condition type that is true while its
// files are being restored from a backup: the owner's apps are held, like
// while Migrating.
const ConditionRestoring = "Restoring"

// How a restore puts the files back.
const (
	// RestoreReplace makes the home exactly as it was in the backup.
	RestoreReplace = "replace"
	// RestoreBeside puts the backup's files in a restored-<date> folder in
	// the home and changes nothing else.
	RestoreBeside = "beside"
)

// Phases of a home's restore.
const (
	RestoreWaiting   = "Waiting"
	RestoreRestoring = "Restoring"
	RestoreDone      = "Done"
	RestoreFailed    = "Failed"
)

// HomeRestoreRequest is an admin's request to bring a home back from one
// backup (a restic snapshot).
// +kubebuilder:object:generate=true
type HomeRestoreRequest struct {
	// ID tells one request from the next.
	ID string `json:"id"`
	// Run is the restore job's run that asked (its Kubernetes Job name),
	// empty for a restore asked for otherwise.
	Run          string      `json:"run,omitempty"`
	Snapshot     string      `json:"snapshot"`
	SnapshotTime metav1.Time `json:"snapshotTime"`
	Mode         string      `json:"mode"`
	By           string      `json:"by,omitempty"`
	At           metav1.Time `json:"at"`
}

// HomeSpec is one user's home directory.
// +kubebuilder:object:generate=true
type HomeSpec struct {
	// Owner is the person's name. It goes into label values and claim
	// names, so it is a DNS-1123 label.
	Owner string `json:"owner"`
	// Size is the size asked for; nil means the configured default.
	Size *resource.Quantity `json:"size,omitempty"`
	// Restore is the newest restore an admin asked for.
	Restore *HomeRestoreRequest `json:"restore,omitempty"`
	// Backup is the newest full backup run this home is to be part of.
	Backup *BackupRequest `json:"backup,omitempty"`
}

// BackupRequest asks the controller to back a volume up as part of one full
// backup run: the run of the backup job that asked (its Kubernetes Job name).
// +kubebuilder:object:generate=true
type BackupRequest struct {
	Run string      `json:"run"`
	At  metav1.Time `json:"at"`
}

// HomeBackup is the state of a home's backups to the bucket.
// +kubebuilder:object:generate=true
type HomeBackup struct {
	// Run is the full backup run of the newest attempt (BackupRequest.Run).
	Run string `json:"run,omitempty"`
	// Job is the backup Job running now, started at StartedAt; LastJob the
	// one that ran before (its pod's log is the step's log).
	Job       string       `json:"job,omitempty"`
	LastJob   string       `json:"lastJob,omitempty"`
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// LastAt is when the newest backup that worked was taken, as Snapshot.
	LastAt   *metav1.Time `json:"lastAt,omitempty"`
	Snapshot string       `json:"snapshot,omitempty"`
	Files    int          `json:"files,omitempty"`
	Bytes    int64        `json:"bytes,omitempty"`
	// Uploaded is what the newest backup sent to the bucket; Took its
	// seconds.
	Uploaded int64   `json:"uploaded,omitempty"`
	Took     float64 `json:"took,omitempty"`
	// Reason is the catalog code of the last attempt when it did not work,
	// at FailedAt; Evidence is restic's own lines.
	Reason   string       `json:"reason,omitempty"`
	FailedAt *metav1.Time `json:"failedAt,omitempty"`
	Evidence []string     `json:"evidence,omitempty"`
}

// HomeRestore is the state of the restore Spec.Restore asked for.
// +kubebuilder:object:generate=true
type HomeRestore struct {
	ID         string       `json:"id"`
	Phase      string       `json:"phase"`
	Job        string       `json:"job,omitempty"`
	Reason     string       `json:"reason,omitempty"`
	Evidence   []string     `json:"evidence,omitempty"`
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// Ended is whether the restore is over, either way.
func (r *HomeRestore) Ended() bool {
	return r != nil && (r.Phase == RestoreDone || r.Phase == RestoreFailed)
}

// HomeMigration is the state of a home's move between modes.
// +kubebuilder:object:generate=true
type HomeMigration struct {
	Phase MigrationPhase `json:"phase,omitempty"`
	Job   string         `json:"job,omitempty"`
	// NewClaim is the claim the files are copied to, until the switch.
	NewClaim    string       `json:"newClaim,omitempty"`
	OldClaim    string       `json:"oldClaim,omitempty"`
	RetainUntil *metav1.Time `json:"retainUntil,omitempty"`
	Attempt     int32        `json:"attempt,omitempty"`
	Evidence    []string     `json:"evidence,omitempty"`
}

// HomeStatus is observed state.
// +kubebuilder:object:generate=true
type HomeStatus struct {
	Mode             HomeMode       `json:"mode,omitempty"`
	ClaimName        string         `json:"claimName,omitempty"`
	StorageClassName string         `json:"storageClassName,omitempty"`
	SizeEnforced     bool           `json:"sizeEnforced,omitempty"`
	Prepared         bool           `json:"prepared,omitempty"`
	Migration        *HomeMigration `json:"migration,omitempty"`
	// CreatedOnSingleNode is set on a perUser home whose claim was made while
	// the cluster had one node. It is the durable record behind the
	// SingleNodeAssumption condition: such homes have no co-location to rely
	// on once a second node joins.
	CreatedOnSingleNode bool `json:"createdOnSingleNode,omitempty"`
	// PrepareAttempts counts the prepare Jobs made for the home, so a setup
	// that can never work stops getting new Jobs.
	PrepareAttempts int `json:"prepareAttempts,omitempty"`
	// VolumeUID and VolumeGID own the files of a home whose owner has no
	// account (a volume from before, found without a row): its backups read
	// it as them.
	VolumeUID int64 `json:"volumeUID,omitempty"`
	VolumeGID int64 `json:"volumeGID,omitempty"`
	// Backup is the state of the home's backups to the bucket, Restore
	// that of the restore Spec.Restore asks for.
	Backup     *HomeBackup        `json:"backup,omitempty"`
	Restore    *HomeRestore       `json:"restore,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Home records which claim holds a user's files and how it is migrating. One
// per owner and namespace.
// +kubebuilder:object:generate=true
type Home struct {
	ID        string
	Namespace string
	Spec      HomeSpec
	Status    HomeStatus
	// Version rises with every write; a write names the version it read.
	Version int64
}

// Name is the home's name: the owner's home claim name of the first
// generation, which also names its prepare Jobs.
func (h *Home) Name() string { return "home-" + h.Spec.Owner }

// Validate checks a home before it is stored.
func (h *Home) Validate() error {
	if errs := validation.IsDNS1123Label(h.Spec.Owner); len(errs) > 0 {
		return fmt.Errorf("owner: %q is not a valid name", h.Spec.Owner)
	}
	return nil
}

// SharedFolderSpec declares one group folder.
// +kubebuilder:object:generate=true
type SharedFolderSpec struct {
	// Group is the group value exactly as the provider reports it.
	Group string `json:"group"`
	// Size is the folder capacity; nil means storage.sharedFolders.defaultSize.
	Size *resource.Quantity `json:"size,omitempty"`
	// StorageClassName beats storage.sharedFolders.storageClassName.
	StorageClassName string `json:"storageClassName,omitempty"`
	// Backup is the newest full backup run this folder is to be part of.
	Backup *BackupRequest `json:"backup,omitempty"`
}

// SharedFolderStatus is observed state.
// +kubebuilder:object:generate=true
type SharedFolderStatus struct {
	ClaimName string `json:"claimName,omitempty"`
	GID       int64  `json:"gid,omitempty"`
	Prepared  bool   `json:"prepared,omitempty"`
	// PrepareAttempts counts the prepare Jobs made for the folder.
	PrepareAttempts int `json:"prepareAttempts,omitempty"`
	// Backup is the state of the folder's backups to the bucket.
	Backup     *HomeBackup        `json:"backup,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SharedFolder is a folder shared by the members of one group.
type SharedFolder struct {
	ID        string
	Namespace string
	// Name names the folder's claim (FolderClaimName), so it is a DNS-1123
	// label of at most MaxFolderName characters.
	Name      string
	Spec      SharedFolderSpec
	Status    SharedFolderStatus
	CreatedAt time.Time
	Version   int64
}

// MaxFolderName is the longest folder name: its claim name ("folder-" and
// the name) must fit a label value.
const MaxFolderName = 56

// Validate checks a folder before it is stored.
func (f *SharedFolder) Validate() error {
	var errs []error
	if errs1 := validation.IsDNS1123Label(f.Name); len(errs1) > 0 {
		errs = append(errs, fmt.Errorf("name: %q is not a valid name", f.Name))
	} else if len(f.Name) > MaxFolderName {
		errs = append(errs, fmt.Errorf("name: must be at most %d characters", MaxFolderName))
	}
	if f.Spec.Group == "" {
		errs = append(errs, errors.New("group: must not be empty"))
	}
	return errors.Join(errs...)
}

// DeepCopy returns a copy of f that shares nothing with it. (CreatedAt is a
// time.Time, which the generator cannot copy, so this one is written out.)
func (f *SharedFolder) DeepCopy() *SharedFolder {
	out := *f
	f.Spec.DeepCopyInto(&out.Spec)
	f.Status.DeepCopyInto(&out.Status)
	return &out
}
