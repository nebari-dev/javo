import type { CustomAllowance, Notice, Size, Step, StorageView, Template, TemplateCategory, Workspace } from '../api'

// Copy below mirrors internal/reasons/catalog.go: the API sends it, the UI only renders it.
export const REASONS = {
  NoCapacity: {
    title: 'No machine has room',
    what: 'No machine has enough free CPU or memory for this app, and the cluster cannot add another one right now.',
    todo: 'Keep it queued and it starts as soon as room frees up, or stop one of your other apps.',
    blame: 'The platform: the cluster is full. Not your app.',
    who: 'Cluster admins can add capacity.',
  },
  SizeTooBig: {
    title: 'Size too big for any machine',
    what: "This app's size needs more CPU, memory or GPUs than any machine in this cluster has, so it can never start here, however long it waits.",
    todo: 'Pick a smaller size with Change size. The app starts as soon as the size fits.',
    blame: "Your app's settings: its size is bigger than any machine here. Not a fault in your code.",
    who: 'You can pick a smaller size. A cluster admin can add bigger machines.',
  },
  HomeBusy: {
    title: 'Home folder is busy',
    what: 'Your home folder can only be attached to one machine at a time. It is attached to another machine that has no room for this app.',
    todo: 'Start this app smaller so it fits next to your other app, or stop your other app first.',
    blame: "A limit of this cluster's storage (per-user disk mode), not your app.",
    who: 'You can fix it now by stopping the other app or starting smaller. A cluster admin can remove the limit for everyone by enabling shared storage.',
  },
  HomeStarting: {
    title: 'Opening your home folder',
    what: 'Your home folder is being opened so its files can be listed. The first time takes a few seconds.',
    todo: 'Wait a moment. The list loads by itself.',
    blame: 'Nothing is wrong.',
    who: 'Nobody needed.',
  },
  ImagePullFailed: {
    title: 'Image cannot be downloaded',
    what: 'The image for this app could not be downloaded from the registry, for example because it does not exist or the registry is unreachable.',
    todo: 'Nothing you can fix here. Pick a different template, or tell the template owner.',
    blame: 'The template: its image cannot be downloaded. Not your app.',
    who: 'The template owner can fix it. You cannot from here.',
  },
  ImageRunsAsRoot: {
    title: 'Image runs as root',
    what: 'This image is set to run as the root user, or names its user in a way that cannot be checked. Apps here never run as root, so it was not started.',
    todo: 'Pick an image that runs as a non-root user (many have an unprivileged or rootless variant), or build one with a numeric user line such as USER 1000, then change the image.',
    blame: 'Your choice of image: it is set to run as root.',
    who: 'You.',
  },
  AppCrashed: {
    title: 'App crashed on start',
    what: 'The app started and then stopped with an error. It was restarted several times and failed the same way, so we stopped retrying.',
    todo: 'Open the evidence to see the app\'s error, fix it in your code or environment, then press Restart.',
    blame: 'Your app code or environment.',
    who: 'You.',
  },
  RefNotFound: {
    title: 'Branch, tag or commit not found',
    what: 'The repository was reached, but it has no branch, tag or commit with that name.',
    todo: "Check the name in the app's source settings, or leave it empty to use the default branch, then redeploy.",
    blame: 'Your source settings: that name does not exist in the repository.',
    who: 'You.',
  },
  IdpUnreachable: {
    title: 'Cannot reach the sign-in service',
    what: 'The service that signs people in did not answer, so nobody can sign in with it right now. Anyone already signed in stays signed in for a while.',
    todo: 'Try again in a minute. If it keeps failing, tell a cluster admin. If local accounts are enabled, you can sign in with one of those meanwhile.',
    blame: 'The platform or the sign-in service: it is down or cannot be reached. Not you.',
    who: 'Cluster admins.',
  },
  SignInLinkExpired: {
    title: 'That sign-in link expired',
    what: 'The sign-in was started in another tab or too long ago, so it cannot be finished.',
    todo: 'Press Start again and sign in once more.',
    blame: 'Nothing is wrong. Sign-in links only work for a few minutes, in the browser that started them.',
    who: 'Nobody needed.',
  },
  HomeMigrating: {
    title: 'Your home folder is being moved',
    what: 'Your files are being copied to new storage. Apps stay stopped until the copy is checked and switched over, so nothing is lost or half moved.',
    todo: 'Wait. Your apps start by themselves when the move is done.',
    blame: 'The platform: an admin scheduled a storage change. Not your app.',
    who: 'Cluster admins can say when it ends.',
  },
  Starting: {
    title: 'Starting',
    what: 'The app is starting. This usually takes a minute or two.',
    todo: 'Nothing to do. You can leave this page; it keeps starting.',
    blame: 'Nothing is wrong.',
    who: 'Nobody needed.',
  },
  Ready: { title: 'Ready', what: 'The app is running and answering.', todo: 'Open it.', blame: 'Nothing is wrong.', who: 'Nobody needed.' },
  Stopped: {
    title: 'Stopped',
    what: 'The app is stopped. Your files are kept.',
    todo: 'Start it again when you need it.',
    blame: 'Nothing is wrong.',
    who: 'Nobody needed.',
  },
  StoppedByAdmin: {
    title: 'Stopped by an admin',
    what: 'A platform admin stopped the app, for example to free the cluster for others. Your files and settings are kept.',
    todo: 'Start it again when you need it, or ask your admin why it was stopped.',
    blame: 'Nothing is wrong with the app.',
    who: 'Your platform admin.',
  },
  RestoreHeld: {
    title: 'Restored, waiting for an admin',
    what: 'The app came back from a backup. It waits until an admin starts the restored apps, so they do not all start at once and your files are back first.',
    todo: 'Nothing to do. It starts when your admin releases the restored apps.',
    blame: 'Nothing is wrong.',
    who: 'Your platform admin.',
  },
  BuildFailed: {
    title: 'Build failed',
    what: 'The build command ended with an error, so there is nothing to serve.',
    todo: 'Read the build output below, fix the build in your repository, then redeploy.',
    blame: "Your repository's build.",
    who: 'You.',
  },
  StoppedIdle: {
    title: 'Stopped when idle',
    what: 'Nobody used the app for a while, so it was stopped to free its CPU and memory. Your files and settings are kept.',
    todo: 'Start it again when you need it.',
    blame: 'Nothing is wrong.',
    who: 'Nobody needed.',
  },
}

