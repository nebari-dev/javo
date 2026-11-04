package model

const (
	// Group is the API group of all javo resources.
	Group = "javo.nebari.dev"
	// ProductName is the product name in lowercase, as it appears in object
	// names, cookies and token prefixes; the single Go source of the name.
	ProductName = "javo"
)

// Label and annotation keys, all under Group, and the names of the objects
// and cookies the platform owns, all built from ProductName. Every package
// uses these; the Helm chart builds the same keys with the "javo.groupKey"
// helper in deploy/helm/javo/templates/_helpers.tpl.
const (
	// LabelApp carries the app's row id on its Pod, Service and API token
	// Secret. It is how the controller maps a runtime object back to its row
	// and finds the ones whose row is gone.
	LabelApp = Group + "/app"
	// LabelWorkspace carries the app object name on API token records.
	LabelWorkspace = Group + "/workspace"
	// LabelOwner carries the owner's user slug (workspaces, their objects,
	// home claims and file pods).
	LabelOwner = Group + "/owner"
	// LabelGroupFolder carries the shared folder's name on a group folder
	// claim and its prepare Job.
	LabelGroupFolder = Group + "/group-folder"
	// LabelHome carries the home's row id on the Jobs made for it (prepare,
	// copy), so the controller finds its own and the sweep the orphans.
	LabelHome = Group + "/home"
	// LabelFolder carries a shared folder's row id on the backup Jobs made
	// for it.
	LabelFolder = Group + "/folder"
	// LabelRetainUntil carries the date (YYYY-MM-DD) until which an old home
	// claim is kept after its home moved to another mode. The home's
	// status.migration.retainUntil is the exact time.
	LabelRetainUntil = Group + "/retain-until"
	// LabelLive ("true") marks the home claim a mode change switched to, so a
	// home made again without its status finds the claim that holds the files.
	LabelLive = Group + "/live"
	// LabelProbe carries the short hash of a storage probe's nonce on every
	// object the probe makes, so the controller finds exactly its own.
	LabelProbe = Group + "/probe"
	// LabelName carries a Workspace's short name: the one in its URL and in
	// the API.
	LabelName = Group + "/name"
	// LabelManaged ("true") marks a namespace the controller keeps for
	// workspaces; the gateway finds the namespaces to read by it.
	LabelManaged = Group + "/managed"
	// PolicyAllowGateway is the NetworkPolicy in every app namespace that
	// lets only gateway pods reach apps; the NetworkPolicy probe tests it.
	PolicyAllowGateway = "allow-gateway-only"
	// LabelJob carries the job's row id on its CronJob, Jobs and their pods.
	// It is how the controller maps a runtime object back to its row and
	// finds the ones whose row is gone.
	LabelJob = Group + "/job"
	// LabelRole marks platform pods in the workspace namespace (file pods).
	LabelRole = Group + "/role"
	// LabelSession marks session Secrets; its value is the session kind
	// ("ui", "app" or "ticket").
	LabelSession = Group + "/session"
	// LabelIdentity carries the Identity name a session belongs to.
	LabelIdentity = Group + "/identity"
	// LabelExpires carries a session's expiry in unix seconds so the janitor
	// finds expired sessions without reading Secret data.
	LabelExpires = Group + "/expires"
	// LabelParent carries the hashed id of the ui session an app session or
	// ticket was minted from.
	LabelParent = Group + "/parent"
	// LabelBackup tells backup tooling how to treat a Secret the gateway
	// writes (regenerate, carry or never).
	LabelBackup = Group + "/backup"
	// BackupCarry is the LabelBackup value for Secrets that are copied into
	// a backup and restored as they are.
	BackupCarry = "carry"
	// LabelToken marks personal API token Secrets; its value is "personal"
	// or "app" (a token the controller made for one app).
	LabelToken = Group + "/token"
	// LabelAppNamespace carries the namespace of the app an app token is
	// bound to (LabelWorkspace carries its object name).
	LabelAppNamespace = Group + "/app-namespace"
	// LabelPullSecret marks the copies of the platform image's pull Secrets
	// the controller keeps in each app namespace; Secrets without it are
	// never changed.
	LabelPullSecret = Group + "/pull-secret"
	// AnnotationLastUsed carries when a token was last used, in unix seconds.
	AnnotationLastUsed = Group + "/last-used"
	// AnnotationTokenID carries, on an app's API token Secret, the id of the
	// token it holds.
	AnnotationTokenID = Group + "/token-id"
	// AnnotationTokenScope carries, on an app's API token Secret, the scope
	// of the token it holds ("read" or "read-write"); absent means
	// read-write, the only scope made before it existed.
	AnnotationTokenScope = Group + "/token-scope"
	// AnnotationAPIAccess carries the API access an app's pod was built
	// with, so a change restarts the pod.
	AnnotationAPIAccess = Group + "/api-access"
	// AnnotationTokenRevoked is set on a Workspace by the gateway when its
	// owner revokes the app's token; the controller then makes a new one.
	AnnotationTokenRevoked = Group + "/token-revoked"

	// AnnotationOwnerID carries the full owner ID on a home claim, so two
	// users whose slugs collide cannot share a home.
	AnnotationOwnerID = Group + "/owner-id"
	// AnnotationGroup carries the verbatim group on a folder claim. Claims
	// outlive their folder, so a new folder of the same name for another
	// group must not take one over.
	AnnotationGroup = Group + "/group"
	// AnnotationProbeDone carries the last storage probe step that finished
	// ("write", "check" or "confirm") on the probe claim. It is the probe's
	// progress, so it survives Jobs being cleaned up by their TTL.
	AnnotationProbeDone = Group + "/probe-done"
	// AnnotationProbeNode carries the node the probe's write step ran on.
	AnnotationProbeNode = Group + "/probe-node"
	// AnnotationProbeCheckNode carries the node the probe's check step ran
	// on. The probe tested two nodes only when it differs from the write node.
	AnnotationProbeCheckNode = Group + "/probe-check-node"
	// AnnotationRestart carries the restart request (an app's
	// RestartRequestedAt) a Pod was built after, so a newer request replaces
	// the Pod once and a resync never does.
	AnnotationRestart = Group + "/restart"
	// AnnotationSize carries what an app's pod was built at (the size name,
	// or the custom amount's sizes.Size.Key), so a changed size or custom
	// amount restarts the pod.
	AnnotationSize = Group + "/size"
	// AnnotationRun carries a fingerprint of the command, image and port a
	// Workspace pod was built with, so changing one restarts the pod.
	AnnotationRun = Group + "/run"
	// AnnotationGitToken carries, on an app's repository token Secret, the
	// generation of the paste it holds, and on a pod the generation its clone
	// used, so a newer paste replaces the pod once.
	AnnotationGitToken = Group + "/git-token"
	// AnnotationSecrets carries, on an app's pod and on its secrets Secret,
	// the fingerprint of the secrets it was built with (names and versions,
	// never a value).
	AnnotationSecrets = Group + "/secrets"
	// AnnotationLayout carries, on an app's pod, a fingerprint of what it
	// mounts and runs as (claims, read-only or not, group ids, uid) and of
	// the template fields its pod is built from, so a person leaving a group,
	// an app being shared or a template change replaces the pod.
	AnnotationLayout = Group + "/layout"

	// RoleStorageProbe is the LabelRole value of the storage probe's claim
	// and Jobs.
	RoleStorageProbe = "storage-probe"
	// RoleHomePrepare is the LabelRole value of the Job that readies a new
	// home volume.
	RoleHomePrepare = "home-prepare"
	// RoleFolderPrepare is the LabelRole value of the Job that readies a new
	// group folder volume.
	RoleFolderPrepare = "folder-prepare"
	// RoleHomeMigrate is the LabelRole value of the Job that copies a home to
	// another mode.
	RoleHomeMigrate = "home-migrate"
	// RoleHomeBackup and RoleHomeRestore are the LabelRole values of the
	// Jobs that back a home up to the backup bucket and bring it back.
	RoleHomeBackup  = "home-backup"
	RoleHomeRestore = "home-restore"
	// RoleHomeOwnerProbe is the LabelRole value of the Job that reads who
	// owns the files of a home volume without an account.
	RoleHomeOwnerProbe = "home-owner-probe"
	// RoleFolderBackup is the LabelRole value of a shared folder's backup Job.
	RoleFolderBackup = "folder-backup"
	// RoleFiles is the LabelRole value of a person's file picker pod, which
	// mounts the home read-only.
	RoleFiles = "files"
	// RoleStorageQuota is the LabelRole value of the ResourceQuota that caps a
	// team namespace's storage.
	RoleStorageQuota = "storage-quota"
	// RoleNamespaceLimits is the LabelRole value of the ResourceQuota and
	// LimitRange of limits.namespace in a per-user or per-team namespace.
	RoleNamespaceLimits = "namespace-limits"

	// SampleCopyContainer is the init container that copies a template's
	// sample into the home; its failure is reported as SampleMissing.
	SampleCopyContainer = "sample-copy"

	// SessionCookieName is the browser cookie that carries the session token.
	SessionCookieName = ProductName + "_session"
	// AppCookieName is the cookie an app host sets after a ticket is redeemed.
	AppCookieName = ProductName + "_app"
	// OIDCFlowCookiePrefix starts the name of the cookie of each OpenID
	// Connect sign-in under way.
	OIDCFlowCookiePrefix = ProductName + "_oidc_"
	// SessionSecretPrefix starts the name of every session Secret.
	SessionSecretPrefix = ProductName + "-session-"
	// TokenSecretPrefix starts the name of every API token Secret.
	TokenSecretPrefix = ProductName + "-token-"
	// DefaultWorkspaceNamespace is where Workspaces live unless configured.
	DefaultWorkspaceNamespace = ProductName + "-apps"
	// FilesPodPrefix starts the name of each user's file picker pod.
	FilesPodPrefix = ProductName + "-files-"
)

// hostCookiePrefix marks a cookie a browser keeps only when it is Secure,
// has no Domain and has Path "/" (RFC 6265bis section 4.1.3.2).
const hostCookiePrefix = "__Host-"

// CookieName is the name a gateway cookie called name is set and read under.
// A Secure cookie gets the __Host- prefix, so no other host under the same
// domain can set one in its place; plain http cannot carry the prefix, so
// there the name is used as it is.
func CookieName(name string, secure bool) string {
	if secure {
		return hostCookiePrefix + name
	}
	return name
}
