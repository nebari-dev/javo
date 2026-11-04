// Package reasons is the single catalog of user-facing status and failure
// text. The API and UI render these entries and never invent their own.
package reasons

// Party is whose fault the cause is: the user (app code, environment, choices),
// the platform (cluster capacity or storage limits) or the template (its
// image or definition). It names the cause, not who can act on it.
type Party string

const (
	PartyUser     Party = "user"
	PartyPlatform Party = "platform"
	PartyTemplate Party = "template"
)

// Code names one Reason. Every Code constant has a catalog entry (a test
// enforces it), so code that emits a Code always finds copy for it.
type Code string

// The reason codes. Conditions store them as their Reason.
const (
	NoCapacity        Code = "NoCapacity"
	HomeBusy          Code = "HomeBusy"
	HomePending       Code = "HomePending"
	HomeConflict      Code = "HomeConflict"
	HomeStarting      Code = "HomeStarting"
	IdentityPending   Code = "IdentityPending"
	NamespacePending  Code = "NamespacePending"
	UIDRangeExhausted Code = "UIDRangeExhausted"
	ImagePullFailed   Code = "ImagePullFailed"
	ImageRunsAsRoot   Code = "ImageRunsAsRoot"
	// ImageNotAllowed: the app's image is from a registry the admin no
	// longer allows (containerImages.allowedRegistries).
	ImageNotAllowed Code = "ImageNotAllowed"
	AppCrashed      Code = "AppCrashed"
	OutOfMemory     Code = "OutOfMemory"
	AppNotAnswering Code = "AppNotAnswering"
	TemplateMissing Code = "TemplateMissing"
	// TemplateNotAllowed: the app's owner may no longer use its template.
	TemplateNotAllowed Code = "TemplateNotAllowed"
	TemplateInvalid    Code = "TemplateInvalid"
	SampleMissing      Code = "SampleMissing"
	SizeUnknown        Code = "SizeUnknown"
	SizeTooBig         Code = "SizeTooBig"
	AppUnreachable     Code = "AppUnreachable"
	Starting           Code = "Starting"
	Ready              Code = "Ready"
	Stopped            Code = "Stopped"
	StoppedIdle        Code = "StoppedIdle"
	StoppedByAdmin     Code = "StoppedByAdmin"
	RestoreHeld        Code = "RestoreHeld"
	SecretsUnreadable  Code = "SecretsUnreadable"
	// An app or a job in a pixi environment (model.Environment).
	EnvironmentUnavailable   Code = "EnvironmentUnavailable"
	EnvironmentInstallFailed Code = "EnvironmentInstallFailed"
	EnvironmentLacksProgram  Code = "EnvironmentLacksProgram"

	// Database backup health, for the admin.
	DatabaseBackedUp         Code = "DatabaseBackedUp"
	DatabaseBackupsOff       Code = "DatabaseBackupsOff"
	DatabaseBackupNone       Code = "DatabaseBackupNone"
	DatabaseBackupStale      Code = "DatabaseBackupStale"
	DatabaseBackupsByCronJob Code = "DatabaseBackupsByCronJob"

	// Home folders without a backup bucket, for the admin.
	NoBackupTool Code = "NoBackupTool"

	// Finer causes the diagnosis rules (internal/diagnose) find in a
	// failure's evidence, and Unknown when none matches.
	ImageNotFound        Code = "ImageNotFound"
	ImageAuthFailed      Code = "ImageAuthFailed"
	MissingModule        Code = "MissingModule"
	PortInUse            Code = "PortInUse"
	WrongBasePath        Code = "WrongBasePath"
	HomePermissionDenied Code = "HomePermissionDenied"
	Unknown              Code = "Unknown"
	// Backups to the S3-compatible bucket (restic), for the admin: the copy
	// of the database, the home folders and their restore.
	DatabaseCopied      Code = "DatabaseCopied"
	DatabaseCopyNone    Code = "DatabaseCopyNone"
	HomesBackedUp       Code = "HomesBackedUp"
	HomesBackupFailing  Code = "HomesBackupFailing"
	HomeBackedUp        Code = "HomeBackedUp"
	HomeBackupNone      Code = "HomeBackupNone"
	HomeBackupRunning   Code = "HomeBackupRunning"
	HomeBackupStale     Code = "HomeBackupStale"
	HomeBackupFailed    Code = "HomeBackupFailed"
	HomeRestoreWaiting  Code = "HomeRestoreWaiting"
	HomeRestoreRunning  Code = "HomeRestoreRunning"
	HomeRestored        Code = "HomeRestored"
	HomeRestoreFailed   Code = "HomeRestoreFailed"
	BackupWrongPassword Code = "BackupWrongPassword"
	BackupRepoMissing   Code = "BackupRepoMissing"
	BackupRepoLocked    Code = "BackupRepoLocked"
	BackupUnreachable   Code = "BackupUnreachable"
	BackupDenied        Code = "BackupDenied"
	BackupBucketMissing Code = "BackupBucketMissing"
	BackupIncomplete    Code = "BackupIncomplete"
	BackupFailed        Code = "BackupFailed"
	// Full backup runs and full restores (the backup and restore jobs).
	BackupsNotSetUp    Code = "BackupsNotSetUp"
	FullBackupNone     Code = "FullBackupNone"
	FullBackupRunning  Code = "FullBackupRunning"
	FullBackupDone     Code = "FullBackupDone"
	FullBackupProblems Code = "FullBackupProblems"
	FullBackupStale    Code = "FullBackupStale"
	BackupWaiting      Code = "BackupWaiting"
	NotInBackupRun     Code = "NotInBackupRun"
	// HomeRestoring holds an app while its owner's files are restored.
	HomeRestoring Code = "HomeRestoring"

	// Sign-in reasons (OIDC).
	IdpUnreachable       Code = "IdpUnreachable"
	IssuerMismatch       Code = "IssuerMismatch"
	ClockSkew            Code = "ClockSkew"
	SignInLinkExpired    Code = "SignInLinkExpired"
	GroupsOverage        Code = "GroupsOverage"
	NoGroupsReceived     Code = "NoGroupsReceived"
	SignedOutLocallyOnly Code = "SignedOutLocallyOnly"
	SessionEnded         Code = "SessionEnded"
)

// Run outcome codes for jobs.
const (
	CellError         Code = "CellError"
	KernelDied        Code = "KernelDied"
	KernelMissing     Code = "KernelMissing"
	CommandFailed     Code = "CommandFailed"
	EngineMissing     Code = "EngineMissing"
	SourceMissing     Code = "SourceMissing"
	OutputWriteFailed Code = "OutputWriteFailed"
	RunOutOfMemory    Code = "RunOutOfMemory"
	RunTimedOut       Code = "RunTimedOut"
	RunSkipped        Code = "RunSkipped"
	RunMissed         Code = "RunMissed"
	RunCancelled      Code = "RunCancelled"
	RunLost           Code = "RunLost"
	RunInterrupted    Code = "RunInterrupted"
	ParametersInvalid Code = "ParametersInvalid"
	// RunLimitReached: the job or its owner has as many runs going as the
	// platform allows (limits.activeRunsPerJob, limits.activeRunsPerPerson).
	RunLimitReached Code = "RunLimitReached"
	Succeeded       Code = "Succeeded"
	Running         Code = "Running"
)