type ReasonCode = keyof typeof REASONS

const TYPES = ['Scheduled', 'HomeReady', 'ImageReady', 'Started', 'Ready']

function step(type: string, status: Step['status'], reason: ReasonCode | '', evidence: string[] = []): Step {
  const r = reason ? REASONS[reason] : { title: '', what: '', todo: '', blame: '', who: '' }
  return { type, status, reason, ...r, evidence }
}

/** Steps the way the API renders them: before `at` True, at `at` False with reason, after Unknown. */
export function stepsAt(at: number, reason: ReasonCode, evidence: string[] = [], types = TYPES): Step[] {
  return types.map((t, i) =>
    i < at ? step(t, 'True', 'Ready') : i === at ? step(t, 'False', reason, evidence) : step(t, 'Unknown', 'Starting'),
  )
}

/** The steps of an app that builds the site it serves: Built comes before Started. */
export const SITE_TYPES = ['Scheduled', 'HomeReady', 'ImageReady', 'Built', 'Started', 'Ready']

/** The error lines the API keeps of a failed npm build, as a failed site app's evidence. */
export const NPM_ERRORS = [
  'container build terminated: Error, exit code 1',
  'npm error code EUSAGE',
  'npm error The `npm ci` command can only install with an existing package-lock.json or',
  'npm error npm-shrinkwrap.json with lockfileVersion >= 1. Run an install with npm@5 or',
  'npm error later to generate a package-lock.json file, then try again.',
]

export const allReady = (): Step[] => TYPES.map((t) => step(t, 'True', 'Ready'))

/** Gives step i the transition time start + seconds[i] (ISO), as the API sends since. */
export function withSince(steps: Step[], start: string, seconds: number[]): Step[] {
  const t0 = Date.parse(start)
  return steps.map((s, i) => (seconds[i] === undefined ? s : { ...s, since: new Date(t0 + seconds[i] * 1000).toISOString() }))
}
export const allStopped = (): Step[] => TYPES.map((t) => step(t, 'False', 'Stopped'))
/** Steps of an app the idle check stopped after `minutes` in words, as the API renders them. */
export const stoppedIdle = (words: string): Step[] =>
  TYPES.map((t) => step(t, 'False', 'StoppedIdle', [`Stopped after ${words} without use`]))
