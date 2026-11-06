package reasons

import (
	"fmt"
	"maps"
)

// MessageCode names a short request or form message: a problem title or
// the text shown next to one form field. Status and failure explanations
// for apps are Reasons; these are the second group of the one catalog.
type MessageCode string

const (
	MsgSignInRequired     MessageCode = "SignInRequired"
	MsgSessionEnded       MessageCode = "SessionEnded"
	MsgWrongCredentials   MessageCode = "WrongCredentials"
	MsgCredentialsMissing MessageCode = "CredentialsMissing"
	MsgTooManyAttempts    MessageCode = "TooManyAttempts"
	MsgTooManyAppOpens    MessageCode = "TooManyAppOpens"
	MsgTooManySearches    MessageCode = "TooManySearches"
	MsgNotAllowedFromApp  MessageCode = "NotAllowedFromApp"
	// The proxy while apps share an origin: with the UI (one host) or with
	// each other (path mode with an apps host).
	MsgAdminOpenNeedsAppsHost MessageCode = "AdminOpenNeedsAppsHost"
	MsgPreviewNeedsAppsHost   MessageCode = "PreviewNeedsAppsHost"
	MsgSharedNeedsAppsHost    MessageCode = "SharedNeedsAppsHost"
	// A platform admin and someone else's app that runs code as its owner.
	MsgOwnerOnlyOpens        MessageCode = "OwnerOnlyOpens"
	MsgOpenedFromAnotherApp  MessageCode = "OpenedFromAnotherApp"
	MsgAppNotFound           MessageCode = "AppNotFound"
	MsgAppChanged            MessageCode = "AppChanged"
	MsgTemplateChanged       MessageCode = "TemplateChanged"
	MsgTemplateManagedByHelm MessageCode = "TemplateManagedByHelm"
	MsgTemplateNameInvalid   MessageCode = "TemplateNameInvalid"
	MsgNameNeedsLetter       MessageCode = "NameNeedsLetter"
	MsgNameInvalid           MessageCode = "NameInvalid"
	MsgNameTaken             MessageCode = "NameTaken"
	MsgNameTakenByYou        MessageCode = "NameTakenByYou" // arg: existing display name
	MsgTemplateUnavailable   MessageCode = "TemplateUnavailable"
	MsgSizeUnavailable       MessageCode = "SizeUnavailable"
	MsgSizeTooBig            MessageCode = "SizeTooBig" // args: the largest machine's CPU and memory in words
	MsgSizeTooBigShort       MessageCode = "SizeTooBigShort"
	MsgSizeNoMachine         MessageCode = "SizeNoMachine" // arg: what the size needs in words, with its GPUs and labels
	// Custom CPU and memory (authz.ActCustomSize and the request checks).
	MsgCustomSizeNotAllowed MessageCode = "CustomSizeNotAllowed"
	MsgCustomSizeOverLimit  MessageCode = "CustomSizeOverLimit" // arg: the limit in words, "4 CPU and 16 GB"
	MsgCustomSizeTooSmall   MessageCode = "CustomSizeTooSmall"  // args: the smallest CPU and memory in words
	MsgSizeOrCustom         MessageCode = "SizeOrCustom"
	MsgAPIAccessInvalid     MessageCode = "APIAccessInvalid"
	MsgAPIAccessTooHigh     MessageCode = "APIAccessTooHigh" // arg: highest allowed level in plain words
	// MsgAPIAccessActsAsOwner explains what API access means for sharing.
	MsgAPIAccessActsAsOwner MessageCode = "APIAccessActsAsOwner"
	MsgAPIAccessNone        MessageCode = "APIAccessNone" // the words for each level, used inside MsgAPIAccessTooHigh
	MsgAPIAccessRead        MessageCode = "APIAccessRead"
	MsgAPIAccessReadWrite   MessageCode = "APIAccessReadWrite"
	// Stop when idle (authz.ActIdleStop and the stopped app's status).
	MsgIdleStopOutOfRange    MessageCode = "IdleStopOutOfRange" // args: the shortest and longest timeout in words
	MsgIdleStopOffNotAllowed MessageCode = "IdleStopOffNotAllowed"
	MsgStoppedIdle           MessageCode = "StoppedIdle" // arg: the timeout in words
	MsgFileInvalid           MessageCode = "FileInvalid"
	MsgFileWrongType         MessageCode = "FileWrongType" // arg: comma-separated extensions
	MsgFolderOutsideHome     MessageCode = "FolderOutsideHome"
	MsgPathTooDeep           MessageCode = "PathTooDeep" // arg: the most folders a path may have
	MsgFolderNotFound        MessageCode = "FolderNotFound"
	MsgFolderListFailed      MessageCode = "FolderListFailed"
	MsgFileNotFound          MessageCode = "FileNotFound"
	// Saving a file from the editor (PUT /api/files/raw).
	MsgFileChanged        MessageCode = "FileChanged"
	MsgFileExists         MessageCode = "FileExists"
	MsgFileNotText        MessageCode = "FileNotText"
	MsgFileTooLargeToEdit MessageCode = "FileTooLargeToEdit"
	MsgFileVersionNeeded  MessageCode = "FileVersionNeeded"
	MsgNoSample           MessageCode = "NoSample" // arg: template display name
	MsgSampleNameTaken    MessageCode = "SampleNameTaken"
	MsgInvalidRequest     MessageCode = "InvalidRequest"
	MsgNoSuchAddress      MessageCode = "NoSuchAddress"
	MsgMethodNotAllowed   MessageCode = "MethodNotAllowed"
	MsgServerError        MessageCode = "ServerError"

	MsgJobNotFound                MessageCode = "JobNotFound"
	MsgRunNotFound                MessageCode = "RunNotFound"
	MsgCronInvalid                MessageCode = "CronInvalid"     // arg: sanitized field detail
	MsgTimeZoneInvalid            MessageCode = "TimeZoneInvalid" // arg: zone
	MsgTimeZoneRequired           MessageCode = "TimeZoneRequired"
	MsgEngineNotInTemplate        MessageCode = "EngineNotInTemplate" // args: template, engine
	MsgKernelNotInTemplate        MessageCode = "KernelNotInTemplate" // args: template, kernel
	MsgJobsNotOnTemplate          MessageCode = "JobsNotOnTemplate"
	MsgTimeoutTooLong             MessageCode = "TimeoutTooLong"  // arg: max
	MsgParamInvalid               MessageCode = "ParamInvalid"    // args: name, type
	MsgFileWrongEngine            MessageCode = "FileWrongEngine" // arg: comma-separated extensions
	MsgAppLoginNeedsNetworkPolicy MessageCode = "AppLoginNeedsNetworkPolicy"
	MsgResumeStartsMissedRun      MessageCode = "ResumeStartsMissedRun"
	MsgDSTSkipped                 MessageCode = "DSTSkipped"  // args: day in words, the runs (MsgDSTOneRun or MsgDSTRuns)
	MsgDSTRepeated                MessageCode = "DSTRepeated" // args: day in words, the runs (MsgDSTOneRun or MsgDSTRuns)
	MsgDSTOneRun                  MessageCode = "DSTOneRun"   // arg: wall time
	MsgDSTRuns                    MessageCode = "DSTRuns"     // args: first and last wall time
	MsgLogsGone                   MessageCode = "LogsGone"
	MsgOutputTooLarge             MessageCode = "OutputTooLarge"
	MsgToolAnswerTooLarge         MessageCode = "ToolAnswerTooLarge" // arg: the cap in MiB
	MsgTooManyStreams             MessageCode = "TooManyStreams"     // arg: how many logs one person may follow at once
	MsgThumbnailBusy              MessageCode = "ThumbnailBusy"
	MsgRunStillActive             MessageCode = "RunStillActive"
	MsgNotAMember                 MessageCode = "NotAMember"
	MsgShareNotAllowed            MessageCode = "ShareNotAllowed"
	MsgShareTooOpen               MessageCode = "ShareTooOpen"       // arg: highest allowed level in plain words
	MsgShareTargetUnknown         MessageCode = "ShareTargetUnknown" // arg: the name that matched nobody
	MsgShareNeedsAppsHost         MessageCode = "ShareNeedsAppsHost"
	MsgShareExposesCode           MessageCode = "ShareExposesCode" // what sharing an app that runs any code hands its viewers
	MsgShareExposesApp            MessageCode = "ShareExposesApp"  // ... an app that runs its own code
	MsgShareExposesSite           MessageCode = "ShareExposesSite" // ... a static site
	MsgTicketExpired              MessageCode = "TicketExpired"
	MsgAccessPrivate              MessageCode = "AccessPrivate" // the words for each level, used inside MsgShareTooOpen
	MsgAccessRestricted           MessageCode = "AccessRestricted"
	MsgAccessSignedIn             MessageCode = "AccessSignedIn"
	MsgAccessPublic               MessageCode = "AccessPublic"
	MsgAccessLabelRestricted      MessageCode = "AccessLabelRestricted" // the General access buttons
	MsgAccessLabelSignedIn        MessageCode = "AccessLabelSignedIn"
	MsgAccessLabelPublic          MessageCode = "AccessLabelPublic"
	MsgNothingShared              MessageCode = "NothingShared"
	MsgPersonInactive             MessageCode = "PersonInactive"
	MsgIdpOutageBanner            MessageCode = "IdpOutageBanner" // arg: the time the grace period ends
	MsgTemplateAdminOnly          MessageCode = "TemplateAdminOnly"
	MsgLocalSignInLabel           MessageCode = "LocalSignInLabel"
	MsgOIDCSignInLabel            MessageCode = "OIDCSignInLabel"
	MsgPickTeam                   MessageCode = "PickTeam"
	MsgNoTeam                     MessageCode = "NoTeam"
	MsgTeamNotYours               MessageCode = "TeamNotYours"
	MsgRunNotStarted              MessageCode = "RunNotStarted"
	MsgJobChanged                 MessageCode = "JobChanged"

	MsgCommandRequired         MessageCode = "CommandRequired"
	MsgCommandTooLong          MessageCode = "CommandTooLong"
	MsgCommandInvalid          MessageCode = "CommandInvalid" // arg: the parser's description with line:column
	MsgCommandVariable         MessageCode = "CommandVariable"
	MsgCommandPlaceholder      MessageCode = "CommandPlaceholder"
	MsgCommandNotTaken         MessageCode = "CommandNotTaken"
	MsgJobCommandRequired      MessageCode = "JobCommandRequired"
	MsgJobCommandVariable      MessageCode = "JobCommandVariable"
	MsgJobCommandPlaceholder   MessageCode = "JobCommandPlaceholder"
	MsgJobCommandNotTaken      MessageCode = "JobCommandNotTaken"
	MsgImageRequired           MessageCode = "ImageRequired"
	MsgImageInvalid            MessageCode = "ImageInvalid"
	MsgImageRegistryNotAllowed MessageCode = "ImageRegistryNotAllowed" // args: the image's registry, the allowed list
	MsgImageNotTaken           MessageCode = "ImageNotTaken"
	MsgPortInvalid             MessageCode = "PortInvalid"
	MsgPortNotTaken            MessageCode = "PortNotTaken"

	MsgTokenInvalid            MessageCode = "TokenInvalid"
	MsgTokenReadOnly           MessageCode = "TokenReadOnly"
	MsgTokenSignInAgain        MessageCode = "TokenSignInAgain"
	MsgTokenCannotManageTokens MessageCode = "TokenCannotManageTokens"
	MsgTokenNotFound           MessageCode = "TokenNotFound"
	MsgTokenNameInvalid        MessageCode = "TokenNameInvalid"
	MsgTokenScopeInvalid       MessageCode = "TokenScopeInvalid"
	MsgTokenExpiryInvalid      MessageCode = "TokenExpiryInvalid"
	MsgTokenExpiryTooLong      MessageCode = "TokenExpiryTooLong" // arg: the most days allowed
	MsgTokenAdminNotAllowed    MessageCode = "TokenAdminNotAllowed"
	MsgSignInBusy              MessageCode = "SignInBusy"
	MsgModeWordsPerUser        MessageCode = "ModeWordsPerUser" // where files are, inside a sentence
	MsgModeWordsShared         MessageCode = "ModeWordsShared"
	MsgHomeLabelPerUser        MessageCode = "HomeLabelPerUser" // the sidebar line
	MsgHomeLabelShared         MessageCode = "HomeLabelShared"
	MsgPlaceInvalid            MessageCode = "PlaceInvalid"
	MsgNotFound                MessageCode = "NotFound"
	MsgHomesPerNode            MessageCode = "HomesPerNode" // args: count, CSI driver
	// The first line of the admin storage page: where files are.
	MsgStorageSummaryOneMachine MessageCode = "StorageSummaryOneMachine"
	MsgStorageSummaryPerUser    MessageCode = "StorageSummaryPerUser"
	MsgStorageSummaryShared     MessageCode = "StorageSummaryShared"
	MsgStorageNothingNeeded     MessageCode = "StorageNothingNeeded"

	// Actions and plans: what a proposed change does, and the plan's states.
	MsgActionStart      MessageCode = "ActionStart"      // arg: app display name
	MsgActionStop       MessageCode = "ActionStop"       // arg: app display name
	MsgActionRestart    MessageCode = "ActionRestart"    // arg: app display name
	MsgActionDelete     MessageCode = "ActionDelete"     // arg: app display name
	MsgActionResize     MessageCode = "ActionResize"     // args: app display name, size display name
	MsgActionNotNeeded  MessageCode = "ActionNotNeeded"  // arg: app display name
	MsgActionNotAllowed MessageCode = "ActionNotAllowed" // a proposal outside what may be asked here
	MsgPlanNotFound     MessageCode = "PlanNotFound"
	MsgPlanExpired      MessageCode = "PlanExpired"
	MsgPlanDone         MessageCode = "PlanDone" // arg: the state in words (MsgPlanState*)
	MsgPlanStateApplied MessageCode = "PlanStateApplied"
	MsgPlanStateReject  MessageCode = "PlanStateRejected"
	MsgPlanStateFailed  MessageCode = "PlanStateFailed"
	MsgPlanSessionOnly  MessageCode = "PlanSessionOnly"
	// Giving an app a secret, or taking it off, by plan.
	MsgActionAttachSecret     MessageCode = "ActionAttachSecret"     // args: app display name, secret name, variable name
	MsgActionAttachSecretInfo MessageCode = "ActionAttachSecretInfo" // what the action does, for an agent
	MsgActionDetachSecret     MessageCode = "ActionDetachSecret"     // args: secret name, variable name, app display name
	MsgActionDetachSecretInfo MessageCode = "ActionDetachSecretInfo" // what the action does, for an agent
	MsgAgentOwnAppSecret      MessageCode = "AgentOwnAppSecret"
	MsgEnvNoSecret            MessageCode = "EnvNoSecret" // args: app display name, variable name
	MsgPlansListPersonOnly    MessageCode = "PlansListPersonOnly"

	// AI features: their states, shown in place of an answer.
	MsgAIOff            MessageCode = "AIOff"
	MsgAISelfTestFailed MessageCode = "AISelfTestFailed"
	MsgAIUnavailable    MessageCode = "AIUnavailable"
	MsgAIBusy           MessageCode = "AIBusy" // arg: seconds to wait
	MsgAIInvalidAnswer  MessageCode = "AIInvalidAnswer"
	MsgAIBudgetUsed     MessageCode = "AIBudgetUsed" // arg: reset time in words
	MsgAIRedacted       MessageCode = "AIRedacted"   // arg: count
	MsgAIWithheld       MessageCode = "AIWithheld"   // arg: count
	MsgAITrimmed        MessageCode = "AITrimmed"    // args: used, total
	MsgAIOwnerOnly      MessageCode = "AIOwnerOnly"
	MsgAILinkRemoved    MessageCode = "AILinkRemoved"
	MsgAINothingToTell  MessageCode = "AINothingToTell"
	MsgAITooManyAsks    MessageCode = "AITooManyAsks"
	MsgAdminOnly        MessageCode = "AdminOnly"
	MsgRestoreHeld      MessageCode = "RestoreHeldStart"
	MsgRestoreNotFound  MessageCode = "RestoreNotFound"

	MsgBackupNotFound      MessageCode = "BackupNotFound"
	MsgBackupNotHere       MessageCode = "BackupNotHere"
	MsgBackupUnreadable    MessageCode = "BackupUnreadable"
	MsgNoBackupBucket      MessageCode = "NoBackupBucket"
	MsgHomeNotFound        MessageCode = "HomeNotFound"
	MsgSnapshotNotFound    MessageCode = "SnapshotNotFound"
	MsgHomeRestoreRunning  MessageCode = "HomeRestoreRunning"
	MsgNoBackupJob         MessageCode = "NoBackupJob"
	MsgBackupRunUnknown    MessageCode = "BackupRunUnknown"
	MsgRestoreOwnerGone    MessageCode = "RestoreOwnerGone"    // arg: the owner's identity name
	MsgRestoreTemplateGone MessageCode = "RestoreTemplateGone" // arg: the template name
	MsgRestoreExists       MessageCode = "RestoreExists"
	MsgRestoreCreate       MessageCode = "RestoreCreate"
	MsgRestoreCreated      MessageCode = "RestoreCreated"

	MsgImportTooLarge   MessageCode = "ImportTooLarge"
	MsgImportTooMany    MessageCode = "ImportTooMany" // arg: the most apps a file may hold
	MsgImportUnreadable MessageCode = "ImportUnreadable"
	MsgImportNotAnApp   MessageCode = "ImportNotAnApp" // arg: the decoder's description
	MsgImportExists     MessageCode = "ImportExists"   // arg: the display name

	MsgThumbnailNotImage MessageCode = "ThumbnailNotImage"
	MsgThumbnailTooLarge MessageCode = "ThumbnailTooLarge"
	MsgThumbnailNone     MessageCode = "ThumbnailNone"
	MsgImportCreate      MessageCode = "ImportCreate"
	MsgImportCreated     MessageCode = "ImportCreated"
	MsgImportSkipped     MessageCode = "ImportSkipped"

	// Apps from a Git repository and their previews.
	MsgGitURLInvalid        MessageCode = "GitURLInvalid"
	MsgGitURLNotHTTPS       MessageCode = "GitURLNotHTTPS"
	MsgGitURLHasCredentials MessageCode = "GitURLHasCredentials"
	MsgSourcePathInvalid    MessageCode = "SourcePathInvalid"
	MsgPreviewsNeedGitHub   MessageCode = "PreviewsNeedGitHub"
	MsgPreviewsTooMany      MessageCode = "PreviewsTooMany" // arg: the most previews allowed
	MsgPreviewForksWarning  MessageCode = "PreviewForksWarning"
	MsgTemplateNeedsRepo    MessageCode = "TemplateNeedsRepo"
	MsgTemplateNoRepo       MessageCode = "TemplateNoRepo"
	MsgWebhookSecretOnce    MessageCode = "WebhookSecretOnce"
	MsgPreviewManaged       MessageCode = "PreviewManaged"
	MsgNothingToRedeploy    MessageCode = "NothingToRedeploy"
	MsgWebhookSignature     MessageCode = "WebhookSignature"
	MsgWebhookTooLarge      MessageCode = "WebhookTooLarge"
	MsgWebhookRateLimited   MessageCode = "WebhookRateLimited"

	// Secrets.
	MsgSecretNotFound         MessageCode = "SecretNotFound"
	MsgSecretNameInvalid      MessageCode = "SecretNameInvalid"
	MsgSecretNameTaken        MessageCode = "SecretNameTaken"
	MsgSecretValueInvalid     MessageCode = "SecretValueInvalid"
	MsgSecretNoteTooLong      MessageCode = "SecretNoteTooLong"
	MsgSecretInUse            MessageCode = "SecretInUse" // arg: the apps and jobs that use it
	MsgSecretsTooMany         MessageCode = "SecretsTooMany"
	MsgSecretsUnavailable     MessageCode = "SecretsUnavailable"
	MsgSecretUnknown          MessageCode = "SecretUnknown" // arg: the secret name
	MsgEnvNameInvalid         MessageCode = "EnvNameInvalid"
	MsgEnvNameReserved        MessageCode = "EnvNameReserved" // arg: the name
	MsgEnvNameRepeated        MessageCode = "EnvNameRepeated" // arg: the name
	MsgEnvTooMany             MessageCode = "EnvTooMany"
	MsgSecretsNotOnPreviews   MessageCode = "SecretsNotOnPreviews"
	MsgSecretsOwnerOnly       MessageCode = "SecretsOwnerOnly"
	MsgSecretsNotForAppTokens MessageCode = "SecretsNotForAppTokens"
	MsgSecretsHeldOwnerOnly   MessageCode = "SecretsHeldOwnerOnly"
	MsgWorkloadTokenReadOnly  MessageCode = "WorkloadTokenReadOnly"
	MsgAgentNeedsPerson       MessageCode = "AgentNeedsPerson"
	MsgAgentBrowserOnly       MessageCode = "AgentBrowserOnly"
	MsgAgentBaseURLInvalid    MessageCode = "AgentBaseURLInvalid"
	MsgAgentModelInvalid      MessageCode = "AgentModelInvalid"
	MsgAgentKeyRequired       MessageCode = "AgentKeyRequired"
	MsgAgentRequestInvalid    MessageCode = "AgentRequestInvalid"
	MsgAgentUnavailable       MessageCode = "AgentUnavailable"
	MsgAgentPromptNotTaken    MessageCode = "AgentPromptNotTaken"
	MsgAISettingsBaseURL      MessageCode = "AISettingsBaseURL"
	MsgAISettingsBaseURLHost  MessageCode = "AISettingsBaseURLHost"
	MsgAISettingsModel        MessageCode = "AISettingsModel"
	MsgAISettingsKey          MessageCode = "AISettingsKey"
	MsgAISettingsKeyOrRemove  MessageCode = "AISettingsKeyOrRemove"
	MsgAISettingsOff          MessageCode = "AISettingsOff"
	MsgRewriteOwnerOnly       MessageCode = "RewriteOwnerOnly"
	MsgEnvValueInvalid        MessageCode = "EnvValueInvalid"
	MsgEnvValueOrSecret       MessageCode = "EnvValueOrSecret"
	MsgWebhookUnknown         MessageCode = "WebhookUnknown"

	// Environments picked from an environment manager (internal/ext/envsource).
	MsgEnvironmentsOff        MessageCode = "EnvironmentsOff"
	MsgEnvironmentUnknown     MessageCode = "EnvironmentUnknown"
	MsgEnvironmentSignIn      MessageCode = "EnvironmentSignIn"
	MsgEnvironmentRefused     MessageCode = "EnvironmentRefused"
	MsgEnvironmentUnreachable MessageCode = "EnvironmentUnreachable"
	MsgEnvironmentTooLarge    MessageCode = "EnvironmentTooLarge" // arg: what does not fit

	// The admin's per-person caps (limits in the platform config); each
	// takes the cap.
	MsgLimitApps          MessageCode = "LimitApps"
	MsgLimitRunningApps   MessageCode = "LimitRunningApps"
	MsgLimitPreviews      MessageCode = "LimitPreviews"
	MsgLimitJobs          MessageCode = "LimitJobs"
	MsgLimitRunsPerJob    MessageCode = "LimitRunsPerJob"
	MsgLimitRunsPerPerson MessageCode = "LimitRunsPerPerson"
	MsgLimitAPITokens     MessageCode = "LimitAPITokens"
	MsgLimitPendingPlans  MessageCode = "LimitPendingPlans"
)