// Job definition state codes.
const (
	JobsNotAllowed     Code = "JobsNotAllowed"
	OwnerLostAccess    Code = "OwnerLostAccess"
	SchedulePaused     Code = "SchedulePaused"
	ScheduleInvalid    Code = "ScheduleInvalid"
	NotificationFailed Code = "NotificationFailed"
	JobSizeUnknown     Code = "JobSizeUnknown"
)

// Storage codes.
const (
	SharedFoldersUnavailable     Code = "SharedFoldersUnavailable"
	NoStorageClass               Code = "NoStorageClass"
	NoDefaultStorageClass        Code = "NoDefaultStorageClass"
	NamedStorageClassMissing     Code = "NamedStorageClassMissing"
	OverrideNotSupported         Code = "OverrideNotSupported"
	SecondNodeJoined             Code = "SecondNodeJoined"
	HomePreparing                Code = "HomePreparing"
	FolderPreparing              Code = "FolderPreparing"
	GroupHasNoId                 Code = "GroupHasNoId"
	FolderPermissionsUnsupported Code = "FolderPermissionsUnsupported"
	FolderPending                Code = "FolderPending"
	FolderPrepareFailed          Code = "FolderPrepareFailed"
	FolderClaimForeign           Code = "FolderClaimForeign"
	FolderDirTaken               Code = "FolderDirTaken"
	HomePrepareFailed            Code = "HomePrepareFailed"
	HomeMountFailed              Code = "HomeMountFailed"
	HomeClaimForeign             Code = "HomeClaimForeign"
	HomeTooLarge                 Code = "HomeTooLarge"
	HomeCannotGrow               Code = "HomeCannotGrow"
	HomeCannotShrink             Code = "HomeCannotShrink"
	TeamStorageFull              Code = "TeamStorageFull"
	HomeMigrating                Code = "HomeMigrating"
	HomeModeMismatch             Code = "HomeModeMismatch"
	MigrationFailed              Code = "MigrationFailed"
	ScratchFull                  Code = "ScratchFull"

	// SupplementalGroupsPolicyNotSupported is the reason a node gives when it
	// cannot enforce the group list of a pod that mounts group folders.
	SupplementalGroupsPolicyNotSupported Code = "SupplementalGroupsPolicyNotSupported"

	// SandboxFailed is a machine's container runtime refusing, again and
	// again, to set up the pod the app runs in.
	SandboxFailed Code = "SandboxFailed"
)

// Interactive kind codes.
const (
	NetworkPolicyNotEnforced Code = "NetworkPolicyNotEnforced"
)

// Repository, build and preview codes.
const (
	RepoNotFound        Code = "RepoNotFound"
	RefNotFound         Code = "RefNotFound"
	AuthFailed          Code = "AuthFailed"
	CloneFailed         Code = "CloneFailed"
	FileNotInRepo       Code = "FileNotInRepo"
	BuildFailed         Code = "BuildFailed"
	BuildOutputMissing  Code = "BuildOutputMissing"
	PreviewRateLimited  Code = "PreviewRateLimited"
	PreviewLimitReached Code = "PreviewLimitReached"
	// PreviewOwnerLimitReached is the platform's cap on all of a person's
	// previews (limits.previews).
	PreviewOwnerLimitReached Code = "PreviewOwnerLimitReached"
	PreviewForkSkipped       Code = "PreviewForkSkipped"
	PreviewsUnavailable      Code = "PreviewsUnavailable"
)

// Reason is one catalog entry, keyed by Code.
type Reason struct {
	Code  Code
	Title string
	// What happened, in plain words.
	What string
	// Todo is what the user can do next.
	Todo string
	// Blame explains whose fault the cause is (the user, the platform or the
	// template) and must agree with Blamed.
	Blame string
	// Who can help.
	Who    string
	Blamed Party
}