/** Steps of an app a platform admin stopped. */
export const stoppedByAdmin = (): Step[] => TYPES.map((t) => step(t, 'False', 'StoppedByAdmin'))
/** Steps of a restored app waiting for an admin to release it. */
export const heldSteps = (): Step[] => TYPES.map((t) => step(t, 'False', 'RestoreHeld'))
/** The admin's idle stop policy as GET /api/idle-stop answers a member. */
export const idlePolicy = { defaultMinutes: 60, minMinutes: 10, maxMinutes: 10080, canTurnOff: true }

export function ws(name: string, displayName: string, phase: string, steps: Step[], over: Partial<Workspace> = {}): Workspace {
  return {
    name,
    displayName,
    template: 'streamlit',
    kind: 'streamlit',
    file: 'sales/app.py',
    size: 'small',
    phase,
    url: `/u/alice/${name}/`,
    steps,
    canShare: true,
    command: '',
    image: '',
    port: 0,
    apiAccess: 'None',
    idleStopMinutes: 0,
    stoppedIdleAfter: 0,
    redeployable: false,
    env: [],
    ...over,
  }
}

// As GET /api/templates lists them, from internal/model/template.go.
export const templateCategories: TemplateCategory[] = [
  { id: 'notebooks', title: 'Notebooks and IDEs' },
  { id: 'dashboards', title: 'Dashboards and visualization' },
  { id: 'web', title: 'Web apps and APIs' },
  { id: 'agents', title: 'Coding agents' },
  { id: 'jobs', title: 'Jobs' },
  { id: 'other', title: 'Other' },
]

export const templates: Template[] = [
  {
    name: 'jupyterlab',
    displayName: 'JupyterLab',
    description: 'Notebooks and files',
    category: 'notebooks',
    kind: 'jupyterlab',
    fileExtensions: [],
    basePath: 'Prefix',
    appLogin: 'Disabled',
    sample: { title: 'Notebook tour', file: 'samples/jupyterlab', command: '' },
    maxSharing: 'Private',
    apiAccessDefault: 'None',
    maxApiAccess: 'ReadWrite',
    userCommand: 'None',
    userImage: false,
    fromRepo: 'None',
    port: 8888,
  },
  {
    name: 'streamlit',
    displayName: 'Streamlit',
    description: 'Python data apps',
    category: 'dashboards',
    kind: 'streamlit',
    fileExtensions: ['.py'],
    basePath: 'Prefix',
    appLogin: 'Disabled',
    sample: { title: 'Sales explorer', file: 'samples/streamlit/app.py', command: '' },
    maxSharing: 'SignedIn',
    apiAccessDefault: 'None',
    maxApiAccess: 'ReadWrite',
    userCommand: 'None',
    userImage: false,
    fromRepo: 'Optional',
    port: 8501,
  },
]

/** Takes a typed command; its sample starts with one. */
export const customTemplate: Template = {
  name: 'custom',
  displayName: 'Custom command',
  description: 'Your own command',
  category: 'web',
  kind: 'custom',
  fileExtensions: [],
  basePath: 'Prefix',
  appLogin: 'Disabled',
  sample: { title: 'Server time', file: 'samples/custom/sample.py', command: 'python sample.py --port={{.Port}} --base-url={{.BaseURL}}' },
  maxSharing: 'Private',
  apiAccessDefault: 'None',
  maxApiAccess: 'ReadWrite',
  userCommand: 'Required',
  userImage: false,
  fromRepo: 'Optional',
  port: 8080,
}

/** Runs the image each app names. */
export const containerTemplate: Template = {
  name: 'container',
  displayName: 'Container image',
  description: 'Any web image',
  category: 'web',
  kind: 'container',
  fileExtensions: [],
  basePath: 'Strip',
  appLogin: 'Disabled',
  maxSharing: 'Private',
  apiAccessDefault: 'None',
  maxApiAccess: 'ReadWrite',
  userCommand: 'Optional',
  userImage: true,
  fromRepo: 'None',
  port: 8080,
}

/** Serves one HTML file from the home or a built Git repository. */
export const staticTemplate: Template = {
  name: 'static-site',
  displayName: 'Static site',
  description: 'Web page or site',
  category: 'web',
  kind: 'static',
  fileExtensions: ['.html'],
  basePath: 'Strip',
  appLogin: 'Disabled',
  maxSharing: 'Public',
  apiAccessDefault: 'None',
  maxApiAccess: 'None',
  userCommand: 'None',
  userImage: false,
  fromRepo: 'Optional',
  build: { command: 'npm ci && npm run build', output: 'dist', ifFile: 'package.json' },
  port: 8080,
}