var messages = map[MessageCode]string{
	MsgSignInRequired:     "Sign in required",
	MsgSessionEnded:       "Your session ended. Sign in again.",
	MsgWrongCredentials:   "Wrong username or password",
	MsgCredentialsMissing: "Enter your username and password.",
	MsgTooManyAttempts:    "Too many sign-in attempts. Wait a minute, then try again.",
	MsgTooManyAppOpens:    "Too many apps opened in a short time. Wait a moment, then try again.",
	MsgTooManySearches:    "Too many searches in a short time. Wait a moment, then try again.",
	MsgNotAllowedFromApp:  "Changes cannot be made from inside an app or from another site. Use the web UI, or an API token from Settings.",
	MsgAdminOpenNeedsAppsHost: "Admins cannot open other people's apps through admin rights while apps share one address, because the app's page could act as you, with your admin rights. " +
		"Ask the owner to share it with you, or give each app its own address (urls.mode subdomain with urls.appsDomain).",
	MsgPreviewNeedsAppsHost: "Previews of pull requests do not open while apps share one address, because the pull request's code could act as you. " +
		"Ask your platform admin to give each app its own address (urls.mode subdomain with urls.appsDomain).",
	MsgSharedNeedsAppsHost: "Shared apps do not open while apps share this page's address, because the app's page could act as you. " +
		"Ask the owner to show you, or ask your platform admin to give apps their own address (urls.appsDomain).",
	MsgOwnerOnlyOpens:        "Only its owner opens this app: it runs code as its owner, with their files and secrets. You can still start, stop, delete it and see its logs. Ask the owner to show you.",
	MsgOpenedFromAnotherApp:  "This app cannot be opened from inside another app. Open it from its own address or from your list of apps.",
	MsgAppNotFound:           "No app with that name.",
	MsgAppChanged:            "The app changed while you were working on it. Reload and try again.",
	MsgTemplateChanged:       "The template changed while you were working on it. Reload and try again.",
	MsgTemplateManagedByHelm: "This template comes with the installation. Change it in the Helm values, not here.",
	MsgTemplateNameInvalid:   "Use a template name of up to 253 lowercase letters, numbers, dots and hyphens that starts and ends with a letter or number.",
	MsgNameNeedsLetter:       "Use at least one letter or number in the name",
	MsgNameInvalid:           "Use a name of up to 63 letters, numbers and spaces.",
	MsgNameTaken:             "That name is taken. Pick another name.",
	MsgNameTakenByYou:        "You already have an app called %q.",
	MsgTemplateUnavailable:   "Pick one of the templates on the list.",
	MsgSizeUnavailable:       "Pick one of the sizes on the list.",
	MsgSizeTooBig:            "Bigger than any machine here: the largest has %s CPU and %s.",
	MsgSizeTooBigShort:       "Too big",
	MsgSizeNoMachine:         "No machine here has what this size needs: %s.",
	MsgCustomSizeNotAllowed:  "Custom CPU and memory are not open to you. Pick one of the sizes on the list.",
	MsgCustomSizeOverLimit:   "You can ask for up to %s.",
	MsgCustomSizeTooSmall:    "Ask for at least %s CPU and %s.",
	MsgSizeOrCustom:          "Pick a size or a custom amount, not both.",
	MsgAPIAccessInvalid:      "Pick None, Read or Read and write.",
	MsgAPIAccessTooHigh:      "This kind of app can have %s API access at most.",
	MsgAPIAccessActsAsOwner:  "This app can call the API as you. Anyone you share it with can make it act as you.",
	MsgAPIAccessNone:         "no",
	MsgAPIAccessRead:         "read",
	MsgAPIAccessReadWrite:    "read and write",
	MsgIdleStopOutOfRange:    "Pick between %s and %s.",
	MsgIdleStopOffNotAllowed: "Apps here must stop when idle. Pick how long they may sit unused.",
	MsgStoppedIdle:           "Stopped after %s without use",
	MsgFileInvalid:           "Pick a file inside your home folder.",
	MsgFileWrongType:         "This template opens %s files. Pick one of those.",
	MsgFolderOutsideHome:     "That folder is outside your home folder",
	MsgPathTooDeep:           "That path has too many folders. Paths may have at most %d.",
	MsgFolderNotFound:        "That folder does not exist",
	MsgFolderListFailed:      "Your home folder could not be listed",
	MsgFileNotFound:          "That file does not exist",
	MsgFileChanged:           "This file changed since you opened it. Reload it to see the new version, then make your edit again.",
	MsgFileExists:            "A file or folder of that name is already here. Pick another name.",
	MsgFileNotText:           "Only text files can be edited here.",
	MsgFileTooLargeToEdit:    "This file is too large to edit here (2 MiB at most). Open it in an app.",
	MsgFileVersionNeeded:     "Say which version of the file this replaces (If-Match), or that it is new (If-None-Match: *).",
	MsgNoSample:              "%s has no sample to start.",
	MsgSampleNameTaken:       "You already have many samples of this kind. Delete one, then try again.",
	MsgInvalidRequest:        "Something in this form is not valid. Check it and try again.",
	MsgNoSuchAddress:         "There is nothing at this address.",
	MsgMethodNotAllowed:      "This address does not accept that kind of request.",
	MsgServerError:           "Something went wrong on our side. Try again in a moment.",

	MsgJobNotFound:                "No job with that name.",
	MsgRunNotFound:                "No run with that id.",
	MsgCronInvalid:                "This schedule is not valid: %s",
	MsgTimeZoneInvalid:            "%s is not a time zone. Use a name like Europe/Berlin.",
	MsgTimeZoneRequired:           "Pick a time zone for this schedule.",
	MsgEngineNotInTemplate:        "Template %s has no %s, so these files cannot run on it.",
	MsgKernelNotInTemplate:        "Template %s has no %s kernel.",
	MsgJobsNotOnTemplate:          "This template does not allow jobs.",
	MsgTimeoutTooLong:             "The time limit is longer than this template allows (%s at most).",
	MsgParamInvalid:               "Parameter %s must be a %s.",
	MsgFileWrongEngine:            "This engine runs %s files. Pick one of those.",
	MsgAppLoginNeedsNetworkPolicy: "This cluster does not enforce NetworkPolicy, so this template would be reachable without a login. Pick another template.",
	MsgResumeStartsMissedRun:      "Resuming starts one missed run now.",
	MsgDSTSkipped:                 "Daylight saving on %s skips %s.",
	MsgDSTRepeated:                "Daylight saving on %s repeats %s.",
	MsgDSTOneRun:                  "the %s run",
	MsgDSTRuns:                    "the runs from %s to %s",
	MsgLogsGone:                   "This run's log is gone. The run may have ended before it printed anything, or its log file was deleted from your home folder.",
	MsgOutputTooLarge:             "This output is too large to show. Download it from your home folder.",
	MsgToolAnswerTooLarge:         "This answer is larger than %d MiB, too large for an agent tool. Ask for less, or call the API directly.",
	MsgTooManyStreams:             "You are following %d logs at once, the most allowed. Close a log you no longer watch, then try again.",
	MsgThumbnailBusy:              "Too many pictures are being processed right now. Try again in a moment.",
	MsgRunStillActive:             "This run is still going. Cancel it first.",
	MsgNotAMember:                 "Your account can open apps shared with you, but cannot create apps. Ask an admin to add you to a member group.",
	MsgShareNotAllowed:            "Only the owner can change who an app is shared with.",
	MsgShareTooOpen:               "This kind of app can be shared with %s at most.",
	MsgShareExposesCode:           "People you share with can run any code or command as you. They can read, change and delete every file in your home folder and your shared folders, including any keys or tokens kept there.",
	MsgShareExposesApp:            "People you share with use this app as it runs. It runs as you, with your home folder and shared folders, so they see whatever it shows and can make it do whatever its code allows, such as reading or changing your files.",
	MsgShareExposesSite:           "People you share with can see every page and file of this site. The site cannot see your home folder.",
	MsgShareTargetUnknown:         "Nobody is called %q. Check the spelling.",
	MsgTicketExpired:              "That link expired. Open the app again.",
	MsgShareNeedsAppsHost:         "Sharing needs apps served on a separate host from this page, so a shared app cannot act as you here. Ask an admin to set one up.",
	MsgAccessPrivate:              "no one but you",
	MsgAccessRestricted:           "chosen people and groups",
	MsgAccessSignedIn:             "everyone who signs in",
	MsgAccessPublic:               "anyone with the link",
	MsgAccessLabelRestricted:      "Only people added",
	MsgAccessLabelSignedIn:        "Anyone signed in",
	MsgAccessLabelPublic:          "Anyone with the link",
	MsgNothingShared:              "Nothing has been shared with you yet.",
	MsgPersonInactive:             "Account no longer active",
	MsgIdpOutageBanner:            "Sign-in service unreachable. You stay signed in until %s.",
	MsgTemplateAdminOnly:          "Only template admins can change templates.",
	MsgOIDCSignInLabel:            "Sign in with single sign-on",
	MsgLocalSignInLabel:           "Username and password",
	MsgPickTeam:                   "You are in more than one team. Pick the team this app belongs to.",
	MsgNoTeam:                     "You are not in a team that can have apps. Ask an admin to add you to one.",
	MsgTeamNotYours:               "You are not in that team. Pick one of your teams.",
	MsgRunNotStarted:              "This run has not started yet. Try again in a few seconds.",
	MsgJobChanged:                 "The job changed while you were working on it. Reload and try again.",

	MsgCommandRequired:         "Type the command that starts your app.",
	MsgCommandTooLong:          "Keep the command to 2048 characters or fewer.",
	MsgCommandInvalid:          "This command cannot be read (%s). It runs without a shell, so pipes, redirects, ; and $(...) do not work.",
	MsgCommandVariable:         "Variables such as $PORT and ~ are not filled in. Use {{.Port}}, {{.BaseURL}} or {{.Home}}, or put the text in single quotes.",
	MsgCommandPlaceholder:      "The command has a placeholder that does not exist. Use {{.Port}}, {{.BaseURL}}, {{.Home}} or {{.File}}.",
	MsgCommandNotTaken:         "This template starts its own command. Leave the command empty.",
	MsgJobCommandRequired:      "Type the command to run.",
	MsgJobCommandVariable:      "Variables such as $JAVO_PARAM_NAME and ~ are not filled in: the command runs without a shell. Read parameters from the environment in your script, use {{.Home}} for the home folder, or put the text in single quotes.",
	MsgJobCommandPlaceholder:   "The command has a placeholder that does not exist. A job's command can use {{.Home}}.",
	MsgJobCommandNotTaken:      "Only a command job takes a command. Leave the command empty.",
	MsgImageRequired:           "Type the image to run, such as registry.example.com/team/app:1.0.",
	MsgImageInvalid:            "This is not an image name. Use a name such as registry.example.com/team/app:1.0.",
	MsgImageRegistryNotAllowed: "Images from %s are not allowed here. Use an image from %s.",
	MsgImageNotTaken:           "This template runs its own image. Leave the image empty.",
	MsgPortInvalid:             "Use a port from 1 to 65535.",
	MsgPortNotTaken:            "This template sets its own port. Leave the port empty.",

	MsgTokenInvalid:            "This API token is not valid. It may have been revoked or have expired.",
	MsgTokenReadOnly:           "This API token can only read. Use a token that can read and write to make changes.",
	MsgTokenSignInAgain:        "Your API tokens are paused because you have not signed in for a long time. Sign in again to use them.",
	MsgTokenCannotManageTokens: "API tokens cannot create, list or revoke tokens. Use Settings in the browser.",
	MsgTokenNotFound:           "No token with that id.",
	MsgTokenNameInvalid:        "Give the token a name of up to 64 characters.",
	MsgTokenScopeInvalid:       "Pick read, propose, read and write, or admin.",
	MsgTokenAdminNotAllowed:    "Only an admin can make an admin token.",
	MsgSignInBusy:              "Too many sign-ins are being checked right now. Try again in a moment.",
	MsgTokenExpiryInvalid:      "Pick an expiry of 1 day or more.",
	MsgTokenExpiryTooLong:      "Tokens here expire within %d days. Pick a shorter expiry.",
	MsgModeWordsPerUser:        "a private disk",
	MsgModeWordsShared:         "shared storage",
	MsgHomeLabelPerUser:        "Files on one machine",
	MsgHomeLabelShared:         "Files on shared storage",
	MsgPlaceInvalid:            "Pick your home folder or one of your group folders.",
	MsgNotFound:                "Not found.",
	MsgHomesPerNode:            "Up to %d user homes per node (CSINode allocatable count for %s).",

	MsgStorageSummaryOneMachine: "Files are stored on this machine's disk.",
	MsgStorageSummaryPerUser:    "Each person's files are on the disk of one machine, so their apps run there.",
	MsgStorageSummaryShared:     "Files are on shared storage that every machine can use.",
	MsgStorageNothingNeeded:     "Nothing needs attention.",

	MsgActionStart:      "Start %s",
	MsgActionStop:       "Stop %s",
	MsgActionRestart:    "Restart %s",
	MsgActionDelete:     "Delete %s. Its home folder is kept.",
	MsgActionResize:     "Run %s at size %s (it restarts)",
	MsgActionNotNeeded:  "%s is already like that, so there is nothing to change.",
	MsgActionNotAllowed: "That change cannot be asked for here.",
	MsgPlanNotFound:     "No change with that id. It may have expired.",
	MsgPlanExpired:      "This change expired before it was approved. Ask for it again.",
	MsgPlanDone:         "This change was already %s.",
	MsgPlanStateApplied: "applied",
	MsgPlanStateReject:  "turned down",
	MsgPlanStateFailed:  "tried and failed",
	MsgPlanSessionOnly:  "Changes can only be approved in the browser, signed in. API tokens and agents cannot approve them.",

	MsgActionAttachSecret:     "Give %s your secret %s as %s (it restarts)",
	MsgActionAttachSecretInfo: "Give one of your apps one of your secrets as an environment variable, by the secret's name; nobody sees the value",
	MsgActionDetachSecret:     "Stop giving your secret %s as %s to %s (it restarts)",
	MsgActionDetachSecretInfo: "Take one of your secrets off one of your apps, by the variable name the app gets it as",
	MsgAgentOwnAppSecret:      "A coding agent cannot ask for a secret for its own app: it would see the value. Ask for it for the app it deploys.",
	MsgEnvNoSecret:            "%s gets no secret as %s.",
	MsgPlansListPersonOnly:    "An app's or job's own token cannot list the changes waiting for approval. Print the approve link the proposal returned.",

	MsgAIOff:               "AI features are off on this installation.",
	MsgAISelfTestFailed:    "The model did not pass its check. A platform admin can see why.",
	MsgAIUnavailable:       "The AI service cannot be reached right now. The cause and fix above do not depend on it.",
	MsgAIBusy:              "The AI service is busy. Trying again in %d seconds.",
	MsgAIInvalidAnswer:     "The AI answer could not be checked against the evidence, so it is not shown.",
	MsgAIBudgetUsed:        "You used today's AI allowance. It resets at %s.",
	MsgAIRedacted:          "%d values that were or looked like secrets were hidden.",
	MsgAIWithheld:          "%d log lines were not sent: they could not be checked for secrets.",
	MsgAITrimmed:           "Only the newest %d of %d log lines fit.",
	MsgAILinkRemoved:       "(link removed)",
	MsgAIOwnerOnly:         "What is sent is shown only to the app's owner. Secret values are hidden without their names.",
	MsgAINothingToTell:     "This app is not failing, so there is nothing to explain.",
	MsgAITooManyAsks:       "Too many requests to explain a failure. Wait a minute, then try again.",
	MsgAdminOnly:           "Only platform admins can do this.",
	MsgBackupNotFound:      "No database backup with that name.",
	MsgBackupNotHere:       "This install keeps its data in Postgres. Restore a pg_dump backup with pg_restore, as the Backups page of the admin guide shows.",
	MsgBackupUnreadable:    "This backup cannot be read. It may be damaged; try an older one.",
	MsgNoBackupBucket:      "This install has no backup bucket. Set backupTarget in the Helm values, as the Backups page of the admin guide shows.",
	MsgHomeNotFound:        "There is no such home folder.",
	MsgSnapshotNotFound:    "That backup is not in the bucket. Pick a date from the list.",
	MsgHomeRestoreRunning:  "A restore of this home folder is under way. Wait until it ends, then restore again.",
	MsgNoBackupJob:         "There is no backup job yet. Set up backups on Settings, Backups first.",
	MsgBackupRunUnknown:    "There is no backup of that date in the bucket. Pick a date from the list.",
	MsgRestoreOwnerGone:    "Its owner %s has no account here. They must sign in once before their apps can come back.",
	MsgRestoreTemplateGone: "Its template %s does not exist here. Add the template first.",
	MsgRestoreExists:       "An app with this name and owner exists now. It is left as it is.",
	MsgRestoreCreate:       "Will come back and wait for you to start the restored apps.",
	MsgRestoreCreated:      "Back, waiting for you to start the restored apps.",
	MsgImportTooLarge:      "The file is larger than 1 MiB. Split it and import the parts.",
	MsgImportTooMany:       "The file holds more than %d apps. Split it and import the parts.",
	MsgImportUnreadable:    "This is not a YAML file of apps. Export your apps to see the format.",
	MsgImportNotAnApp:      "This entry is not an app this install can read: %s",
	MsgImportExists:        "You already have an app called %s.",
	MsgThumbnailNotImage:   "This is not a PNG or JPEG image. Upload a screenshot saved as PNG or JPEG.",
	MsgThumbnailTooLarge:   "The image is too large. Upload one under 4 MB and at most 16 megapixels.",
	MsgThumbnailNone:       "This app has no picture.",
	MsgImportCreate:        "Will be added, stopped.",
	MsgImportCreated:       "Added, stopped. Start it when you need it.",
	MsgImportSkipped:       "Skipped: you already have an app with this name.",
	MsgRestoreNotFound:     "No restored apps are waiting under that name.",
	MsgRestoreHeld:         "This app was restored from a backup and waits for an admin to start the restored apps.",

	MsgGitURLInvalid:        "Use the address of a Git repository, such as https://github.com/example-org/site.",
	MsgGitURLNotHTTPS:       "Only https addresses work. Use an address that starts with https://.",
	MsgGitURLHasCredentials: "Take the user name and password out of the address. A private repository takes a token instead.",
	MsgSourcePathInvalid:    "Use a folder inside the repository, such as web or docs/site. It cannot start with / or contain ..",
	MsgPreviewsNeedGitHub:   "Previews work with repositories on github.com only.",
	MsgPreviewsTooMany:      "Pick a number of previews from 1 to %d.",
	MsgPreviewForksWarning:  "A pull request from a fork runs code you have not reviewed. It gets no home folder and no token, but it can still use the network. Turn this on only if you trust everyone who can open pull requests.",
	MsgTemplateNeedsRepo:    "This kind of app runs from a Git repository. Add the repository address.",
	MsgTemplateNoRepo:       "This kind of app runs from your home folder, not from a Git repository. Leave the repository empty.",
	MsgWebhookSecretOnce:    "Copy the secret now. It is shown once and cannot be shown again.",
	MsgPreviewManaged:       "This is the preview of a pull request. Change the app it belongs to, or close the pull request.",
	MsgNothingToRedeploy:    "This app does not run from a Git repository, so there is nothing to redeploy. Use Restart instead.",
	MsgWebhookSignature:     "The delivery is not signed with this app's webhook secret.",
	MsgWebhookTooLarge:      "The delivery is larger than 1 MiB.",
	MsgWebhookRateLimited:   "Too many deliveries from this address. Wait a minute, then try again.",

	MsgSecretNotFound:         "No secret with that name.",
	MsgSecretNameInvalid:      "Use a name of up to 100 letters, digits and underscores that does not start with a digit, such as OPENAI_API_KEY.",
	MsgSecretNameTaken:        "You already have a secret with this name. Pick another name, or change that secret's value.",
	MsgSecretValueInvalid:     "Paste a value of up to 16 KiB of text.",
	MsgSecretNoteTooLong:      "Keep the note under 200 characters.",
	MsgSecretInUse:            "In use by %s. Take the secret off them first.",
	MsgSecretsTooMany:         "You have 200 secrets, the most allowed. Delete one you no longer need.",
	MsgSecretsUnavailable:     "Secrets are not set up on this server yet. Ask an admin.",
	MsgSecretUnknown:          "You have no secret called %s.",
	MsgEnvNameInvalid:         "Use letters, digits and underscores, not starting with a digit, such as API_KEY.",
	MsgEnvNameReserved:        "%s is set by the platform. Pick another name.",
	MsgEnvNameRepeated:        "%s is used twice. Give each secret its own name.",
	MsgEnvTooMany:             "An app or a job can have at most 50 environment variables.",
	MsgSecretsNotOnPreviews:   "Previews of pull requests never get secrets.",
	MsgSecretsOwnerOnly:       "Only the owner can change its environment variables, with their own secrets.",
	MsgEnvValueInvalid:        "Use a value of up to 4 KiB of text.",
	MsgEnvValueOrSecret:       "Type a value or pick a secret, not both.",
	MsgSecretsNotForAppTokens: "An app's or job's own API token cannot use secrets. Use the browser, or a personal token from Settings.",
	MsgSecretsHeldOwnerOnly:   "This uses its owner's secrets, so only its owner can change what it runs, and not with an app's or job's own API token. Ask the owner, or have them remove its secrets first.",
	MsgAgentBrowserOnly:       "Only you can do this, in the browser or with a personal token; an app's or job's own token cannot.",
	MsgAgentBaseURLInvalid:    "Give the https address of an OpenAI-compatible API, such as https://openrouter.ai/api/v1.",
	MsgAgentModelInvalid:      "Name the model in up to 200 characters, such as anthropic/claude-sonnet-5.5.",
	MsgAgentKeyRequired:       "Paste the API key.",
	MsgAgentRequestInvalid:    "Say what to build in up to 4000 characters of text.",
	MsgAgentUnavailable:       "The coding agent is not available to you. Ask your admin to turn it on.",
	MsgAISettingsBaseURL:      "Give the https address of the provider's API, such as https://openrouter.ai/api/v1, or leave it empty for the provider's own.",
	MsgAISettingsBaseURLHost:  "That address is a link-local or cloud metadata address; the key is never sent there.",
	MsgAISettingsModel:        "Name the model in up to 200 characters, such as claude-opus-5-5.",
	MsgAISettingsKey:          "Paste the key as it is, at most 4096 characters, with no spaces or line breaks.",
	MsgAISettingsKeyOrRemove:  "Paste a new key or remove the stored one, not both.",
	MsgAISettingsOff:          "AI is off in the platform config (ai.enabled), so there is nothing to set here.",
	MsgAgentPromptNotTaken:    "This kind of app takes no first prompt.",
	MsgAgentNeedsPerson:       "A coding agent cannot do this itself: the app or job holds a secret, an API token or a Nebi token, belongs to someone else, or the change would give it one. Ask the person to do it in the browser, or propose it as a plan (POST /api/plans) for them to approve.",
	MsgWorkloadTokenReadOnly:  "An app's or job's own API token can read, start, stop and run, but cannot change files or create or change apps and jobs. Use the browser, or a personal token from Settings.",
	MsgRewriteOwnerOnly:       "Only its owner can change what it runs: its command, image, port, file, engine or template. It runs in their home folder, so a change could read or rewrite their files. Ask the owner.",
	MsgWebhookUnknown:         "This webhook was not made here. Set up the webhook again and paste the new address and secret into GitHub.",

	MsgEnvironmentsOff:        "Environments are not set up on this platform.",
	MsgEnvironmentUnknown:     "That environment was not found: it is not yours or shared with you, or it has no version yet.",
	MsgEnvironmentSignIn:      "Environments need a sign-in with your organization's account; this session has none.",
	MsgEnvironmentRefused:     "The environment manager did not accept your sign-in. Sign out and in again; if that does not help, ask your admin.",
	MsgEnvironmentUnreachable: "The environment manager could not be reached. Try again in a moment.",
	MsgEnvironmentTooLarge:    "That environment cannot run here: %s.",

	MsgLimitApps:          "You have %d apps, the most allowed. Delete one you no longer need to make another.",
	MsgLimitRunningApps:   "You have %d apps running, the most allowed at once. Stop one to start another.",
	MsgLimitPreviews:      "Your apps have %d previews, the most allowed. Close a pull request or turn previews off on an app to free one.",
	MsgLimitJobs:          "You have %d jobs, the most allowed. Delete one you no longer need to make another.",
	MsgLimitRunsPerJob:    "This job has %d runs going, the most allowed at once. Wait for one to finish, or cancel one.",
	MsgLimitRunsPerPerson: "You have %d job runs going, the most allowed at once. Wait for one to finish, or cancel one.",
	MsgLimitAPITokens:     "You have %d API tokens, the most allowed. Revoke one you no longer use.",
	MsgLimitPendingPlans:  "%d proposed changes are waiting for your approval, the most allowed. Approve or reject some first.",
}

// Msg returns the text for code, formatted with args. An unknown code
// returns the generic MsgInvalidRequest text, never the code itself.
func Msg(code MessageCode, args ...any) string {
	t, ok := messages[code]
	if !ok {
		return messages[MsgInvalidRequest]
	}
	if len(args) == 0 {
		return t
	}
	return fmt.Sprintf(t, args...)
}

// AllMessages returns a copy of every message, keyed by code.
func AllMessages() map[MessageCode]string { return maps.Clone(messages) }

// Minutes is a timeout in plain words: "1 minute", "90 minutes", "2 hours",
// "7 days". Whole days and whole hours from two up read as such.
func Minutes(m int32) string {
	unit, n := "minute", m
	switch {
	case m >= 1440 && m%1440 == 0:
		unit, n = "day", m/1440
	case m >= 120 && m%60 == 0:
		unit, n = "hour", m/60
	}
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