var catalog = []Reason{
	{Code: NoCapacity, Title: "No machine has room",
		What:  "No machine has enough free CPU or memory for this app, and the cluster cannot add another one right now.",
		Todo:  "Keep it queued and it starts as soon as room frees up, or stop one of your other apps.",
		Blame: "The platform: the cluster is full. Not your app.",
		Who:   "Cluster admins can add capacity.", Blamed: PartyPlatform},
	{Code: HomeBusy, Title: "Home folder is busy",
		What:  "Your home folder can only be attached to one machine at a time. Right now it is attached to a machine where there is no room for more.",
		Todo:  "Stop one of your running apps, then try again. New apps also fit more easily when started smaller.",
		Blame: "A limit of this cluster's storage (per-user disk mode), not something you did.",
		Who:   "You can fix it now by stopping a running app. A cluster admin can remove the limit for everyone by enabling shared storage.", Blamed: PartyPlatform},
	{Code: HomePending, Title: "Home folder is not ready yet",
		What:  "Your home folder is still being created or attached, so the app cannot start yet.",
		Todo:  "Wait a minute. If it stays like this, ask a cluster admin to check the storage.",
		Blame: "The platform: storage is slow to provision. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeConflict, Title: "Home folder belongs to another account",
		What:  "The home folder this app would use is recorded as belonging to a different account, so it is not opened.",
		Todo:  "Ask a cluster admin to look at your home folder. Nothing you change here will fix it.",
		Blame: "The platform: two accounts were given the same home folder name. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeStarting, Title: "Opening your home folder",
		What:  "Your home folder is being opened so its files can be listed. The first time takes a few seconds.",
		Todo:  "Wait a moment. The list loads by itself.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: IdentityPending, Title: "Setting up your account",
		What:  "Your account has no user ID yet, and your files and apps need one. It is assigned within a few seconds of your first sign-in.",
		Todo:  "Wait a moment. The app starts by itself once your account is ready.",
		Blame: "Nothing is wrong yet. If it stays like this, the platform could not set up your account.",
		Who:   "Cluster admins if it does not clear.", Blamed: PartyPlatform},
	{Code: NamespacePending, Title: "Setting up your space",
		What:  "The space your apps live in is still being set up. This happens once, a few seconds after your first sign-in.",
		Todo:  "Wait a moment and try again. Nothing was created yet.",
		Blame: "Nothing is wrong yet. If it stays like this, the platform could not set up your space.",
		Who:   "Cluster admins if it does not clear.", Blamed: PartyPlatform},
	{Code: UIDRangeExhausted, Title: "No free user IDs",
		What:  "Your account cannot be set up yet because there are no free user IDs left.",
		Todo:  "Ask a cluster admin to widen the user ID range. Your account is set up as soon as they do.",
		Blame: "The platform: the range of user IDs it may hand out is full. Not you.",
		Who:   "Cluster admins can widen the range.", Blamed: PartyPlatform},
	{Code: ImagePullFailed, Title: "Image cannot be downloaded",
		What:  "The image for this app could not be downloaded from the registry, for example because it does not exist or the registry is unreachable.",
		Todo:  "Nothing you can fix here. Pick a different template, or tell the template owner.",
		Blame: "The template: its image cannot be downloaded. Not your app.",
		Who:   "The template owner can fix it. You cannot from here.", Blamed: PartyTemplate},
	{Code: ImageNotFound, Title: "Image does not exist",
		What:  "The registry answered that the template's image, or the tag it names, does not exist.",
		Todo:  "Pick a different template, or tell the template owner the image name in the evidence.",
		Blame: "The template: it names an image that does not exist. Not your app.",
		Who:   "The template owner can fix it. You cannot from here.", Blamed: PartyTemplate},
	{Code: ImageAuthFailed, Title: "Registry refused the download",
		What:  "The registry asked for credentials to download the template's image, and the platform has none that it accepts.",
		Todo:  "Pick a different template, or tell the template owner the registry needs a pull secret.",
		Blame: "The template: its image needs registry credentials the platform lacks. Not your app.",
		Who:   "The template owner or a cluster admin can add the credentials.", Blamed: PartyTemplate},
	{Code: ImageRunsAsRoot, Title: "Image runs as root",
		What:  "This image is set to run as the root user, or names its user in a way that cannot be checked. Apps here never run as root, so it was not started.",
		Todo:  "Pick an image that runs as a non-root user (many have an unprivileged or rootless variant), or build one with a numeric user line such as USER 1000, then change the image.",
		Blame: "Your choice of image: it is set to run as root.",
		Who:   "You.", Blamed: PartyUser},
	{Code: ImageNotAllowed, Title: "Image registry not allowed",
		What:  "This app runs an image from a registry the platform no longer allows, so it was not started.",
		Todo:  "Change the app's image to one from an allowed registry (the evidence lists them), then start it again.",
		Blame: "Your choice of image: its registry is not on the platform's list.",
		Who:   "You, or a platform admin who can allow the registry.", Blamed: PartyUser},
	{Code: AppCrashed, Title: "App crashed on start",
		What:  "The app started and then stopped with an error. It was restarted several times and failed the same way, so we stopped retrying.",
		Todo:  "Open the evidence to see the app's error, fix it in your code or environment, then press Restart.",
		Blame: "Your app code or environment.",
		Who:   "You.", Blamed: PartyUser},
	{Code: MissingModule, Title: "A Python package is missing",
		What:  "The app stopped because it imports a Python package that is not installed in its environment.",
		Todo:  "Install the package named in the evidence in your environment, or remove the import, then restart.",
		Blame: "Your app code or environment.",
		Who:   "You.", Blamed: PartyUser},
	{Code: PortInUse, Title: "The port is already taken",
		What:  "The app tried to listen on a port that something else in the app already uses, so it stopped.",
		Todo:  "Make sure the app starts its server once, on the port the template gives it, then restart.",
		Blame: "Your app code or environment.",
		Who:   "You.", Blamed: PartyUser},
	{Code: HomePermissionDenied, Title: "Home folder cannot be written",
		What:  "The app stopped because it was not allowed to read or write a file in the home folder.",
		Todo:  "Ask a cluster admin to fix the owner of your home folder. Restarting will not help.",
		Blame: "The platform: your home folder belongs to a different user id. Not your app.",
		Who:   "A cluster admin.", Blamed: PartyPlatform},
	{Code: OutOfMemory, Title: "App ran out of memory",
		What:  "The app used more memory than its size allows, so the system stopped it.",
		Todo:  "Reduce how much data the app loads at once, then press Restart. If it needs more memory, ask the template owner for a larger size.",
		Blame: "Your app's memory use, or the size it was started with.",
		Who:   "You.", Blamed: PartyUser},
	{Code: AppNotAnswering, Title: "App started but does not answer",
		What:  "The app is running, but nothing answered the platform's health check within the allowed time. It may be listening on a different port or path than the one the platform expects.",
		Todo:  "Check the app's port and base path in its configuration against the address in the evidence, then restart.",
		Blame: "Your app's configuration.",
		Who:   "You.", Blamed: PartyUser},
	{Code: WrongBasePath, Title: "App answers on a different path",
		What:  "The app runs, but the address the platform checks answered \"not found\". The app is most likely serving at / instead of under its own path.",
		Todo:  "Configure the app to serve under the base path in the evidence (for example its root path or base URL setting), then restart.",
		Blame: "Your app's configuration.",
		Who:   "You.", Blamed: PartyUser},
	{Code: Unknown, Title: "Cause not recognized",
		What:  "We could not match this to a known cause.",
		Todo:  "Open the evidence to see what the cluster reported. If it does not help, ask a cluster admin.",
		Blame: "The platform could not tell whose fault this is.",
		Who:   "A cluster admin.", Blamed: PartyPlatform},
	{Code: TemplateMissing, Title: "Template not found",
		What:  "The template this app was created from no longer exists.",
		Todo:  "Pick a different template, or ask the template owner to restore it.",
		Blame: "The template: it was removed.",
		Who:   "The template owner or a cluster admin.", Blamed: PartyTemplate},
	{Code: TemplateNotAllowed, Title: "Template not available to the owner",
		What:  "The owner of this app may no longer use its template, for example after leaving a group the template is limited to, so the app does not run.",
		Todo:  "Ask a cluster admin to give the owner the template again, or move the app's work to a template the owner may use.",
		Blame: "The platform: the owner's access to the template was removed.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: TemplateInvalid, Title: "Template has a mistake",
		What:  "The template this app was created from has a mistake, so the app cannot be built from it.",
		Todo:  "Pick another template, or tell the template owner. The evidence shows the mistake.",
		Blame: "The template: its definition is invalid. Not your app.",
		Who:   "The template owner can fix it. You cannot from here.", Blamed: PartyTemplate},
	{Code: SampleMissing, Title: "Sample could not be copied",
		What:  "The example files for this app could not be copied into your home folder, so the app cannot start on them.",
		Todo:  "Nothing you can fix here. Start an app of your own on this template instead, or tell the template owner.",
		Blame: "The template: its image does not contain the sample it names. Not your app.",
		Who:   "The template owner can fix it. You cannot from here.", Blamed: PartyTemplate},
	{Code: SizeUnknown, Title: "Size not available",
		What:  "This app is set to a size that this cluster does not offer, for example because an admin removed it.",
		Todo:  "Pick one of the offered sizes for the app. It starts as soon as the size is changed.",
		Blame: "The platform: the size is not on the list any more. Not your app.",
		Who:   "You can pick another size. A cluster admin can add the size back.", Blamed: PartyPlatform},
	{Code: SizeTooBig, Title: "Size too big for any machine",
		What:  "This app's size needs more CPU, memory or GPUs than any machine in this cluster has, so it can never start here, however long it waits.",
		Todo:  "Pick a smaller size with Change size. The app starts as soon as the size fits.",
		Blame: "Your app's settings: its size is bigger than any machine here. Not a fault in your code.",
		Who:   "You can pick a smaller size. A cluster admin can add bigger machines.", Blamed: PartyUser},
	{Code: AppUnreachable, Title: "This app is not answering",
		What:  "The app is running, but the gateway could not reach it just now.",
		Todo:  "Open the app's status page to see what is happening, or restart the app.",
		Blame: "The platform, unless the app's status page shows otherwise.",
		Who:   "Cluster admins if a restart does not help.", Blamed: PartyPlatform},
	{Code: Starting, Title: "Starting",
		What:  "The app is starting. This usually takes a minute or two.",
		Todo:  "Nothing to do. You can leave this page; it keeps starting.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: Ready, Title: "Ready",
		What:  "The app is running and answering.",
		Todo:  "Open it.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: Stopped, Title: "Stopped",
		What:  "The app is stopped. Your files are kept.",
		Todo:  "Start it again when you need it.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.",
		// Stopped is not a fault; PartyUser only records that the user chose it.
		Blamed: PartyUser},
	{Code: StoppedIdle, Title: "Stopped when idle",
		What:  "Nobody used the app for a while, so it was stopped to free its CPU and memory. Your files and settings are kept.",
		Todo:  "Start it again when you need it.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.",
		// Not a fault; PartyUser records that the app's own setting chose it.
		Blamed: PartyUser},
	{Code: StoppedByAdmin, Title: "Stopped by an admin",
		What:  "A platform admin stopped the app, for example to free the cluster for others. Your files and settings are kept.",
		Todo:  "Start it again when you need it, or ask your admin why it was stopped.",
		Blame: "Nothing is wrong with the app.",
		Who:   "Your platform admin.", Blamed: PartyPlatform},
	{Code: RestoreHeld, Title: "Restored, waiting for an admin",
		What:  "The app came back from a backup. It waits until an admin starts the restored apps, so they do not all start at once and your files are back first.",
		Todo:  "Nothing to do. It starts when your admin releases the restored apps.",
		Blame: "Nothing is wrong.",
		Who:   "Your platform admin.", Blamed: PartyPlatform},
	{Code: SecretsUnreadable, Title: "Secrets could not be read",
		What:  "It gets secrets, and the platform could not decrypt them for it, so it does not start without them.",
		Todo:  "Tell your platform admin. The evidence says what failed; no secret value is ever shown.",
		Blame: "The platform: its secrets key is missing or does not match the database. Not your app or job.",
		Who:   "Your platform admin.", Blamed: PartyPlatform},
	{Code: EnvironmentUnavailable, Title: "Environment not available",
		What:  "It runs in an environment picked from an environment manager, and the platform cannot use it: its stored files are missing or too large to hand to a pod, or no image to install environments with is set.",
		Todo:  "Tell your platform admin; the evidence says which. Files that are missing or too large need a new pick: create it again with the environment, or a smaller one.",
		Blame: "The platform: it cannot hand this environment to a pod. Not your code.",
		Who:   "Your platform admin.", Blamed: PartyPlatform},
	{Code: EnvironmentInstallFailed, Title: "Environment did not install",
		What:  "The step that installs its environment from the pixi.lock failed, so it did not start. The evidence is pixi's own error.",
		Todo:  "Fix the environment in the environment manager (it needs the linux-64 platform, and every package must still be downloadable), then create it again with the new version.",
		Blame: "Your environment: pixi could not install its lockfile here.",
		Who:   "You, in the environment manager.", Blamed: PartyUser},
	{Code: EnvironmentLacksProgram, Title: "Environment lacks the program",
		What:  "Its environment replaces the template's, and it does not have the program the template starts, so the app did not start. The evidence names the environment and the program.",
		Todo:  "Add the program to the environment and start the app again, or pick another environment.",
		Blame: "Your environment: it does not have the program this template runs.",
		Who:   "You, in the environment manager or the repository.", Blamed: PartyUser},
	{Code: DatabaseBackedUp, Title: "Database backed up",
		What:  "The database is copied on schedule and the newest copy is recent. A copy holds every app, template, job, person, token and setting; files in home folders are not in it.",
		Todo:  "Keep a copy of the backups claim outside the cluster, or turn on backups to an S3-compatible bucket (backupTarget in the Helm values), which also backs up home folders.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: DatabaseBackupsOff, Title: "Database not backed up",
		What:  "Database backups are turned off, so losing the database volume loses every app, template, job and setting.",
		Todo:  "Set database.backup.schedule in the Helm values, or remove the empty value to get the default 0 2 * * * (every night at 02:00).",
		Blame: "The install's settings.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: DatabaseBackupNone, Title: "No database backup yet",
		What:  "Backups are scheduled, but none has been taken yet.",
		Todo:  "Wait for the first scheduled backup, or take one now.",
		Blame: "Nothing is wrong yet.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: DatabaseBackupStale, Title: "Database backups stopped",
		What:  "The newest database backup is older than two scheduled runs, so recent changes are not in any backup.",
		Todo:  "Take a backup now, then read the server log for the error of the scheduled backup (a full backups claim is common).",
		Blame: "The platform: the scheduled backup failed.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: DatabaseBackupsByCronJob, Title: "Backed up by pg_dump",
		What:  "This install keeps its data in Postgres. The javo-db-backup CronJob dumps it on schedule, or your managed Postgres backs it up.",
		Todo:  "Check the CronJob's last run with kubectl, and restore with pg_restore as the Backups page of the admin guide shows.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: NoBackupTool, Title: "Home folders not backed up",
		What:  "Files in home folders are in no backup. The database backups above do not hold them.",
		Todo:  "Turn on backups to an S3-compatible bucket: set backupTarget in the Helm values (works on any disk). The Backups page of the admin guide shows how.",
		Blame: "The install's settings.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: DatabaseCopied, Title: "Copied to the bucket",
		What:  "Every database backup is also copied, encrypted, to the backup bucket, so losing this machine loses no data.",
		Todo:  "Nothing to do. Keep the backup password safe, apart from the cluster.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: DatabaseCopyNone, Title: "Not copied to the bucket yet",
		What:  "The next database backup is copied to the backup bucket as well.",
		Todo:  "Wait for the next backup, or back up now.",
		Blame: "Nothing is wrong yet.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomesBackedUp, Title: "Home folders backed up",
		What:  "Every home folder is backed up to the backup bucket each night: encrypted, and only what changed is uploaded.",
		Todo:  "Nothing to do.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomesBackupFailing, Title: "Some home folders were not backed up",
		What:  "The last backup of the home folders listed below did not work, so their newest changes are in no backup.",
		Todo:  "Open a person below to read why, fix the cause, and the next night's backup tries again.",
		Blame: "The platform: the backup of some home folders failed.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeBackedUp, Title: "Backed up",
		What:  "The newest backup of this home folder worked.",
		Todo:  "Nothing to do.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeBackupNone, Title: "Not backed up yet",
		What:  "This home folder is backed up with the next nightly backup.",
		Todo:  "Wait for the next backup.",
		Blame: "Nothing is wrong yet.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeBackupRunning, Title: "Backing up",
		What:  "This home folder is being backed up now. Its apps keep running.",
		Todo:  "Wait.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeBackupStale, Title: "Backup is late",
		What:  "The newest backup of this home folder is older than two scheduled runs.",
		Todo:  "Check that the controller runs, then read its log for this home folder.",
		Blame: "The platform: the nightly backup did not run.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeBackupFailed, Title: "Backup failed",
		What:  "The backup of this home folder stopped before it finished, so its newest changes are in no backup.",
		Todo:  "Read the details below; the next nightly backup tries again.",
		Blame: "The platform: the backup job of this home folder failed.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeRestoreWaiting, Title: "Waiting for apps to stop",
		What:  "The person's apps are stopping so that no file changes while it is restored.",
		Todo:  "Wait. The restore starts by itself.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeRestoreRunning, Title: "Restoring files",
		What:  "The files are coming back from the backup. The person's apps stay stopped until it is done.",
		Todo:  "Wait. The apps start again by themselves.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeRestored, Title: "Restored",
		What:  "The files are back and the person's apps were started again.",
		Todo:  "Nothing to do.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeRestoreFailed, Title: "Restore failed",
		What:  "The files could not all be brought back. The person's apps were started again.",
		Todo:  "Read the details below, fix the cause and restore again.",
		Blame: "The platform: the restore job failed.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupWrongPassword, Title: "Wrong backup password",
		What:  "The backups in the bucket were made with another password, so they cannot be read or added to.",
		Todo:  "Put the original backup password back in the Secret named by backupTarget.password.existingSecret, then restart javo.",
		Blame: "The platform: the backup password does not match the bucket.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupRepoMissing, Title: "No backups in the bucket",
		What:  "The bucket holds no backups at this place yet.",
		Todo:  "Check backupTarget.bucket and backupTarget.prefix. A new install makes them with its first backup.",
		Blame: "The platform: the backup target.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupRepoLocked, Title: "Backups are busy",
		What:  "Another backup or clean-up holds the backups in the bucket, so this one could not start.",
		Todo:  "Wait for the other one to finish; this one is tried again.",
		Blame: "The platform: two backup jobs met.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupUnreachable, Title: "Backup bucket unreachable",
		What:  "The backup bucket's address did not answer, so nothing was copied.",
		Todo:  "Check backupTarget.endpoint and that the cluster may reach it (a NetworkPolicy, a proxy or DNS).",
		Blame: "The platform: the network to the bucket.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupDenied, Title: "Backup bucket refused the key",
		What:  "The bucket refused the access key, so nothing was copied.",
		Todo:  "Check the access key in the Secret named by backupTarget.existingSecret and that it may read and write the bucket.",
		Blame: "The platform: the bucket's access key.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupBucketMissing, Title: "Backup bucket not found",
		What:  "The bucket named in the settings does not exist at that address.",
		Todo:  "Create the bucket, or fix backupTarget.bucket and backupTarget.region.",
		Blame: "The platform: the backup target.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupIncomplete, Title: "Some files were skipped",
		What:  "The backup finished, but some files could not be read and are not in it.",
		Todo:  "Read the details below for the files; usually they belong to another user or vanished while being read.",
		Blame: "The platform: files the backup could not read.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupFailed, Title: "Backup tool failed",
		What:  "restic stopped with an error that has no plainer name.",
		Todo:  "Read the details below for restic's own message.",
		Blame: "The platform: the backup tool.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupsNotSetUp, Title: "Backups not set up",
		What:  "There is no backup job yet, so nothing is backed up to the bucket.",
		Todo:  "Press Set up backups: it makes the backup job, which runs every night, and the restore job.",
		Blame: "The install's settings.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: FullBackupNone, Title: "No full backup yet",
		What:  "The backup job has not run yet.",
		Todo:  "Wait for its first night, or back up now.",
		Blame: "Nothing is wrong yet.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: FullBackupRunning, Title: "Backing up now",
		What:  "The database, every home folder and every shared folder are being copied to the bucket. Apps keep running.",
		Todo:  "Wait; the job run shows each step.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: FullBackupDone, Title: "Everything is backed up",
		What:  "The newest full backup copied the database, every home folder and every shared folder to the bucket.",
		Todo:  "Nothing to do.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: FullBackupProblems, Title: "The newest backup missed something",
		What:  "The newest full backup ran, but some parts were not copied, so their newest changes are in no backup.",
		Todo:  "Open the details below or the job run's log to read why, fix the cause and back up now.",
		Blame: "The platform: a step of the backup failed.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: FullBackupStale, Title: "Backups stopped",
		What:  "The newest full backup is more than two days old, so recent changes are in no backup.",
		Todo:  "Check the backup job on the Jobs page: its schedule, and whether it is paused.",
		Blame: "The platform: the backup job did not run.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: BackupWaiting, Title: "Waiting",
		What:  "This part of the backup has not started yet.",
		Todo:  "Wait.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: NotInBackupRun, Title: "Not in that backup",
		What:  "This home folder is not in the chosen backup: it was made later, or its backup failed that day.",
		Todo:  "Pick another date for this person.",
		Blame: "Nothing is wrong.",
		Who:   "Platform admins.", Blamed: PartyPlatform},
	{Code: HomeRestoring, Title: "Your files are being restored",
		What:  "An admin is bringing back your home folder from a backup. Apps stay stopped meanwhile, so nothing writes to it halfway.",
		Todo:  "Wait. Your apps start again by themselves when it is done.",
		Blame: "The platform: an admin started a restore. Not your app.",
		Who:   "Platform admins can say when it ends.", Blamed: PartyPlatform},
	{Code: CellError, Title: "A cell raised an error",
		What:  "The run stopped at a cell that raised an error. The cell and the error are shown with the run, and the executed notebook keeps everything up to that cell.",
		Todo:  "Fix the notebook or its inputs, then run it again.",
		Blame: "Your notebook code or its inputs.",
		Who:   "You.", Blamed: PartyUser},
	{Code: KernelDied, Title: "The kernel crashed",
		What:  "The kernel stopped in the middle of a cell without raising an error, for example from a crash in a native library. It is not a mistake in the cell's code.",
		Todo:  "Check the memory the notebook uses and the libraries it loads, then run it again.",
		Blame: "Your notebook's code or libraries.",
		Who:   "You.", Blamed: PartyUser},
	{Code: KernelMissing, Title: "The kernel is not in the template",
		What:  "The notebook needs a kernel that the template's image does not have, so it cannot run.",
		Todo:  "Pick a template that has the kernel, or ask the template owner to add it.",
		Blame: "The template: its image does not have the kernel.",
		Who:   "The template owner can add the kernel. You can pick another template.", Blamed: PartyTemplate},
	{Code: CommandFailed, Title: "The command failed",
		What:  "The command exited with an error code, so the run is marked failed. The last lines of its log are shown with the run.",
		Todo:  "Read the log, fix the command or its inputs, then run it again.",
		Blame: "Your command or its inputs.",
		Who:   "You.", Blamed: PartyUser},
	{Code: EngineMissing, Title: "The engine is not in the template",
		What:  "The tool that runs this kind of file is not in the template's image, so the file cannot run.",
		Todo:  "Pick a template that can run this kind of file, or ask the template owner to add the tool.",
		Blame: "The template: its image does not have the tool.",
		Who:   "The template owner can add the tool. You can pick another template.", Blamed: PartyTemplate},
	{Code: SourceMissing, Title: "The file was not found",
		What:  "The file to run is not in your home folder any more. The last copy that was run is kept in the run folder.",
		Todo:  "Put the file back or pick the new path, then run it again.",
		Blame: "Your file was moved or deleted.",
		Who:   "You.", Blamed: PartyUser},
	{Code: OutputWriteFailed, Title: "Outputs could not be saved",
		What:  "The run finished but its outputs could not be written because the home disk is full, so the run is marked failed.",
		Todo:  "Free space in your home folder or keep fewer past runs, then run it again.",
		Blame: "The platform: the home disk is full.",
		Who:   "You can free space. A cluster admin can make the disk larger.", Blamed: PartyPlatform},
	{Code: RunOutOfMemory, Title: "Run ran out of memory",
		What:  "The run used more memory than its size allows, so the system stopped it. It is not retried.",
		Todo:  "Load less data at once, or pick a larger size with Change size, then run it again.",
		Blame: "Your notebook's memory use, or the size it runs with.",
		Who:   "You.", Blamed: PartyUser},
	{Code: ScheduleInvalid, Title: "Schedule is not valid",
		What:  "The schedule cannot be read as a cron expression in a time zone, so nothing is scheduled.",
		Todo:  "Fix the cron expression or the time zone, then save the job again.",
		Blame: "Your schedule: its cron expression or time zone is not valid.",
		Who:   "You.", Blamed: PartyUser},
	{Code: RunTimedOut, Title: "Run hit its time limit",
		What:  "The run was stopped at its time limit. Time spent waiting for a machine counts toward the limit. Partial output is saved.",
		Todo:  "Raise the time limit (up to the template's maximum) or make the run faster, then run it again.",
		Blame: "Your run needed more time than its limit.",
		Who:   "You.", Blamed: PartyUser},
	{Code: RunSkipped, Title: "Run skipped",
		What:  "A run was still going when the next one was due, and the overlap rule says not to start another.",
		Todo:  "Change the overlap rule or make the run faster if you need every run to happen.",
		Blame: "Your overlap rule, working as set.",
		Who:   "You.", Blamed: PartyUser},
	{Code: RunMissed, Title: "Run missed",
		What:  "A scheduled run did not start because the platform was down or too busy at that time.",
		Todo:  "Run it now if you still need it. Later runs are not affected.",
		Blame: "The platform: it was not running the schedule at that time.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: RunCancelled, Title: "Run cancelled",
		What:  "The run was stopped before it finished, on request.",
		Todo:  "Run it again when you are ready.",
		Blame: "Your choice to stop it.",
		Who:   "You.", Blamed: PartyUser},
	{Code: RunLost, Title: "Run lost with its machine",
		What:  "The machine the run was on went away, and only the run's summary survived. The log may be incomplete.",
		Todo:  "Run it again.",
		Blame: "The platform: the machine was lost.",
		Who:   "Cluster admins if it keeps happening.", Blamed: PartyPlatform},
	{Code: RunInterrupted, Title: "Run interrupted and retried",
		What:  "The machine the run was on was removed, so the run was stopped and started again automatically.",
		Todo:  "Nothing to do. The retry does not count against your retries.",
		Blame: "The platform: the machine was removed.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: ParametersInvalid, Title: "Parameters are not valid",
		What:  "A parameter value does not fit what the notebook expects, so the run was not started.",
		Todo:  "Correct the parameter named in the message, then run it again.",
		Blame: "Your parameter values.",
		Who:   "You.", Blamed: PartyUser},
	{Code: RunLimitReached, Title: "Too many runs going",
		What:  "This job, or all your jobs together, already have as many runs going as the platform allows, so no more start until one ends. Scheduled runs due meanwhile are skipped.",
		Todo:  "Wait for a run to finish, or cancel one. Your platform admin sets the limit.",
		Blame: "The platform's limit on runs going at once.",
		Who:   "You, or your platform admin.", Blamed: PartyPlatform},
	{Code: Succeeded, Title: "Succeeded",
		What:  "The run finished and its outputs were saved.",
		Todo:  "Open the outputs.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: Running, Title: "Running",
		What:  "The run is in progress.",
		Todo:  "Nothing to do. You can leave this page.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: JobsNotAllowed, Title: "Template cannot run jobs",
		What:  "The template this job uses does not allow jobs, so nothing is scheduled.",
		Todo:  "Pick a template that allows jobs, or ask the template owner to allow them.",
		Blame: "The template: it does not allow jobs.",
		Who:   "The template owner can allow jobs.", Blamed: PartyTemplate},
	{Code: JobSizeUnknown, Title: "Size not available",
		What:  "This job is set to a size that this cluster does not offer, for example because an admin removed it, so no run starts.",
		Todo:  "Pick one of the offered sizes with Change size. The next run uses it.",
		Blame: "The platform: the size is not on the list any more. Not your job.",
		Who:   "You can pick another size. A cluster admin can add the size back.", Blamed: PartyPlatform},
	{Code: OwnerLostAccess, Title: "Owner no longer has access",
		What:  "The owner of this job no longer has access to its template, so its schedule is paused.",
		Todo:  "Ask a cluster admin to restore access, or to reassign or delete the job.",
		Blame: "The platform: the owner's access to the template was removed.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: SchedulePaused, Title: "Schedule paused",
		What:  "The schedule is paused, so no new runs start by themselves.",
		Todo:  "Resume the schedule when you want runs to start again.",
		Blame: "Your choice to pause it.",
		Who:   "You.", Blamed: PartyUser},
	{Code: NotificationFailed, Title: "Failure notice could not be sent",
		What:  "A run failed, but the notice about it could not be sent, for example because the mail server refused it.",
		Todo:  "Check the run list for failures. A cluster admin can check the notification channel.",
		Blame: "The platform: the notification channel is not working.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: NetworkPolicyNotEnforced, Title: "No login without network rules",
		What:  "This template starts apps without their own login, which is only safe when the cluster enforces NetworkPolicy. This cluster does not, so the app is not started.",
		Todo:  "Pick a template whose app has its own login, or ask a cluster admin about network rules.",
		Blame: "The platform: this cluster does not enforce NetworkPolicy.",
		Who:   "A cluster admin can enable a network plugin that enforces NetworkPolicy.", Blamed: PartyPlatform},
	{Code: SharedFoldersUnavailable, Title: "Group folders are not available",
		What:  "This cluster keeps each home on its own disk and has no shared drive, so group folders cannot be offered across several machines.",
		Todo:  "Keep working in your own home. A cluster admin can add storage that several machines can share.",
		Blame: "The platform: the cluster has no shared storage.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: NoStorageClass, Title: "The cluster has no storage",
		What:  "The cluster has no StorageClass, so home folders cannot be created.",
		Todo:  "Install a storage provisioner, or ask the cluster provider to add one.",
		Blame: "The platform: the cluster offers no storage.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: NoDefaultStorageClass, Title: "No storage class chosen for homes",
		What:  "No StorageClass is marked default and none is named for homes, so home folders cannot be created.",
		Todo:  "Mark one StorageClass as the default, or name one for homes in the storage settings.",
		Blame: "The platform settings: no class is picked for homes.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: NamedStorageClassMissing, Title: "The chosen storage class does not exist",
		What:  "The storage settings name a StorageClass for homes that the cluster does not have, so home folders would never be attached.",
		Todo:  "Fix the class name in the storage settings, or create that StorageClass.",
		Blame: "The platform settings: they name a class that is not there.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: OverrideNotSupported, Title: "Shared homes are not possible here",
		What:  "Shared homes were requested, but no StorageClass is known to support shared files with POSIX ownership.",
		Todo:  "Add storage that supports shared files, or switch homes back to one disk per user.",
		Blame: "The platform settings ask for something this cluster's storage cannot do.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: SecondNodeJoined, Title: "A second machine joined",
		What:  "Homes were created on a one-machine cluster and another machine has joined, so a user's apps may no longer be able to share their home disk.",
		Todo:  "Switch homes to shared storage, or keep each user's apps on one machine.",
		Blame: "The platform: the cluster grew after homes were made.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeMigrating, Title: "Your home folder is being moved",
		What:  "Your files are being copied to new storage. Apps stay stopped until the copy is checked and switched over, so nothing is lost or half moved.",
		Todo:  "Wait. Your apps start by themselves when the move is done.",
		Blame: "The platform: an admin scheduled a storage change. Not your app.",
		Who:   "Cluster admins can say when it ends.", Blamed: PartyPlatform},
	{Code: HomeModeMismatch, Title: "Some homes are on the old storage mode",
		What:  "The cluster now uses a different home storage mode than some existing home folders. Those homes stay where they are until an admin schedules the move.",
		Todo:  "Schedule the move when it suits users, or switch the mode back.",
		Blame: "The platform settings: the mode changed after these homes were made.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: MigrationFailed, Title: "Your home folder could not be moved",
		What:  "Copying your files to the new storage did not finish or did not match, so nothing was switched. Your files are still where they were, and your apps can start.",
		Todo:  "Nothing is lost. Ask a cluster admin to check the details below and try the move again.",
		Blame: "The platform: the copy to new storage failed. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: ScratchFull, Title: "Temporary space is full",
		What:  "The app wrote more temporary data (environments, package caches, scratch files) than the space next to it holds, so the machine stopped it. Your files are not affected.",
		Todo:  "Clean up large temporary files or packages, then press Restart.",
		Blame: "Your app: it used more temporary space than the limit.",
		Who:   "You. Cluster admins can raise the limit.", Blamed: PartyUser},
	{Code: SupplementalGroupsPolicyNotSupported, Title: "This machine cannot start apps with group folders",
		What:  "Your apps mount shared group folders, and the machine they were placed on cannot enforce the group list that keeps those folders private.",
		Todo:  "Ask a cluster admin to upgrade the machines or leave group folders off.",
		Blame: "The platform: an older machine in the cluster. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: SandboxFailed, Title: "The machine could not start the app",
		What:  "The machine this app was placed on refused to set it up, and kept refusing when it tried again. The app never got as far as running your code.",
		Todo:  "Nothing you change in the app will fix it. Ask a cluster admin to look at the details below.",
		Blame: "The platform: the machine's container runtime refused the app. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomePreparing, Title: "Setting up your home folder",
		What:  "Your home folder was just created and is being set up with the right owner. This takes a few seconds.",
		Todo:  "Wait a moment. Your app starts by itself when the folder is ready.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: FolderPreparing, Title: "Setting up a group folder",
		What:  "A group folder was just created and is being set up so every member of the group can write to it. This takes a few seconds.",
		Todo:  "Wait a moment. The folder appears in your apps by itself when it is ready.",
		Blame: "Nothing is wrong.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: GroupHasNoId, Title: "Group folder is waiting for its group",
		What:  "The group this folder belongs to has no numeric id yet, so the folder cannot be created.",
		Todo:  "Wait a minute. If it stays like this, ask a cluster admin to check that the group exists.",
		Blame: "The platform: the group has not been given an id yet.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: FolderPermissionsUnsupported, Title: "Group folder could not be set up",
		What:  "This cluster's storage did not let the group own the folder, so group members could not all write to it. The folder is not offered.",
		Todo:  "Ask a cluster admin to check the storage; the details below say what it reported. Your own home is not affected.",
		Blame: "The platform: this storage does not support shared group ownership. Not your app.",
		Who:   "Cluster admins can pick storage that supports group ownership.", Blamed: PartyPlatform},
	{Code: FolderPending, Title: "Group folder is not ready yet",
		What:  "A group folder is still being created or attached, so it is not offered yet.",
		Todo:  "Wait a minute. If it stays like this, ask a cluster admin to check the storage; the details below say what is missing.",
		Blame: "The platform: storage is slow to provision. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: FolderPrepareFailed, Title: "Group folder could not be set up",
		What:  "A group folder was created, but the step that makes it usable did not finish, so the folder is not offered.",
		Todo:  "Ask a cluster admin to check the storage; the details below say what failed. Your own home is not affected.",
		Blame: "The platform: this cluster's storage did not accept the setup. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: FolderClaimForeign, Title: "Group folder storage belongs to something else",
		What:  "The storage this group folder would use already exists and is not this folder's, so it is left untouched and the folder is not offered.",
		Todo:  "Ask a cluster admin to look at the folder. Nothing you change here will fix it.",
		Blame: "The platform: two objects were given the same storage name. Not you.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: FolderDirTaken, Title: "Group folder shares a name with another",
		What:  "Another group's folder would appear in apps under the same directory name as this one, so only the older folder is offered.",
		Todo:  "Ask a cluster admin to rename one of the groups or remove the duplicate folder. Nothing you change here will fix it.",
		Blame: "The platform: two groups end in the same name. Not you.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeClaimForeign, Title: "Home folder storage belongs to another account",
		What:  "The storage this home would use is labelled as belonging to a different account, so it is left untouched.",
		Todo:  "Ask a cluster admin to look at your home folder. Nothing you change here will fix it.",
		Blame: "The platform: two accounts were given the same storage name. Not you.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeTooLarge, Title: "Home folder size is over the limit",
		What:  "The size asked for is larger than the largest home folder the cluster allows, so the folder was not created.",
		Todo:  "Ask for a size at or below the limit, or ask a cluster admin to raise it.",
		Blame: "The platform settings: they cap the size of a home folder.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomePrepareFailed, Title: "Home folder could not be set up",
		What:  "Your home folder was created, but the step that makes it usable failed, so the app cannot start.",
		Todo:  "Ask a cluster admin to check the storage; the details below say what failed.",
		Blame: "The platform: this cluster's storage did not accept the setup. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeMountFailed, Title: "Home folder could not be mounted",
		What:  "The home folder's disk could not be mounted on the machine, so the app cannot start. The machine kept refusing it.",
		Todo:  "Ask a cluster admin to check the storage; the details below say what the machine reported.",
		Blame: "The platform: the machine cannot open this kind of storage. Not your app.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeCannotGrow, Title: "Home folder cannot grow",
		What:  "Your home folder is set to a larger size than it has, but this cluster's storage cannot grow a folder in place. It stays at its current size.",
		Todo:  "Your files are safe. A cluster admin can move homes to storage that can grow.",
		Blame: "A limit of this cluster's storage, not something you did.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: HomeCannotShrink, Title: "Home folder cannot shrink",
		What:  "Your home folder is set to a smaller size than it has now. Storage cannot shrink, so it stays at its current size.",
		Todo:  "Nothing to do. Your files are safe at the current size.",
		Blame: "The size setting asks for something storage cannot do.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: TeamStorageFull, Title: "The storage allowance is used up",
		What:  "The storage allowance for this space is used up, so a new home folder could not be created.",
		Todo:  "Remove unused files or homes, or ask a cluster admin to raise the allowance.",
		Blame: "The platform: a storage allowance set by the cluster admins is full.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: IdpUnreachable, Title: "Cannot reach the sign-in service",
		What:  "The service that signs people in did not answer, so nobody can sign in with it right now. Anyone already signed in stays signed in for a while.",
		Todo:  "Try again in a minute. If it keeps failing, tell a cluster admin. If local accounts are enabled, you can sign in with one of those meanwhile.",
		Blame: "The platform or the sign-in service: it is down or cannot be reached. Not you.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: IssuerMismatch, Title: "Sign-in is set up wrongly",
		What:  "The address of the sign-in service in the platform settings is not the one the service itself reports, so its sign-ins cannot be trusted.",
		Todo:  "Tell a cluster admin. The admin details show both addresses, and the settings must use exactly the one the service reports.",
		Blame: "The platform settings, not you.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: ClockSkew, Title: "The clocks disagree",
		What:  "The sign-in service and this server disagree about the time by more than a few minutes, so the sign-in was refused.",
		Todo:  "Check that your computer's clock is right and try again. If it is, tell a cluster admin that a server clock is off.",
		Blame: "A clock that is set wrongly, on your computer or on a server.",
		Who:   "You for your computer, cluster admins for the servers.", Blamed: PartyPlatform},
	{Code: SignInLinkExpired, Title: "That sign-in link expired",
		What:  "The sign-in was started in another tab or too long ago, so it cannot be finished.",
		Todo:  "Press Start again and sign in once more.",
		Blame: "Nothing is wrong. Sign-in links only work for a few minutes, in the browser that started them.",
		Who:   "Nobody needed.", Blamed: PartyUser},
	{Code: GroupsOverage, Title: "Too many groups to read",
		What:  "You belong to so many groups that the sign-in service did not include them, and the platform cannot tell which groups you are in.",
		Todo:  "Tell a cluster admin. They can limit the groups the sign-in service sends.",
		Blame: "The sign-in service's settings. Not you.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: NoGroupsReceived, Title: "No groups received",
		What:  "You are signed in, but the sign-in service sent no groups, so templates and apps shared with groups are hidden.",
		Todo:  "If you expected to see them, tell a cluster admin. They can check which groups the sign-in service sends.",
		Blame: "The sign-in service's settings, or you are in no groups.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: SignedOutLocallyOnly, Title: "Signed out of Workspaces only",
		What:  "You are signed out of Workspaces. Your sign-in service does not support signing out everywhere, so you may still be signed in to it in this browser.",
		Todo:  "Sign out of the sign-in service too if this is a shared computer.",
		Blame: "Nothing is wrong. Some sign-in services have no sign out for other apps.",
		Who:   "Nobody needed.", Blamed: PartyPlatform},
	{Code: SessionEnded, Title: "Your session ended",
		What:  "You were signed out because the sign-in service no longer vouches for you, or the session ran out.",
		Todo:  "Sign in again.",
		Blame: "Nothing is wrong. Sessions end when an account is removed, a password is reset or time runs out.",
		Who:   "Nobody needed.", Blamed: PartyUser},
	{Code: RepoNotFound, Title: "Repository not found",
		What:  "The repository was not found. It may be misspelled, or private and need a token.",
		Todo:  "Check the address. For a private repository, add a token that can read it, then redeploy.",
		Blame: "Your repository address or token.",
		Who:   "You.", Blamed: PartyUser},
	{Code: RefNotFound, Title: "Branch, tag or commit not found",
		What:  "The repository was reached, but it has no branch, tag or commit with that name.",
		Todo:  "Check the name in the app's source settings, or leave it empty to use the default branch, then redeploy.",
		Blame: "Your source settings: that name does not exist in the repository.",
		Who:   "You.", Blamed: PartyUser},
	{Code: AuthFailed, Title: "Token not accepted",
		What:  "The Git server refused the token: it is wrong, has expired, or is not allowed to read this repository.",
		Todo:  "Make a new token with read access to the repository and set it in the app's source settings, then redeploy.",
		Blame: "Your token.",
		Who:   "You.", Blamed: PartyUser},
	{Code: CloneFailed, Title: "Git server not reachable",
		What:  "The app could not reach the Git server to download the repository.",
		Todo:  "Wait a minute and redeploy. If it keeps failing, tell a cluster admin and show them the evidence.",
		Blame: "The platform: the Git server could not be reached from the cluster. Not your code.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
	{Code: FileNotInRepo, Title: "File not in the repository",
		What:  "The repository was downloaded, but the file this app opens is not in it, at the folder and commit the app uses.",
		Todo:  "Check the file and folder in the app's source settings, or push the file to the branch, then redeploy.",
		Blame: "Your app's settings or repository: the file is not there.",
		Who:   "You.", Blamed: PartyUser},
	{Code: BuildFailed, Title: "Build failed",
		What:  "The build command ended with an error, so there is nothing to serve.",
		Todo:  "Read the build output below, fix the build in your repository, then redeploy.",
		Blame: "Your repository's build.",
		Who:   "You.", Blamed: PartyUser},
	{Code: BuildOutputMissing, Title: "Build wrote no output folder",
		What:  "The build finished, but the output folder it should write to does not exist, so there is nothing to serve.",
		Todo:  "Make the build write its files to the output folder named in the evidence, or change the output folder in the app's source settings.",
		Blame: "Your build settings: the output folder does not match what the build writes.",
		Who:   "You.", Blamed: PartyUser},
	{Code: PreviewRateLimited, Title: "GitHub request limit reached",
		What:  "GitHub allows only so many requests an hour, and the platform used them up, so pull requests are not being looked at.",
		Todo:  "Nothing to do. Previews catch up after the time in the message.",
		Blame: "The platform: the shared GitHub request limit is used up. Not your repository.",
		Who:   "Cluster admins if it keeps happening.", Blamed: PartyPlatform},
	{Code: PreviewLimitReached, Title: "Preview limit reached",
		What:  "This app already has the most previews it may have, so this pull request has none.",
		Todo:  "Close a pull request, or raise the limit in the app's preview settings.",
		Blame: "Your preview limit.",
		Who:   "You.", Blamed: PartyUser},
	{Code: PreviewOwnerLimitReached, Title: "Your preview limit reached",
		What:  "Your apps together already have the most previews the platform allows one person, so this pull request has none.",
		Todo:  "Close a pull request, or turn previews off on an app you do not need them for. Your platform admin sets the limit.",
		Blame: "The platform's limit on previews per person.",
		Who:   "You, or your platform admin.", Blamed: PartyPlatform},
	{Code: PreviewForkSkipped, Title: "Pull request from a fork skipped",
		What:  "This pull request comes from a fork, and previews of forks are off because they run code you have not reviewed.",
		Todo:  "Turn on previews of forks in the app's preview settings only if you trust everyone who can open pull requests.",
		Blame: "Your preview settings.",
		Who:   "You.", Blamed: PartyUser},
	{Code: PreviewsUnavailable, Title: "Cannot read pull requests",
		What:  "GitHub did not answer the request for the repository's pull requests, so previews are not being updated.",
		Todo:  "Wait a few minutes. If it keeps failing, check the repository address and tell a cluster admin.",
		Blame: "The platform or GitHub: the request for pull requests failed.",
		Who:   "Cluster admins.", Blamed: PartyPlatform},
}

// Lookup returns the catalog entry for code.
func Lookup(code Code) (Reason, bool) {
	for _, r := range catalog {
		if r.Code == code {
			return r, true
		}
	}
	return Reason{}, false
}

// All returns a copy of every catalog entry.
func All() []Reason {
	return append([]Reason(nil), catalog...)
}