/** Runs a Node server from the home folder or a Git repository. */
export const nodeTemplate: Template = {
  name: 'backend-node',
  displayName: 'Node back-end',
  description: 'Node.js server',
  category: 'web',
  kind: 'custom',
  fileExtensions: [],
  basePath: 'Strip',
  appLogin: 'Disabled',
  maxSharing: 'Private',
  apiAccessDefault: 'None',
  maxApiAccess: 'None',
  userCommand: 'Required',
  userImage: false,
  fromRepo: 'Optional',
  port: 8080,
}

/** Runs a Go server from the home folder or a Git repository. */
export const goTemplate: Template = {
  ...nodeTemplate,
  name: 'backend-go',
  displayName: 'Go back-end',
  description: 'Go server',
}

const GiB = 2 ** 30

export const sizes: Size[] = [
  { name: 'small', displayName: 'Small', cpu: 1, memory: 2 * GiB, default: true, fits: true },
  { name: 'medium', displayName: 'Medium', cpu: 4, memory: 8 * GiB, default: false, fits: true },
  { name: 'large', displayName: 'Large', cpu: 8, memory: 32 * GiB, default: false, fits: true },
]

// Words from internal/reasons/messages.go (SizeTooBig) for one 8 CPU, 15.6 GB machine.
export const TOO_BIG = 'Bigger than any machine here: the largest has 8 CPU and 15.6 GB.'

// A custom amount allowance capped by one 8 CPU, 15.6 GB machine; words from
// internal/reasons/messages.go.
export const allowance: CustomAllowance = {
  allowed: true,
  maxCPU: 8,
  maxMemory: 16365140 * 1024,
  minCPU: 0.1,
  minMemory: 256 * 2 ** 20,
  cpuStep: 0.1,
  memoryStep: 256 * 2 ** 20,
  limit: 'You can ask for up to 8 CPU and 15.6 GB.',
  minimum: 'Ask for at least 0.1 CPU and 256 MB.',
}

/** The sizes on that machine, configured with Large as the default: the API moves the default to Medium. */
export const singleNodeSizes: Size[] = [
  { ...sizes[0], default: false },
  { ...sizes[1], default: true },
  { ...sizes[2], fits: false, reason: TOO_BIG, reasonShort: 'Too big' },
]

export const me = { id: 'u-1', username: 'alice', displayName: 'Alice', groups: ['data-science'], roles: ['member'] }
/** A platform admin, as GET /api/me answers. */
export const adminMe = { id: 'bob-bbbbbbbb', username: 'bob', displayName: 'Bob', groups: ['javo-admins'], roles: ['platform-admin', 'member'] }

// Words from internal/reasons/messages.go, as the API sends them.
export const MESSAGES = {
  ShareTooOpen: 'This kind of app can be shared with no one but you at most.',
  ShareTooOpenSignedIn: 'This kind of app can be shared with everyone who signs in at most.',
  InvalidRequest: 'Something in this form is not valid. Check it and try again.',
  ShareTargetUnknown: (name: string) => `Nobody is called "${name}". Check the spelling.`,
}

export const bob = { id: 'bob-bbbbbbbb', displayName: 'Bob', email: 'bob@example.com', active: true }

export const jobTemplate: Template = {
  name: 'python-jobs',
  displayName: 'Python jobs',
  description: 'Notebooks and scripts on Python',
  category: 'jobs',
  kind: 'python',
  fileExtensions: [],
  basePath: 'Prefix',
  appLogin: 'Disabled',
  jobOnly: true,
  maxSharing: 'Private',
  apiAccessDefault: 'None',
  maxApiAccess: 'ReadWrite',
  userCommand: 'None',
  userImage: false,
  fromRepo: 'None',
  port: 8080,
  jobs: {
    engines: ['papermill', 'command'],
    kernels: ['python3', 'python3-gpu'],
    maxTimeout: '6h0m0s',
    sample: { title: 'Print the time', command: 'bash /opt/javo/samples/jobs/print-time.sh', parameters: [{ name: 'greeting', type: 'string', default: 'hello' }] },
  },
}

/** The Custom command job template: typed scripts run through bash. */
export const customJobTemplate: Template = {
  ...jobTemplate,
  name: 'jobs-command',
  displayName: 'Custom command',
  description: 'Your own command',
  kind: 'custom',
  jobs: { engines: ['bash'], kernels: [] },
}

export const engines = [
  { id: 'papermill', displayName: 'Notebook (papermill)', extensions: ['.ipynb'] },
  { id: 'command', displayName: 'Command', extensions: [] },
  { id: 'bash', displayName: 'Bash script', extensions: [] },
]

export const failureProblem = {
  reason: 'NotebookFailed',
  title: 'A notebook cell raised an error',
  what: 'The notebook stopped at a cell that raised an error.',
  todo: 'Fix the cell and run the job again.',
  blame: 'Your notebook code.',
  who: 'You.',
  evidence: ["Cell 14 raised KeyError: 'region'"],
}

const failedRun = {
  id: 'run-3',
  trigger: 'schedule',
  outcome: 'Failed',
  reason: 'NotebookFailed',
  title: failureProblem.title,
  message: "Cell 14 raised KeyError: 'region'",
  startTime: '2026-10-26T05:00:00Z',
  finishTime: '2026-10-26T05:03:00Z',
  scheduledTime: '2026-10-26T05:00:00Z',
  // As the gateway sends them: outputPath is the run folder, outputs are its files with the HTML first.
  outputPath: 'jobs/weekly-report/run-3',
  outputs: ['jobs/weekly-report/run-3/output.html', 'jobs/weekly-report/run-3/output.ipynb'],
  // Started with a one-off value, not the job's saved default (emea).
  parameters: { region: 'apac' },
}

const okRun = {
  id: 'run-2',
  trigger: 'schedule',
  outcome: 'Succeeded',
  startTime: '2026-10-19T05:00:00Z',
  finishTime: '2026-10-19T05:02:00Z',
  scheduledTime: '2026-10-19T05:00:00Z',
  outputPath: 'jobs/weekly-report/run-2',
  outputs: ['jobs/weekly-report/run-2/output.html', 'jobs/weekly-report/run-2/output.ipynb'],
}

/** A scheduled job whose last run failed; the strip holds 10 successes and 2 failures. */
export const weeklyView = {
  name: 'weekly-report',
  displayName: 'Weekly report',
  engine: 'papermill',
  source: 'reports/weekly.ipynb',
  template: 'python-jobs',
  phase: 'Ready',
  recent: [...Array(9).fill('Succeeded'), 'Failed', 'Succeeded', 'Failed'],
  schedule: { cron: '0 6 * * 1', timeZone: 'Europe/Berlin', concurrency: 'Forbid', paused: false },
  nextRuns: ['2026-11-02T05:00:00Z', '2026-11-09T05:00:00Z'],
  lastRun: failedRun,
  problem: failureProblem,
}

/** A command job: no file, a command line. */
export const printTimeDetail = {
  ...weeklyView,
  name: 'print-the-time',
  displayName: 'Print the time',
  engine: 'command',
  source: '',
  command: 'bash /opt/javo/samples/jobs/print-time.sh',
  schedule: undefined,
  nextRuns: [],
  recent: [],
  lastRun: undefined,
  problem: undefined,
  notify: { onFailure: true, onSuccess: false },
  parameters: [{ name: 'greeting', type: 'string', default: 'hello' }],
  run: { retries: 0 },
  runs: [],
}

export const weeklyDetail = {
  ...weeklyView,
  kernel: 'python3',
  notify: { onFailure: true, onSuccess: false },
  parameters: [{ name: 'region', type: 'string', default: 'emea' }],
  run: { retries: 0, timeout: '1h0m0s' },
  size: 'small',
  runs: [failedRun, okRun],
}

/** A coding agent: its apps call the API as their owner unless told otherwise. */
export const agentTemplate: Template = {
  name: 'claude-code',
  displayName: 'Claude Code',
  description: 'A coding agent in a terminal.',
  category: 'agents',
  kind: 'claude-code',
  fileExtensions: [],
  basePath: 'Prefix',
  appLogin: 'Enabled',
  maxSharing: 'Private',
  apiAccessDefault: 'ReadWrite',
  maxApiAccess: 'ReadWrite',
  userCommand: 'None',
  userImage: false,
  fromRepo: 'None',
  port: 7681,
}

// Words from internal/reasons/notices.go, as the API sends them.
export const NOTICES = {
  MigrationScheduled: {
    code: 'MigrationScheduled',
    tone: 'warn',
    title: 'Your files are moving',
    text: 'Your files move to shared storage on 5 Nov 2026, 02:00. Your apps will be stopped for about 30 minutes.',
    action: 'Save your work before then.',
  },
  ScratchPaths: {
    code: 'ScratchPaths',
    tone: 'info',
    title: 'Some folders are not saved',
    text: 'Caches and environments are rebuilt each time an app starts and are not saved.',
    action: 'Keep anything you need in your home folder.',
  },
} satisfies Record<string, Notice>

export function storageView(over: Partial<StorageView> = {}): StorageView {
  return {
    mode: 'perUser',
    modeLabel: 'Files on one machine',
    notices: [],
    folders: [],
    scratchPaths: ['~/.cache', '/scratch'],
    size: 0,
    sizeEnforced: false,
    ...over,
  }
}

/** An app from a public GitHub repository: running commit 0123456, previews on offer. */
export function repoApp(name: string, over: Partial<Workspace> = {}): Workspace {
  return ws(name, 'Site', 'Running', allReady(), {
    template: 'static-site',
    kind: 'static',
    file: '',
    source: {
      url: 'https://github.com/example-org/site',
      ref: 'main',
      path: '',
      commit: '0123456789abcdef0123456789abcdef01234567',
      hasToken: false, tokenSecret: '',
      webURL: 'https://github.com/example-org/site/commit/0123456789abcdef0123456789abcdef01234567',
      previewsAvailable: true,
      webhookConfigured: false,
    },
    redeployable: true,
    ...over,
  })
}

/** The preview of pull request n of the app called parent. */
export function previewApp(parent: string, n: number, phase = 'Running'): Workspace {
  return repoApp(`${parent}--pr-${n}`, {
    displayName: `Site PR #${n}`,
    phase,
    url: `/u/alice/${parent}--pr-${n}/`,
    previewOf: { name: parent, pullNumber: n },
    source: { url: 'https://github.com/example-org/site', ref: 'abc1234', path: '', commit: 'abc1234def', hasToken: false, tokenSecret: '', webURL: '', previewsAvailable: false, webhookConfigured: false },
  })
}

export const LIMIT_TEXT = 'This app already has the most previews it may have, so this pull request has none.'

// AI, as GET /api/ai/status answers it.
export const aiOff = { enabled: false, features: { explain: false, draft: false, palette: false, admin: false }, message: 'AI features are off on this installation.' }
export const aiOn = { enabled: true, features: { explain: true, draft: true, palette: true, admin: true }, model: 'claude-opus-5-5' }

// Words from internal/reasons, as the API sends them.
export const AI_TEXT = {
  MissingModule: {
    title: 'A Python package is missing',
    what: 'The app stopped because it imports a Python package that is not installed in its environment.',
    todo: 'Install the package named in the evidence in your environment, or remove the import, then restart.',
    blame: 'Your app code or environment.',
    who: 'You.',
  },
  busy: 'The AI service is busy. Trying again in 20 seconds.',
  unavailable: 'The AI service cannot be reached right now. The cause and fix above do not depend on it.',
  withheld: '2 log lines were not sent: no secret scanner is set up to check them.',
  planDone: 'This change was already applied.',
  planExpired: 'This change expired before it was approved. Ask for it again.',
  planNotFound: 'No change with that id. It may have expired.',
}

/** The rules' diagnosis of a crashed app: a missing module, with a restart proposal. */
export function diagnosis(over: Record<string, unknown> = {}) {
  return {
    rule: 'missing-module',
    code: 'MissingModule',
    known: true,
    ...AI_TEXT.MissingModule,
    evidence: [
      { kind: 'log', ref: 'log/app/prev/41', excerpt: "ModuleNotFoundError: No module named 'plotly'" },
      { kind: 'condition', ref: 'condition/Started', excerpt: 'container app terminated: Error, exit code 1' },
    ],
    bundle: [
      { kind: 'condition', ref: 'condition/Started', excerpt: 'container app terminated: Error, exit code 1' },
      { kind: 'log', ref: 'log/app/prev/41', excerpt: "ModuleNotFoundError: No module named 'plotly'" },
    ],
    proposals: [{ action: 'restart', target: 'sales-app', summary: 'Restart Sales app' }],
    ...over,
  }
}

/** A plan as POST /api/plans answers it. */
export function plan(over: Record<string, unknown> = {}) {
  return {
    id: 'p1',
    source: 'rules',
    state: 'pending',
    createdAt: '2026-11-01T12:00:00Z',
    expiresAt: '2026-11-01T12:10:00Z',
    planned: { action: 'restart', target: 'sales-app', summary: 'Restart Sales app', destructive: false, changes: [{ field: 'state', from: 'Failed', to: 'Restarted' }] },
    ...over,
  }
}
