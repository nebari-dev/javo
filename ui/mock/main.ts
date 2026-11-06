// Dev-only: runs the real UI against canned API answers so pages can be
// looked at without a cluster (pnpm mock). Not part of the production build,
// which starts from ui/index.html.
import {
  allReady,
  allStopped,
  containerTemplate,
  LIMIT_TEXT,
  goTemplate,
  nodeTemplate,
  NPM_ERRORS,
  SITE_TYPES,
  previewApp,
  repoApp,
  staticTemplate,
  customTemplate,
  customJobTemplate,
  engines,
  jobTemplate,
  me as member,
  NOTICES,
  storageView,
  printTimeDetail,
  REASONS,
  AI_TEXT,
  aiOff,
  aiOn,
  diagnosis,
  plan,
  allowance,
  singleNodeSizes,
  idlePolicy,
  stepsAt,
  stoppedIdle,
  stoppedByAdmin,
  heldSteps,
  templateCategories,
  templates,
  weeklyDetail,
  weeklyView,
  ws,
} from '../src/test/fixtures'

// Every template kind, so each logo can be looked at.
const allTemplates = [
  ...templates,
  customJobTemplate,
  jobTemplate,
  ...([
    ['panel', 'Panel', 'Python dashboards', '.py'],
    ['bokeh', 'Bokeh', 'Interactive plots', '.py'],
    ['dash', 'Dash', 'Plotly dashboards', '.py'],
    ['gradio', 'Gradio', 'Python ML demos', '.py'],
    ['voila', 'Voila', 'Notebooks as apps', '.ipynb'],
    ['marimo', 'marimo', 'Reactive notebooks', '.py'],
    ['fastapi', 'FastAPI', 'Python web APIs', '.py'],
    ['vscode', 'VS Code', 'Code in the browser', ''],
    ['rstudio', 'RStudio', 'R in the browser', ''],
    ['claude-code', 'Claude Code', 'Claude coding agent', ''],
    ['pi', 'pi coding agent', 'pi coding agent', ''],
    ['herdr', 'herdr', 'Agents in a terminal', ''],
    ['terminal', 'Terminal', 'Shell in your files', ''],
  ] as const).map(([kind, displayName, description, ext]) => ({
    name: kind,
    displayName,
    description,
    category: ['panel', 'bokeh', 'dash', 'gradio', 'voila'].includes(kind) ? 'dashboards' : ['marimo', 'vscode', 'rstudio', 'terminal'].includes(kind) ? 'notebooks' : kind === 'fastapi' ? 'web' : 'agents',
    kind,
    fileExtensions: ext ? [ext] : [],
    sample: { title: `${displayName} sample`, file: ext ? `samples/${kind}/app${ext}` : `samples/${kind}`, command: '' },
    userCommand: 'None',
    userImage: false,
    fromRepo: ['panel', 'bokeh', 'dash', 'gradio', 'voila', 'marimo', 'fastapi'].includes(kind) ? 'Optional' : 'None',
    port: 8080,
    // Coding agents call the API as their owner unless told otherwise.
    apiAccessDefault: ['claude-code', 'pi', 'herdr'].includes(kind) ? 'ReadWrite' : 'None',
    maxApiAccess: 'ReadWrite',
  })),
  customTemplate,
  containerTemplate,
  staticTemplate,
  nodeTemplate,
  goTemplate,
]

// Preview apps of 'site': hidden from the list unless asked for.
const previewApps = [previewApp('site', 12), previewApp('site', 14, 'Starting'), previewApp('site', 15, 'Failed')]

// The last look at the pull requests of 'site', and its settings.
const previewSettings: Record<string, { enabled: boolean; max: number; forks: boolean }> = {
  site: { enabled: true, max: 3, forks: false },
  'private-docs': { enabled: false, max: 3, forks: false },
  'broken-docs': { enabled: true, max: 3, forks: false },
  'team-blog': { enabled: false, max: 3, forks: false },
}
const FORKS_WARNING =
  'A pull request from a fork runs code you have not reviewed. It gets no home folder and no token, but it can still use the network. Turn this on only if you trust everyone who can open pull requests.'
const previewItems: Record<string, unknown[]> = {
  site: [
    { number: 15, title: 'Add dark mode to the docs', app: 'site--pr-15' },
    { number: 14, title: 'Fix the footer links', app: 'site--pr-14' },
    { number: 12, title: 'Document the new export option', app: 'site--pr-12' },
    { number: 9, title: 'Try a different sidebar', reason: 'PreviewLimitReached', text: LIMIT_TEXT },
    { number: 7, title: 'Rework search from a fork', reason: 'PreviewForkSkipped', text: 'This pull request comes from a fork, and previews of forks are off because they run code you have not reviewed.' },
  ],
}
const previewsView = (name: string) => ({
  ...previewSettings[name],
  maxAllowed: 5,
  forksWarning: FORKS_WARNING,
  syncedAt: new Date(Date.now() - 3 * 60_000).toISOString(),
  ...(name === 'broken-docs'
    ? {
        reason: 'PreviewsUnavailable',
        title: 'Cannot read pull requests',
        text: 'GitHub did not answer the request for the repository\'s pull requests, so previews are not being updated.',
        todo: 'Wait a few minutes. If it keeps failing, check the repository address and tell a cluster admin.',
      }
    : {}),
  items: previewItems[name] ?? [],
})

// What the controller records for each done step of a running app; its
// events expired long ago, so the recorded lines are all that is left.
const recorded: Record<string, string[]> = {
  Scheduled: ['node node-1', 'placed at 2026-10-31 10:07:12 UTC'],
  HomeReady: ['claim home-alice-1a2b, 10Gi, storage class standard', 'mounted within 2s of placement'],
  ImageReady: ['image javo-python-apps:dev', 'digest sha256:9c4e0d7a1f2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5', 'already on the node, not downloaded'],
  Started: ['container app started at 2026-10-31 10:07:15 UTC', 'restarts 0'],
  Ready: ['checked TCP :8080', 'answered 4s after the container started'],
}
const recordedSteps = () => allReady().map((s) => ({ ...s, evidence: recorded[s.type] }))
// ?scenario=noshared shows the per-user disk banners; ?admin=1 signs in a platform admin.
const params = new URLSearchParams(window.location.search)
const noshared = params.get('scenario') === 'noshared'
const me = params.get('admin') === '1' ? { ...member, roles: ['platform-admin'] } : member

const quiet = params.get('scenario') === 'quiet'
const storage = storageView({
  ...(quiet
    ? { notices: [NOTICES.ScratchPaths] as never }
    : noshared
    ? { notices: [NOTICES.MigrationScheduled, NOTICES.ScratchPaths] as never }
    : { mode: 'shared', modeLabel: 'Files on shared storage', notices: [NOTICES.ScratchPaths] as never }),
  folders: [
    { dir: 'data-science', group: 'data-science', ready: true },
    { dir: 'ml', group: 'ml', ready: false, reason: { code: 'SharedFoldersUnavailable', title: 'Group folders are not available', what: '', todo: '' } },
  ],
})

const notice = (code: string, tone: string, title: string, text: string, action: string) => ({ code, tone, title, text, action })

// The admin storage report, in the catalog's words. ?scenario=quiet is one
// machine with its own disk: nothing needs attention.
const row = (fact: string, attention: boolean, code: string, tone: string, title: string, text: string, action: string, detail: string, evidence: string[] = []) => ({
  fact,
  provenance: 'known',
  attention,
  evidence,
  notice: { code, tone, title, text, action, detail },
})
const facts = [
  row('home', false, 'AdminHomeShared', 'info', 'Files are on shared storage', 'Every machine can reach every home folder, so apps can run anywhere.', 'Nothing to do.', 'StorageClass filestore, one ReadWriteMany drive with a folder per person (storage.home.mode shared).'),
  row('snapshots', false, 'AdminNoSnapshots', 'info', 'Backups copy files one by one', 'This storage cannot take instant copies, so backups of home folders are slower.', 'To change this, add snapshot support to the cluster.', 'No VolumeSnapshotClass was found.'),
]
// The admin's AI settings (Settings > AI), kept while the page is open.
const aiSettingsFromConfig: Record<string, unknown> & { keyStored: boolean; keyLast4: string } = {
  enabled: true,
  overridden: false,
  provider: 'anthropic',
  baseURL: '',
  model: 'claude-opus-5-5',
  keyStored: false,
  keyLast4: '',
  keyFrom: 'config',
  config: { provider: 'anthropic', baseURL: '', model: 'claude-opus-5-5', key: true },
}
let aiSettings = { ...aiSettingsFromConfig }

const adminStorage = quiet
  ? { summary: "Files are stored on this machine's disk. Nothing needs attention.", rows: facts }
  : {
      summary: 'Files are on shared storage that every machine can use.',
      rows: [
        row('probe', true, 'AdminProbeFailed', 'err', 'The shared storage test failed', 'This storage is not used for home folders until the test passes.', 'Fix the cause on the cluster, then run the test again.',
          'StorageClass filestore failed at the mount step. Run it again with javo storage verify filestore.', ['step mount failed on node-2: no NFS client']),
        row('mismatch', true, 'AdminModeMismatch', 'warn', 'Home folders are waiting to move', 'The storage setting changed to shared storage, but 2 home folders are still on the old storage.', 'Run javo storage migrate to move them.',
          'javo storage migrate plan shows the move and javo storage migrate schedule --at <time> starts it. Each owner\'s apps stop while their files copy.'),
        ...facts,
      ],
    }


const apps = [
  // Coding-agent apps built from a prompt: a short one and one long enough to fold.
  ws('stock-explorer', 'Nvidia stock explorer', 'Running', allReady(), {
    kind: 'claude-code', template: 'claude-code', file: '', apiAccess: 'ReadWrite',
    agentPrompt: 'Create a Streamlit app that lets me look at Nvidia stock prices over time, with a date range picker and a moving average.',
  }),
  ws('churn-notebook', 'Churn analysis notebook', 'Stopped', allStopped(), {
    kind: 'pi', template: 'pi', file: '', apiAccess: 'ReadWrite',
    agentPrompt: [
      'Build a Jupyter notebook in ~/projects/churn that analyses customer churn from data/customers.csv.',
      'Start with a short data quality check: missing values, duplicate customer ids and odd dates.',
      'Then plot churn rate by plan, by signup month and by region, and fit a simple logistic regression with plan, tenure and monthly spend as features.',
      'Explain each step in a markdown cell written for a non-technical reader, and finish with three concrete recommendations for the retention team.',
      'Keep the notebook runnable top to bottom and pin the packages you use in a pixi.toml next to it.',
    ].join('\n'),
  }),
  ws('plots', 'Plot explorer', 'Running', recordedSteps(), {
    kind: 'bokeh', template: 'bokeh', file: 'plots/app.py', filePath: '/home/user/plots/app.py', idleStopMinutes: 60,
    run: { image: 'ghcr.io/example/pixi:1', command: [], line: '/opt/javo/pod-user.sh bokeh serve /home/user/plots/app.py --port 8080', environment: '' },
    env: [{ name: 'LOG_LEVEL', value: 'info' }, { name: 'OPENAI_API_KEY', secret: 'OPENAI_KEY' }, { name: 'PGPASSWORD', secret: 'WAREHOUSE_PASSWORD' }],
  }),
  ws('sales-app', 'Sales app', 'Starting', stepsAt(2, 'Starting')),
  ws('churn', 'Churn dashboard', 'Failed', stepsAt(3, 'AppCrashed', [
    'container app terminated: Error, exit code 1',
    "ModuleNotFoundError: No module named 'plotly'",
  ])),
  ws('research-lab', 'Research lab', 'Running', allReady(), { kind: 'jupyterlab', template: 'jupyterlab', file: '' }),
  ws('report', 'Weekly report', 'Starting', stepsAt(0, 'NoCapacity', ['0/3 nodes are available: 3 Insufficient cpu.'])),
  ws('model', 'Model trainer', 'Failed', stepsAt(2, 'ImagePullFailed', ['container app waiting: ErrImagePull: manifest unknown'])),
  ws('big-model', 'Big model', 'Failed', stepsAt(0, 'SizeTooBig', ['needs 8 CPU and 32 GB; the largest machine has 8 CPU and 15.6 GB']), { size: 'large' }),
  ws('tuned', 'Tuned model', 'Running', allReady(), { kind: 'jupyterlab', template: 'jupyterlab', file: '', size: '', resources: { cpu: 2.5, memory: 6 * 2 ** 30 } }),
  ws('archive', 'Archive explorer', 'Stopped', allStopped()),
  ws('notebook', 'Idle notebook', 'Stopped', stoppedIdle('60 minutes'), { kind: 'jupyterlab', template: 'jupyterlab', file: '', idleStopMinutes: 60, stoppedIdleAfter: 60 }),
  ws('gpu-run', 'GPU run', 'Stopped', stoppedByAdmin(), { kind: 'jupyterlab', template: 'jupyterlab', file: '' }),
  ws('restored', 'Restored dashboard', 'Pending', heldSteps(), { kind: 'panel', template: 'panel', file: 'dash/app.py' }),
  ws('orders-api', 'Orders API', 'Running', allReady(), { kind: 'custom', template: 'custom', file: '', command: './orders-api --port={{.Port}}', apiAccess: 'Read' }),
  ws('clock', 'Server clock', 'Running', allReady(), {
    template: 'custom',
    kind: 'custom',
    file: 'clock/app.py',
    command: 'python app.py --port={{.Port}} --base-url={{.BaseURL}}',
  }),
  ws('web', 'Team site', 'Running', allReady(), { template: 'container', kind: 'container', file: '', image: 'nginxinc/nginx-unprivileged:1.27-alpine', port: 8080 }),
  // Apps from Git: a public repository with previews on, a private one with previews off,
  // a clone that fails, a back-end from a non-GitHub host, and a static page from a home file.
  repoApp('site', { displayName: 'Docs site' }),
  repoApp('private-docs', {
    displayName: 'Private docs',
    source: { url: 'https://github.com/example-org/private-docs', ref: '', path: 'web', commit: 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678', hasToken: true, tokenSecret: 'GH_DOCS_TOKEN', webURL: 'https://github.com/example-org/private-docs/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678', previewsAvailable: true, webhookConfigured: true },
  }),
  repoApp('broken-docs', {
    displayName: 'Broken docs',
    phase: 'Failed',
    steps: stepsAt(3, 'RefNotFound', ['fatal: Remote branch release not found in upstream origin']),
    source: { url: 'https://github.com/example-org/broken-docs', ref: 'release', path: '', commit: '', hasToken: false, tokenSecret: '', webURL: '', previewsAvailable: true, webhookConfigured: false },
  }),
  ws('orders-node', 'Orders back-end', 'Running', allReady(), {
    template: 'backend-node',
    kind: 'custom',
    file: '',
    command: 'node server.js',
    redeployable: true,
    source: { url: 'https://git.example.org/example-org/orders', ref: 'main', path: '', commit: '9f8e7d6c5b4a39281706f5e4d3c2b1a098765432', hasToken: true, tokenSecret: '', webURL: '', previewsAvailable: false, webhookConfigured: false },
  }),
  ws('page', 'Landing page', 'Running', allReady(), { template: 'static-site', kind: 'static', file: 'pages/index.html', redeployable: true }),
  // A static site whose npm build failed: the Build step carries the build's log.
  repoApp('team-blog', {
    displayName: 'Team blog',
    template: 'static-site',
    kind: 'static',
    file: '',
    phase: 'Failed',
    steps: stepsAt(3, 'BuildFailed', NPM_ERRORS, SITE_TYPES),
    source: { url: 'https://github.com/example-org/team-blog', ref: '', path: '', commit: '', hasToken: false, tokenSecret: '', webURL: '', previewsAvailable: true, webhookConfigured: false },
  }),
  ws('root-web', 'Root image', 'Failed', stepsAt(3, 'ImageRunsAsRoot', [
    'container app waiting: CreateContainerConfigError: container has runAsNonRoot and image will run as root (pod: "alice-root-web_javo-apps(5f1c)", container: app)',
  ]), { template: 'container', kind: 'container', file: '', image: 'traefik/whoami:v1.11', port: 80 }),
]

const folders: Record<string, { name: string; kind: 'file' | 'folder'; size: number; modified: string }[]> = {
  '': [
    { name: '.config', kind: 'folder', size: 0, modified: '2026-10-20T09:00:00Z' },
    { name: 'notebooks', kind: 'folder', size: 0, modified: '2026-10-27T09:00:00Z' },
    { name: 'sales', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' },
    { name: '.bashrc', kind: 'file', size: 220, modified: '2026-10-20T09:00:00Z' },
    { name: 'README.md', kind: 'file', size: 2048, modified: '2026-09-02T10:00:00Z' },
  ],
  notebooks: [],
  plots: [{ name: 'app.py', kind: 'file', size: 92, modified: '2026-10-30T11:02:00Z' }],
  'sales/pages': [],
  sales: [
    { name: 'pages', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' },
    { name: 'app.py', kind: 'file', size: 4096, modified: '2026-10-30T11:02:00Z' },
    { name: 'utils.py', kind: 'file', size: 2100, modified: '2026-10-29T10:00:00Z' },
    { name: 'pixi.toml', kind: 'file', size: 900, modified: '2026-10-25T10:00:00Z' },
    { name: 'sales.csv', kind: 'file', size: 18874368, modified: '2026-10-27T10:00:00Z' },
  ],
}

// ?samples=copied: an app has started before, so the samples are already in the home.
if (new URLSearchParams(window.location.search).get('samples') === 'copied') {
  const dir = (name: string) => ({ name, kind: 'folder' as const, size: 0, modified: '2026-10-28T09:00:00Z' })
  const file = (name: string, size: number) => ({ name, kind: 'file' as const, size, modified: '2026-10-28T09:00:00Z' })
  folders[''].splice(3, 0, dir('samples'))
  folders.samples = [dir('claude-code'), dir('jupyterlab'), dir('streamlit')]
  folders['samples/streamlit'] = [file('app.py', 3100), file('sales.csv', 52000), file('pixi.toml', 400)]
  folders['samples/claude-code'] = [dir('.claude'), file('README.md', 1800), file('hello.js', 240)]
}

// Canned Kubernetes events (oldest first) and app log lines per app.
const T0 = Date.parse('2026-10-30T12:00:00Z')
const at = (s: number) => new Date(T0 + s * 1000).toISOString()
const ev = (s: number, step: string, reason: string, message: string, type = 'Normal', count = 1) => ({ step, time: at(s), type, reason, message, count })
const startEvents = [
  ev(0, 'HomeReady', 'ProvisioningSucceeded', 'Successfully provisioned volume pvc-7f3a for claim javo-apps/home-alice'),
  ev(1, 'Scheduled', 'Scheduled', 'Successfully assigned javo-apps/alice-sales-app to node-1'),
  ev(2, 'HomeReady', 'SuccessfulAttachVolume', 'AttachVolume.Attach succeeded for volume "pvc-7f3a"'),
  ev(3, 'ImageReady', 'Pulling', 'Pulling image "javo-streamlit:dev"'),
  ev(30, 'ImageReady', 'Pulled', 'Successfully pulled image "javo-streamlit:dev" in 27.1s (27.1s including waiting). Image size: 412 MB.'),
  ev(31, 'Started', 'Created', 'Created container: app'),
  ev(31, 'Started', 'Started', 'Started container app'),
  ev(33, 'Ready', 'Unhealthy', 'Readiness probe failed: Get "http://10.42.0.17:8501/u/alice/sales-app/": dial tcp 10.42.0.17:8501: connect: connection refused', 'Warning', 3),
]
const events: Record<string, unknown[]> = {
  'sales-app': startEvents.slice(0, 5),
  'research-lab': startEvents,
  churn: [
    ...startEvents.slice(0, 7),
    ev(40, 'Started', 'BackOff', 'Back-off restarting failed container app in pod alice-churn', 'Warning', 6),
  ],
  report: [ev(1, 'Scheduled', 'FailedScheduling', '0/3 nodes are available: 3 Insufficient cpu. preemption: 0/3 nodes are available: 3 No preemption victims found for incoming pod.', 'Warning', 4)],
  model: [
    ...startEvents.slice(0, 4),
    ev(5, 'ImageReady', 'Failed', 'Failed to pull image "registry.example.org/model:1": manifest unknown', 'Warning', 2),
    ev(6, 'ImageReady', 'BackOff', 'Back-off pulling image "registry.example.org/model:1"', 'Warning', 5),
  ],
  'team-blog': [
    ...startEvents.slice(0, 4),
    ev(18, 'Built', 'Started', 'Started container build'),
    ev(26, 'Built', 'BackOff', 'Back-off restarting failed container build in pod alice-team-blog', 'Warning', 3),
  ],
}
// Real line shapes: Streamlit, Jupyter Server (tornado LogFormatter, coloured
// prefix when it thinks it has a terminal), a Python traceback, Go slog/zap
// lines and a Go panic.
const ESC = '\x1b'
const streamlitLines = [
  '',
  '  You can now view your Streamlit app in your browser.',
  '',
  '  URL: http://0.0.0.0:8501/u/alice/sales-app/',
  '',
  "2026-10-30 12:00:33.412 WARNING streamlit.runtime.scriptrunner_utils.script_run_context: Thread 'MainThread': missing ScriptRunContext! This warning can be ignored when running in bare mode.",
  '2026-10-30 12:00:34.020 Uncaught app execution',
  'Traceback (most recent call last):',
  '  File "/opt/pixi/envs/default/lib/python3.12/site-packages/streamlit/runtime/scriptrunner/exec_code.py", line 121, in exec_func_with_error_handling',
  '    result = func()',
  '  File "/home/alice/sales/app.py", line 18, in <module>',
  '    totals = df.groupby("region")["revenue"].sum()',
  "KeyError: 'region'",
  '<script>alert("log lines are text")</script>',
]
const jupyterLines = [
  `${ESC}[32m[I 2026-10-30 12:00:31.104 ServerApp]${ESC}(B${ESC}[m jupyter_lsp | extension was successfully loaded.`,
  '[I 2026-10-30 12:00:31.311 ServerApp] Serving notebooks from local directory: /home/alice',
  '[I 2026-10-30 12:00:31.311 ServerApp] Jupyter Server 2.14.2 is running at:',
  '[I 2026-10-30 12:00:31.311 ServerApp] http://research-lab:8888/u/alice/research-lab/lab',
  '[D 2026-10-30 12:00:31.402 ServerApp] Searching [\'/home/alice\', \'/opt/pixi/envs/default/etc/jupyter\'] for config files',
  '[W 2026-10-30 12:00:32.950 ServerApp] 404 GET /u/alice/research-lab/api/kernels/4f1c2a7e-0b8d-4c5e-9a61-2f3d7e8b9c10 (alice@10.42.0.1) 1.52ms referer=None',
  '[I 2026-10-30 12:00:33.120 ServerApp] 201 POST /u/alice/research-lab/api/sessions (alice@10.42.0.1) 48.31ms',
  '[I 2026-10-30 12:00:33.188 KernelManager] Kernel started: 4f1c2a7e-0b8d-4c5e-9a61-2f3d7e8b9c10',
  `${ESC}[31m[E 2026-10-30 12:00:40.004 ServerApp]${ESC}(B${ESC}[m 500 GET /u/alice/research-lab/api/contents/sales/data.parquet?content=1 (alice@10.42.0.1) 12.10ms referer=http://research-lab:8888/u/alice/research-lab/lab`,
  '    Traceback (most recent call last):',
  '      File "/opt/pixi/envs/default/lib/python3.12/site-packages/tornado/web.py", line 1790, in _execute',
  '        result = await result',
  '    UnicodeDecodeError: \'utf-8\' codec can\'t decode byte 0x8b in position 1: invalid start byte',
  '[I 2026-10-30 12:00:41.300 ServerApp] Saving file at /sales/explore.ipynb',
]
const goLines = [
  'time=2026-10-30T12:00:31.002Z level=INFO msg="listening" addr=:8080 version=1.4.2',
  '{"level":"info","ts":1730289632.118,"caller":"orders/handler.go:88","msg":"request","method":"GET","path":"/u/alice/orders-api/v1/orders","status":200,"duration":0.0042}',
  'time=2026-10-30T12:00:33.410Z level=WARN msg="slow query" table=orders duration=1.82s rows=48210',
  '{"level":"error","ts":1730289634.551,"caller":"orders/store.go:131","msg":"insert failed","error":"pq: duplicate key value violates unique constraint \\"orders_pkey\\"","status":500}',
  'time=2026-10-30T12:00:35.000Z level=DEBUG msg="cache refresh" entries=1204',
]
const goPanic = [
  'time=2026-10-30T11:58:02.513Z level=INFO msg="listening" addr=:8080 version=1.4.2',
  'panic: runtime error: invalid memory address or nil pointer dereference',
  '[signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x6f2b1c]',
  '',
  'goroutine 37 [running]:',
  'main.(*Store).Insert(0x0, {0x8a1c28, 0xc000134000}, 0xc0001b2000)',
  '\t/src/orders/store.go:131 +0x3c',
  'net/http.HandlerFunc.ServeHTTP(0xc0000a6040, {0x8a3d10, 0xc0001c8000}, 0xc0001ba000)',
  '\t/usr/local/go/src/net/http/server.go:2220 +0x29',
  'exit status 2',
]
// Bokeh's default "%(asctime)s %(message)s", as `bokeh serve` prints it.
const bokehLines = [
  '2026-10-31 10:07:18,494 Starting Bokeh server version 3.9.2 (running on Tornado 6.6)',
  '2026-10-31 10:07:18,497 User authentication hooks NOT provided (default user enabled)',
  '2026-10-31 10:07:18,500 Bokeh app running at: http://localhost:8080/u/alice/plots/app',
  '2026-10-31 10:07:18,500 Starting Bokeh server with process id: 1',
  '2026-10-31 10:07:25,123 200 GET /u/alice/plots/app (10.244.0.5) 120.45ms',
  '2026-10-31 10:07:25,456 WebSocket connection opened',
  '2026-10-31 10:07:25,457 ServerConnection created',
]
const logLines: Record<string, string[]> = {
  plots: bokehLines,
  'sales-app': streamlitLines.slice(0, 5),
  'research-lab': jupyterLines,
  churn: [
    "2026-10-30 12:00:31.880 Uncaught app execution",
    'Traceback (most recent call last):',
    '  File "/home/alice/sales/app.py", line 3, in <module>',
    '    import plotly.express as px',
    "ModuleNotFoundError: No module named 'plotly'",
  ],
  'orders-api': goLines,
}
const previousLines: Record<string, string[]> = { 'orders-api': goPanic, churn: streamlitLines }
// The whole output of a failed npm build, as the build container wrote it.
const npmLog = [
  'npm error code EUSAGE',
  'npm error',
  'npm error The `npm ci` command can only install with an existing package-lock.json or',
  'npm error npm-shrinkwrap.json with lockfileVersion >= 1. Run an install with npm@5 or',
  'npm error later to generate a package-lock.json file, then try again.',
  'npm error',
  'npm error Clean install a project',
  'npm error',
  'npm error Usage:',
  'npm error npm ci',
  'npm error',
  'npm error Run "npm help ci" for more info',
  'npm error A complete log of this run can be found in: /tmp/npm/_logs/2026-11-02T09_05_34_949Z-debug-0.log',
]
const buildLines = (name: string) => (name === 'team-blog' ? npmLog : []).map((text, i) => ({ time: at(20 + i / 10), text }))

const lines = (name: string, previous = false) =>
  ((previous ? previousLines[name] : logLines[name]) ?? []).map((text, i) => ({ time: at(31 + i), text }))

// The live log stream: replays the app's lines, then adds one every second.
class MockEventSource extends EventTarget {
  onopen: ((e: Event) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  private timer = 0
  constructor(url: string | URL) {
    super()
    const name = String(url).match(/\/api\/workspaces\/([^/]+)\/logs\/stream/)?.[1] ?? ''
    setTimeout(() => {
      this.readyState = 1
      this.onopen?.(new Event('open'))
      const send = (l: unknown) => this.dispatchEvent(new MessageEvent('line', { data: JSON.stringify(l) }))
      lines(name).forEach(send)
      if (name !== 'research-lab') return
      let n = 0
      this.timer = window.setInterval(() => {
        const ms = (12 + (n % 7) * 3.7).toFixed(2)
        const text = `[I ${new Date().toISOString().replace('T', ' ').slice(0, 23)} ServerApp] 200 GET /u/alice/research-lab/api/kernels?${++n} (alice@10.42.0.1) ${ms}ms`
        send({ time: new Date().toISOString(), text })
      }, 1000)
    }, 50)
  }
  close() {
    this.readyState = 2
    clearInterval(this.timer)
  }
}
Object.assign(window, { EventSource: MockEventSource })

// papermill's own output for a run that fails in cell 14.
const papermillLog = [
  'Input Notebook:  /home/alice/reports/sales.ipynb',
  'Output Notebook: /home/alice/reports/runs/run-3.ipynb',
  'Executing notebook with kernel: python3',
  'Executing:  65%|██████▌   | 13/20 [00:04<00:02,  3.10cell/s]',
  'Traceback (most recent call last):',
  '  File "/opt/pixi/envs/default/bin/papermill", line 10, in <module>',
  '    sys.exit(papermill())',
  '  File "/opt/pixi/envs/default/lib/python3.12/site-packages/papermill/execute.py", line 251, in raise_for_execution_errors',
  '    raise error',
  'papermill.exceptions.PapermillExecutionError: ',
  '---------------------------------------------------------------------------',
  'Exception encountered at "In [14]":',
  "KeyError: 'region'",
  '',
].join('\n')

// papermill's output for a run that went through.
const papermillDone = [
  'Input Notebook:  /home/alice/reports/sales.ipynb',
  'Output Notebook: /home/alice/reports/runs/run-2.ipynb',
  'Executing notebook with kernel: python3',
  'Executing: 100%|██████████| 20/20 [00:06<00:00,  3.22cell/s]',
  'Wrote jobs/weekly-report/run-2/output.html',
  '',
].join('\n')

// ?files=starting|busy|stale shows the picker's waiting, failure and updating states.
const filesMode = new URLSearchParams(window.location.search).get('files')
// ?empty=1 shows a user with no apps.
const listed = new URLSearchParams(window.location.search).get('empty') === '1' ? [] : apps

// alice's API tokens; ?tokens=none shows the empty list, ?docs=0 hides the API docs link.
const tokenParams = new URLSearchParams(window.location.search)
const aiParams = tokenParams
let sharingView = {
  access: 'Restricted',
  maxAccess: 'Public',
  apiAccess: 'None',
  users: [{ id: 'bob-5e6f7a8b', displayName: 'Bob', email: 'bob@example.com' }],
  groups: [],
  link: 'https://apps.example.com/u/alice/churn/',
  levels: [
    { value: 'Restricted', label: 'Only people added' },
    { value: 'SignedIn', label: 'Anyone signed in' },
    { value: 'Public', label: 'Anyone with the link' },
  ],
}
const tokens: { id: string; name: string; scope: 'read' | 'read-write'; prefix: string; createdAt: string; lastUsedAt?: string; expiresAt?: string; app?: string }[] =
  tokenParams.get('tokens') === 'none'
    ? []
    : [
        { id: 'tok-1', name: 'laptop', scope: 'read', prefix: 'javo_7f3a9c1e_', createdAt: at(-86400 * 40), lastUsedAt: at(-3600) },
        { id: 'tok-2', name: 'ci', scope: 'read-write', prefix: 'javo_b24e6d90_', createdAt: at(-86400 * 12), lastUsedAt: at(-86400), expiresAt: at(86400 * 78) },
        { id: 'tok-3', name: 'agent', scope: 'read-write', prefix: 'javo_51c08f2b_', createdAt: at(-86400 * 2), app: 'agent' },
      ]
// alice's secrets (names only: the API never returns a value); ?secrets=none shows the empty list.
type Use = { name: string; displayName: string }
const secrets: { name: string; note: string; createdAt: string; updatedAt: string; apps: Use[]; jobs: Use[] }[] =
  tokenParams.get('secrets') === 'none'
    ? []
    : [
        { name: 'OPENAI_KEY', note: 'Team billing account', createdAt: at(-86400 * 30), updatedAt: at(-86400 * 3), apps: [{ name: 'plots', displayName: 'Plot explorer' }], jobs: [] },
        {
          name: 'WAREHOUSE_PASSWORD', note: 'Read-only warehouse user', createdAt: at(-86400 * 20), updatedAt: at(-86400 * 20),
          apps: [{ name: 'plots', displayName: 'Plot explorer' }], jobs: [{ name: 'weekly-report', displayName: 'Weekly report' }],
        },
        { name: 'SLACK_WEBHOOK_URL', note: '', createdAt: at(-86400 * 2), updatedAt: at(-86400 * 2), apps: [], jobs: [] },
        { name: 'GH_DOCS_TOKEN', note: 'Read-only token for example-org', createdAt: at(-86400 * 9), updatedAt: at(-86400 * 9), apps: [{ name: 'private-docs', displayName: 'Private docs' }], jobs: [] },
      ]
// The weekly report's runs get the warehouse password; changing its secrets changes this copy.
const weekly = { ...weeklyDetail, env: [{ name: 'PGPASSWORD', secret: 'WAREHOUSE_PASSWORD' }, { name: 'REGION', value: 'emea' }] }
const hex = (n: number) => Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => b.toString(16).padStart(2, '0')).join('')

// The mock signs in a platform admin, so Settings shows the admin pages.
const admin = { ...me, roles: ['platform-admin', 'member'] }
const owners = [
  { name: 'alice-1a2b3c4d', displayName: 'Alice' },
  { name: 'bob-5e6f7a8b', displayName: 'Bob' },
  { name: 'carol-9c0d1e2f', displayName: 'Carol' },
]
// The All apps page also has a second Alice, told apart by username, and running apps ready for between
// seconds and days (from the real clock, as the page counts from it).
const adminOwners = [
  { ...owners[0], username: 'alice' },
  { ...owners[1], username: 'bob' },
  { ...owners[2], username: 'carol' },
  { name: 'alice-7f8e9d0c', displayName: 'Alice', username: 'alice.w' },
]
const readyMinutesAgo = [134, 4325, 12, 0.5, 61, 1500, 7, 300, 20000]
const readyAgo = (app: (typeof apps)[number], minutes: number) =>
  app.phase === 'Running' ? { ...app, steps: (app.steps ?? []).map((s) => (s.type === 'Ready' ? { ...s, since: new Date(Date.now() - minutes * 60000).toISOString() } : s)) } : app
const adminApps = apps.slice(0, 9).map((app, i) => ({
  id: `app-${i}`,
  owner: adminOwners[i % adminOwners.length],
  app: readyAgo(app, readyMinutesAgo[i]),
  createdAt: at(-86400 * (i + 1)),
  ...(app.phase === 'Running' ? { lastActiveAt: at(-600 * (i + 1)) } : {}),
}))
const backupName = (daysAgo: number) => `javo-${new Date(T0 - daysAgo * 86400000).toISOString().slice(0, 10).replaceAll('-', '')}T020000Z.db`
// ?backups=none: no schedule; otherwise healthy database backups.
const backupsOff = new URLSearchParams(window.location.search).get('backups') === 'none'
const databaseBackups = backupsOff
  ? {
      database: { engine: 'sqlite', schedule: '', keep: 7, canBackUpNow: false, items: [],
        status: { reason: 'DatabaseBackupsOff', title: 'Database not backed up', what: 'No backup schedule is set, so losing the database volume loses every app, template, job and setting.', todo: 'Set database.backup.schedule in the Helm values, for example 0 2 * * * for every night at 02:00.', problem: true } },
    }
  : {
      database: { engine: 'sqlite', schedule: '0 2 * * *', keep: 7, canBackUpNow: true, nextAt: at(50400),
        items: [0, 1, 2].map((d) => ({ name: backupName(d), takenAt: new Date(T0 - d * 86400000 - 36000000).toISOString(), size: 1835008 - d * 40960 })),
        status: { reason: 'DatabaseBackedUp', title: 'Database backed up', what: 'The database is copied on schedule and the newest copy is recent. A copy holds every app, template, job, person, token and setting; files in home folders are not in it.', todo: '', problem: false } },
    }
// ?backups=bucket: full backups to the S3-compatible bucket, run by the ordinary jobs "backup" and
// "restore": the newest run missed one home folder; two runs to pick from. ?backups=setup: a bucket but no backup
// job yet. The copy is the catalog's.
const backupsMode = new URLSearchParams(window.location.search).get('backups')
const bucketMode = backupsMode === 'bucket' || backupsMode === 'setup'
const homeOK = (ago: number) => ({ status: { reason: 'HomeBackedUp', title: 'Backed up', what: 'The newest backup of this home folder worked.', todo: 'Nothing to do.', problem: false }, lastAt: at(ago), evidence: [] })
const backupRun = (daysAgo: number, outcome: string, extra: Record<string, unknown> = {}) => ({
  run: `backup-29${daysAgo}1`, runId: `run-b${daysAgo}`, trigger: 'schedule', startedAt: at(-32400 - daysAgo * 86400), finishedAt: at(-32100 - daysAgo * 86400), outcome,
  status: outcome === 'Succeeded'
    ? { reason: 'FullBackupDone', title: 'Everything is backed up', what: 'The newest full backup copied the database, every home folder and every shared folder to the bucket.', todo: 'Nothing to do.', problem: false }
    : { reason: 'FullBackupProblems', title: 'The newest backup missed something', what: 'The newest full backup ran, but some parts were not copied, so their newest changes are in no backup.', todo: 'Open the details below or the job run\'s log to read why, fix the cause and back up now.', problem: true },
  homes: 3, homesFailed: 0, volumes: 1, volumesFailed: 0, folders: 1, foldersFailed: 0, ...extra,
})
const bucketState = {
  target: { endpoint: 's3.us-east-1.amazonaws.com', bucket: 'javo-backups', prefix: 'javo' },
  jobs: backupsMode === 'setup' ? {} : {
    backup: { id: 'j-backup', name: 'backup', owner: 'alice-1a2b3c4d', schedule: '0 1 * * *', timeZone: 'Europe/London', paused: false },
    restore: { id: 'j-restore', name: 'restore', owner: 'alice-1a2b3c4d', paused: false },
  },
  status: backupsMode === 'setup'
    ? { reason: 'BackupsNotSetUp', title: 'Backups not set up', what: 'There is no backup job yet, so nothing is backed up to the bucket.', todo: 'Press Set up backups: it makes the backup job, which runs every night, and the restore job.', problem: true }
    : backupRun(0, 'Failed').status,
  ...(backupsMode === 'setup' ? {} : {
    lastRun: backupRun(0, 'Failed', { database: backupName(0), homesFailed: 1 }),
    lastRestore: { ...backupRun(3, 'Succeeded'), run: 'restore-x1', runId: 'run-r1', trigger: 'manual', homes: 1, folders: 0 },
  }),
  runs: backupsMode === 'setup' ? [] : [backupRun(0, 'Failed', { homesFailed: 1 }), backupRun(1, 'Succeeded'), backupRun(2, 'Succeeded')],
  keepDaily: 7, keepWeekly: 4, keepMonthly: 6,
  homes: [
    { id: 'h-bob', owner: 'bob-5e6f7a8b', person: 'Bob Example (bob, SSO)', account: true, claim: 'home-bob-5e6f7a8b', namespace: 'javo-apps', lastAt: at(-32400 - 86400), evidence: ['Fatal: unable to open repository at s3:https://s3.us-east-1.amazonaws.com/javo-backups/javo/homes/javo-apps/bob-5e6f7a8b: Access Denied.'],
      status: { reason: 'BackupDenied', title: 'Backup bucket refused the key', what: 'The bucket refused the access key, so nothing was copied.', todo: 'Check the access key in the Secret named by backupTarget.existingSecret and that it may read and write the bucket.', problem: true } },
    { id: 'h-alice', owner: 'alice-1a2b3c4d', person: 'Alice Example (alice, SSO)', account: true, claim: 'home-alice-1a2b3c4d', namespace: 'javo-apps', ...homeOK(-32400) },
    { id: 'h-alice2', owner: 'alice-97e08fe0', person: 'alice (local)', account: true, claim: 'home-alice-97e08fe0', namespace: 'javo-apps', ...homeOK(-32390) },
    { id: 'h-carol', owner: 'carol-9c0d1e2f', person: 'Carol Example (carol, SSO)', account: true, claim: 'home-carol-9c0d1e2f', namespace: 'javo-apps', ...homeOK(-32380) },
    { id: 'h-old', owner: 'local-alice-6f5951a4', person: 'Home volume with no account', account: false, claim: 'home-local-alice-6f5951a4', namespace: 'javo-apps', ...homeOK(-32370) },
  ],
  folders: [{ id: 'f-data', owner: 'data-science', person: '/data-science', account: true, claim: 'folder-data-science', namespace: 'javo-apps', ...homeOK(-32300) }],
}
// Without a bucket home folders are in no backup (the catalog's copy).
const noBucketHomes = { reason: 'NoBackupTool', title: 'Home folders not backed up', what: 'Files in home folders are in no backup. The database backups above do not hold them.', todo: 'Turn on backups to an S3-compatible bucket: set backupTarget in the Helm values (works on any disk). The Backups page of the admin guide shows how.', problem: true }
const backups = { ...databaseBackups, ...(bucketMode ? { bucket: bucketState } : { homeFolders: noBucketHomes }) }
const restores = backupsOff || bucketMode ? [] : [{ name: backupName(1), held: 12, scheduled: 0, wasRunning: 7 }]
const previewRows = [
  { name: 'old-report', displayName: 'Old report', template: 'quarto', owner: 'bob-5e6f7a8b', outcome: 'TemplateMissing', message: 'Its template quarto does not exist here. Add the template first.', wasRunning: false },
  { name: 'plots', displayName: 'Plot explorer', template: 'bokeh', owner: 'alice-1a2b3c4d', outcome: 'Exists', message: 'An app with this name and owner exists now. It is left as it is.', wasRunning: true },
  { name: 'forecast', displayName: 'Sales forecast', template: 'panel', owner: 'bob-5e6f7a8b', outcome: 'Create', message: 'Will come back and wait for you to start the restored apps.', wasRunning: true },
  { name: 'kpis', displayName: 'Team KPIs', template: 'dash', owner: 'carol-9c0d1e2f', outcome: 'Create', message: 'Will come back and wait for you to start the restored apps.', wasRunning: false },
]
const importRows = [
  { name: 'churn', displayName: 'Churn dashboard', template: 'streamlit', outcome: 'Exists', message: 'You already have an app called Churn dashboard.', wasRunning: true },
  { name: 'forecast', displayName: 'Sales forecast', template: 'panel', outcome: 'Create', message: 'Will be added, stopped.', wasRunning: true },
]

function reply(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json', ...headers },
  })
}

const realFetch = window.fetch.bind(window)
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const req = input instanceof Request ? input : new Request(new URL(String(input), window.location.origin), init)
  const url = new URL(req.url)
  if (!url.pathname.startsWith('/api/')) return realFetch(input, init)
  const key = `${req.method} ${url.pathname}`
  const detail = url.pathname.match(/^\/api\/workspaces\/([^/]+)$/)
  if (key === 'POST /api/auth/local/login') return reply(200, me)
  if (key === 'POST /api/auth/logout') return reply(204, null)
  // AI is on in the mock unless ?ai=off.
  if (key === 'GET /api/ai/status') return reply(200, aiParams.get('ai') === 'off' ? aiOff : aiOn)
  // The coding agent has no model yet (Describe it says how to add one); ?agent=off has no agent at all.
  if (key === 'GET /api/ai/agent')
    return reply(200, { available: aiParams.get('agent') !== 'off', mode: 'login', template: 'pi', templateName: 'pi coding agent' })
  // Every app is shared with the people added, and a change is kept while the page is open.
  const sharing = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/sharing$/)
  if (sharing) {
    if (req.method === 'PUT') sharingView = { ...sharingView, access: (await req.json()).access }
    return reply(200, sharingView)
  }
  const diag = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/diagnosis(\/explain)?$/)
  if (diag && req.method === 'GET') {
    if (diag[1] === 'churn') return reply(200, diagnosis({ proposals: [{ action: 'restart', target: 'churn', summary: 'Restart Churn dashboard' }] }))
    if (diag[1] === 'model') return reply(200, diagnosis({ rule: 'from-condition', code: 'ImagePullFailed', known: false, ...REASONS.ImagePullFailed, proposals: [] }))
    return reply(200, diagnosis({ rule: 'from-condition', code: 'Unknown', known: false, proposals: [] }))
  }
  if (diag && req.method === 'POST') {
    const preview = (await req.json()).preview
    return reply(200, {
      ...(preview ? {} : { explanation: { summary: 'The dashboard imports plotly, which is not installed in its environment, so it stops at start.', cause: 'plotly is missing from the environment.', confidence: 'high', evidence: ['log/app/prev/41'], fix: 'Add plotly to the environment, then restart.', actions: [{ action: 'restart', target: diag[1] }] }, model: 'claude-opus-5-5' }),
      bundle: { text: '<untrusted ref="condition/Started" kind="condition">container app terminated: Error, exit code 1</untrusted>', refs: ['condition/Started'], hidden: 0, withheld: 2, notes: [AI_TEXT.withheld] },
      dropped: { refs: 0, actions: 0 },
      proposals: preview ? [] : [{ action: 'restart', target: diag[1], summary: 'Restart Churn dashboard' }],
    })
  }
  if (key === 'POST /api/plans') return reply(201, plan())
  if (key === 'GET /api/plans/p1') return reply(200, plan({ source: 'mcp:claude-code', planned: { action: 'stop', target: 'plots', summary: 'Stop Plot explorer', destructive: false, changes: [{ field: 'state', from: 'Running', to: 'Stopped' }] } }))
  const decided = url.pathname.match(/^\/api\/plans\/([^/]+)\/(approve|reject)$/)
  if (decided) return reply(200, plan({ id: decided[1], state: decided[2] === 'approve' ? 'applied' : 'rejected' }))
  if (key === 'POST /api/ai/feedback') return reply(204, null)
  if (key === 'POST /api/ai/command' && /\b(create|new)\b/i.test((await req.clone().json()).text))
    return reply(200, {
      reply: 'Here is a new Streamlit app to review before it is created.',
      plans: [],
      draft: { draft: { displayName: 'Sales dashboard', template: 'streamlit', file: 'sales/app.py' }, setBy: ['displayName', 'template', 'file'], errors: [], model: 'claude-opus-5-5' },
      dropped: { refs: 0, actions: 0 },
    })
  if (key === 'POST /api/ai/command')
    return reply(200, {
      reply: 'Plot explorer and Research lab have not been used for over an hour. I can stop both.',
      plans: [
        plan({ id: 'p1', source: 'palette', planned: { action: 'stop', target: 'plots', summary: 'Stop Plot explorer', destructive: false, changes: [{ field: 'state', from: 'Running', to: 'Stopped' }] } }),
        plan({ id: 'p2', source: 'palette', planned: { action: 'stop', target: 'research-lab', summary: 'Stop Research lab', destructive: false, changes: [{ field: 'state', from: 'Running', to: 'Stopped' }] } }),
      ],
      dropped: { refs: 0, actions: 0 },
    })
  if (key === 'POST /api/ai/draft')
    return reply(200, { draft: { displayName: 'Sales', template: 'streamlit', file: 'sales/app.py' }, setBy: ['displayName', 'template', 'file'], errors: [], model: 'claude-opus-5-5' })
  if (key === 'GET /api/admin/report')
    return reply(200, { rows: [
      { ref: 'failures/AppCrashed', title: 'Apps held by: App crashed on start', value: 1, unit: 'apps', objects: ['javo-apps/churn'] },
      { ref: 'failures/NoCapacity', title: 'Apps held by: No machine has room', value: 1, unit: 'apps', objects: ['javo-apps/report'] },
      { ref: 'phase/Running', title: 'Apps Running', value: 7, unit: 'apps', objects: [] },
    ] })
  if (key === 'POST /api/admin/ai/ask')
    return reply(200, { answer: 'One app crashed on start and one waits for a machine with room.', refs: ['failures/AppCrashed', 'failures/NoCapacity'], dropped: { refs: 0, actions: 0 } })
  if (key === 'GET /api/me') return reply(200, admin)
  if (key === 'GET /api/admin/apps') return reply(200, { items: adminApps })
  if (key === 'GET /api/admin/usage') {
    const group = url.searchParams.get('by') === 'group'
    return reply(200, {
      since: at(-7 * 86400),
      totals: { hours: 131.5, people: 3, runningNow: 4 },
      items: group
        ? [
            { key: 'data-science', displayName: 'data-science', hours: 96.5, apps: 6, people: 2, runningNow: 3 },
            { key: 'ops', displayName: 'ops', hours: 35, apps: 2, people: 1, runningNow: 1 },
          ]
        : [
            { key: 'alice-1a2b3c4d', displayName: 'Alice', hours: 74.5, apps: 4, people: 1, runningNow: 2 },
            { key: 'bob-5e6f7a8b', displayName: 'Bob', hours: 35, apps: 2, people: 1, runningNow: 1 },
            { key: 'carol-9c0d1e2f', displayName: 'Carol', hours: 22, apps: 2, people: 1, runningNow: 1 },
          ],
    })
  }
  if (key === 'GET /api/admin/backups') return reply(200, backups)
  if (key === 'POST /api/admin/backups') return reply(201, backups.database.items[0] ?? {})
  if (key === 'GET /api/admin/restores') return reply(200, { items: restores })
  if (key === 'POST /api/admin/backups/now') return reply(201, { job: 'backup', owner: 'alice-1a2b3c4d', runId: 'run-b9' })
  if (key === 'POST /api/admin/backups/restore') return reply(201, { job: 'restore', owner: 'alice-1a2b3c4d', runId: 'run-r9' })
  if (/^POST \/api\/admin\/backups\/[^/]+\/preview$/.test(key)) return reply(200, { rows: previewRows })
  if (/^POST \/api\/admin\/backups\/[^/]+\/restore$/.test(key))
    return reply(200, { rows: previewRows.map((r) => (r.outcome === 'Create' ? { ...r, outcome: 'Created', message: 'Back, waiting for you to start the restored apps.' } : r)) })
  if (/^POST \/api\/admin\/restores\/[^/]+\/release$/.test(key)) {
    restores.splice(0)
    return reply(200, { released: 12 })
  }
  if (key === 'POST /api/workspaces/import/preview') return reply(200, { rows: importRows })
  if (key === 'POST /api/workspaces/import')
    return reply(200, { rows: importRows.map((r) => (r.outcome === 'Create' ? { ...r, outcome: 'Created', message: 'Added, stopped. Start it when you need it.' } : { ...r, outcome: 'Skipped', message: 'Skipped: you already have an app with this name.' })) })
  const verb = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/(stop|start)$/)
  if (req.method === 'POST' && verb) {
    const w = apps.find((a) => a.name === verb[1])
    if (!w) return reply(404, { title: 'No app with that name.' })
    return reply(200, { ...w, phase: verb[2] === 'stop' ? 'Stopping' : 'Starting' })
  }
  if (key === 'GET /api/templates') return reply(200, { items: allTemplates, categories: templateCategories })
  if (key === 'GET /api/workspaces') {
    // Created a day apart, newest first, as the API orders them.
    const dated = (items: typeof listed) => items.map((w, i) => ({ ...w, createdAt: at(-86400 * (i + 1)) }))
    return reply(200, { items: dated(url.searchParams.get('previews') === 'true' ? [...listed, ...previewApps] : listed) })
  }
  const previews = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/previews$/)
  if (previews && previewSettings[previews[1]]) {
    if (req.method === 'PUT') {
      const b = await req.json()
      previewSettings[previews[1]] = { enabled: b.enabled, max: b.max, forks: !!b.forks }
    }
    return reply(200, previewsView(previews[1]))
  }
  if (key === 'GET /api/previews') return reply(200, { available: true, max: 3, maxAllowed: 5, forksWarning: FORKS_WARNING })
  if (key === 'POST /api/previews/webhook') {
    const id = hex(16)
    return reply(200, { id, url: `${window.location.origin}/hooks/github/${id}`, secret: hex(32), notice: 'Copy the secret now. It is shown once and cannot be shown again.' })
  }
  if (key === 'POST /api/workspaces/site/previews/webhook') {
    return reply(200, {
      url: `${window.location.origin}/hooks/github/${hex(16)}`,
      secret: hex(32),
      notice: 'Copy the secret now. It is shown once and cannot be shown again.',
    })
  }
  const source = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/(redeploy|source\/token)$/)
  if (source) {
    const w = [...apps, ...previewApps].find((a) => a.name === source[1])
    if (!w) return reply(404, { title: 'No app with that name.' })
    if (source[2] === 'redeploy') return reply(200, { ...w, phase: 'Starting', steps: stepsAt(2, 'Starting') })
    if (w.source) w.source = { ...w.source, hasToken: req.method === 'PUT' }
    return reply(204, null)
  }
  if (key === 'GET /api/sizes') return reply(200, { items: singleNodeSizes, custom: allowance })
  if (key === 'GET /api/idle-stop') return reply(200, idlePolicy)
  if (key === 'GET /api/shared')
    return reply(200, {
      empty: 'Nothing has been shared with you yet.',
      items: [
        { name: 'forecast', displayName: 'Sales forecast', template: 'panel', kind: 'panel', phase: 'Running', url: 'https://apps.example.com/u/bob/forecast/', owner: { id: 'bob-bbbbbbbb', displayName: 'Bob' }, via: 'user', createdAt: at(-86400 * 2.5) },
        { name: 'pricing', displayName: 'Pricing simulator', template: 'claude-code', kind: 'claude-code', phase: 'Running', url: 'https://apps.example.com/u/bob/pricing/', owner: { id: 'bob-bbbbbbbb', displayName: 'Bob' }, via: 'user', createdAt: at(-86400 * 3.5), agentPrompt: 'Make a pricing simulator where I can drag a price slider and see projected revenue and churn.' },
        { name: 'kpis', displayName: 'Team KPIs', template: 'dash', kind: 'dash', phase: 'Stopped', url: 'https://apps.example.com/u/carol/kpis/', owner: { id: 'carol-cccccccc', displayName: 'Carol' }, via: 'group', createdAt: at(-86400 * 6.5) },
      ],
    })
  if (key === 'GET /api/storage') return reply(200, storage)
  if (key === 'GET /api/storage/placement') {
    const fits = url.searchParams.get('size') !== 'large'
    const text = `You have 2 apps on machine cpu-standard-3 with 5 CPUs free. ${fits ? 'This app fits.' : 'This app will wait until you stop something.'}`
    const placement = notice(fits ? 'PlacementFits' : 'PlacementWaits', fits ? 'info' : 'warn', 'Will run next to your other apps', text, fits ? 'Nothing to do.' : 'Stop an app to make room, or ask an admin to enable shared storage.')
    return reply(200, { coLocated: noshared, fits, freeCPU: 5, freeMemory: 8 * 2 ** 30, node: 'cpu-standard-3', runningApps: ['Sales app', 'Research lab'], ...(noshared ? { notice: placement } : {}) })
  }
  if (url.pathname === '/api/admin/ai' && me.roles.includes('platform-admin')) {
    if (req.method === 'PUT') {
      const b = await req.json()
      const key = b.apiKey ? String(b.apiKey) : ''
      const keyStored = key ? true : b.removeKey ? false : aiSettings.keyStored
      aiSettings = {
        ...aiSettings,
        overridden: true,
        provider: 'openai',
        baseURL: b.baseURL ?? '',
        model: b.model,
        keyStored,
        keyLast4: key ? key.slice(-4) : keyStored ? aiSettings.keyLast4 : '',
        keyFrom: keyStored ? 'settings' : 'none',
        updatedAt: new Date().toISOString(),
      }
      return reply(200, aiSettings)
    }
    if (req.method === 'DELETE') {
      aiSettings = { ...aiSettingsFromConfig }
      return reply(204, null)
    }
    return reply(200, aiSettings)
  }
  if (key === 'GET /api/admin/storage') {
    return me.roles.includes('platform-admin')
      ? reply(200, adminStorage)
      : reply(404, { status: 404, title: 'Not found' })
  }
  const resize = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/size$/)
  if (req.method === 'PUT' && resize) {
    const w = apps.find((a) => a.name === resize[1])
    if (!w) return reply(404, { title: 'No app with that name.' })
    const b = await req.json()
    w.size = b.size ?? ''
    w.resources = b.resources
    return reply(200, w)
  }
  const access = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/api-access$/)
  if (req.method === 'PUT' && access) {
    const w = apps.find((a) => a.name === access[1])
    if (!w) return reply(404, { title: 'No app with that name.' })
    w.apiAccess = (await req.json()).apiAccess
    return reply(200, w)
  }
  const idle = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/idle-stop$/)
  if (req.method === 'PUT' && idle) {
    const w = apps.find((a) => a.name === idle[1])
    if (!w) return reply(404, { title: 'No app with that name.' })
    w.idleStopMinutes = (await req.json()).idleStopMinutes
    return reply(200, w)
  }
  const relaunch = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/launch$/)
  if (req.method === 'PUT' && relaunch) {
    const w = apps.find((a) => a.name === relaunch[1])
    if (!w) return reply(404, { title: 'No app with that name.' })
    const b = await req.json()
    if (w.image && !String(b.image ?? '').includes('/')) {
      return reply(422, { title: 'Invalid', errors: [{ location: 'body.image', message: 'Images from docker.io are not allowed here. Use an image from ghcr.io/example.' }] })
    }
    Object.assign(w, { command: b.command ?? '', image: b.image ?? '', port: b.port ?? 0 })
    return reply(200, w)
  }
  const sub = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/(events|logs)$/)
  if (req.method === 'GET' && sub) {
    if (sub[2] === 'events') return reply(200, { items: events[sub[1]] ?? [] })
    const previous = url.searchParams.get('previous') === 'true'
    if (url.searchParams.get('step') === 'Built') return reply(200, { lines: buildLines(sub[1]), restarts: 2 })
    const restarts = sub[1] === 'churn' ? 6 : sub[1] === 'orders-api' ? 1 : 0
    return reply(200, { lines: previous ? lines(sub[1], true) : lines(sub[1]).slice(-Number(url.searchParams.get('tail') ?? 200)), restarts })
  }
  if (req.method === 'GET' && detail) {
    const w = [...apps, ...previewApps].find((a) => a.name === detail[1])
    return w ? reply(200, w) : reply(404, { title: 'Not Found', detail: 'No app with that name.' })
  }
  if (key === 'POST /api/workspaces') {
    const b = await req.json()
    if (b.displayName === 'Sales app') {
      return reply(422, { title: 'Unprocessable Entity', errors: [{ location: 'body.displayName', message: 'You already have an app called "Sales app".' }] })
    }
    return reply(201, ws('new-app', b.displayName, 'Pending', stepsAt(0, 'Starting')))
  }
  if (key === 'GET /api/files') {
    if (filesMode === 'starting') return reply(503, { status: 503, reason: 'HomeStarting', ...REASONS.HomeStarting }, { 'Retry-After': '2' })
    if (url.searchParams.get('place') === 'group:data-science') {
      const entries = [{ name: 'shared-notes.md', kind: 'file', size: 1024, modified: '2026-10-20T10:00:00Z' }]
      return reply(200, { path: '', entries, count: 1, truncated: false })
    }
    if (filesMode === 'busy') return reply(409, { status: 409, reason: 'HomeBusy', ...REASONS.HomeBusy })
    const p = url.searchParams.get('path') ?? ''
    const entries = folders[p]
    if (!entries) return reply(404, { status: 404, title: 'That folder does not exist' })
    const stale = filesMode === 'stale'
    return reply(200, { path: p, entries, count: entries.length, truncated: false, stale, ageSeconds: stale ? 40 : 0 })
  }
  if (key === 'POST /api/files/warm') return reply(204, null)
  if (key === 'GET /api/files/raw' && url.searchParams.get('path') === 'plots/app.py') {
    const text = 'from bokeh.plotting import curdoc, figure\n\np = figure(title="Plots")\np.line([1, 2, 3], [4, 6, 5])\ncurdoc().add_root(p)\n'
    return new Response(text, { status: 200, headers: { 'Content-Type': 'text/plain', ETag: '"92-1"', 'Content-Length': String(text.length) } })
  }
  if (key === 'GET /api/info') return reply(200, { apiDocs: tokenParams.get('docs') !== '0', tokenMaxDays: 90 })
  if (key === 'GET /api/tokens') return reply(200, tokens)
  if (key === 'POST /api/tokens') {
    const b = await req.json()
    if (tokens.some((t) => t.name === b.name)) {
      return reply(422, { title: 'Unprocessable Entity', errors: [{ location: 'body.name', message: `You already have a token called "${b.name}".` }] })
    }
    const prefix = `javo_${hex(4)}_`
    const now = new Date().toISOString()
    const t = { id: `tok-${hex(4)}`, name: b.name, scope: b.scope, prefix, createdAt: now, ...(b.expiresInDays ? { expiresAt: new Date(Date.now() + b.expiresInDays * 86400000).toISOString() } : {}) }
    tokens.push(t)
    return reply(201, { ...t, token: prefix + hex(24) })
  }
  if (key === 'GET /api/secrets') return reply(200, secrets)
  if (key === 'POST /api/secrets') {
    const b = await req.json()
    if (secrets.some((s) => s.name === b.name)) {
      return reply(422, { title: 'Unprocessable Entity', errors: [{ location: 'body.name', message: 'You already have a secret with this name. Pick another name, or change that secret\'s value.' }] })
    }
    const now = new Date().toISOString()
    const s = { name: b.name, note: b.note ?? '', createdAt: now, updatedAt: now, apps: [], jobs: [] }
    secrets.push(s)
    return reply(201, s)
  }
  const sec = url.pathname.match(/^\/api\/secrets\/([^/]+)$/)
  if (sec) {
    const i = secrets.findIndex((s) => s.name === sec[1])
    if (i < 0) return reply(404, { title: 'No secret with that name.' })
    if (req.method === 'PUT') {
      const b = await req.json()
      Object.assign(secrets[i], { ...(b.note !== undefined ? { note: b.note } : {}), updatedAt: new Date().toISOString() })
      return reply(200, secrets[i])
    }
    if (req.method === 'DELETE') {
      const users = [...secrets[i].apps, ...secrets[i].jobs]
      if (users.length) {
        return reply(409, { status: 409, title: `In use by ${users.map((a) => a.displayName).join(', ')}. Take the secret off them first.` })
      }
      secrets.splice(i, 1)
      return reply(204, null)
    }
  }
  const srcToken = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/source\/token$/)
  if (srcToken && (req.method === 'PUT' || req.method === 'DELETE')) {
    const w = apps.find((a) => a.name === srcToken[1])
    if (!w?.source) return reply(404, { title: 'No app with that name.' })
    const secret = req.method === 'PUT' ? (await req.json()).secret : ''
    w.source = { ...w.source, tokenSecret: secret, hasToken: !!secret }
    return reply(204, null)
  }
  const appEnv = url.pathname.match(/^\/api\/workspaces\/([^/]+)\/env$/)
  if (req.method === 'PUT' && appEnv) {
    const w = apps.find((a) => a.name === appEnv[1])
    if (!w) return reply(404, { title: 'No app with that name.' })
    w.env = (await req.json()).env
    return reply(200, w)
  }
  const tok = url.pathname.match(/^\/api\/tokens\/([^/]+)$/)
  if (req.method === 'DELETE' && tok) {
    const i = tokens.findIndex((t) => t.id === tok[1])
    if (i < 0) return reply(404, { title: 'Not Found', detail: 'No token with that id.' })
    tokens.splice(i, 1)
    return reply(204, null)
  }
  if (key === 'GET /api/engines') return reply(200, { items: engines })
  if (key === 'POST /api/jobs') return reply(201, printTimeDetail)
  if (key === 'GET /api/jobs') return reply(200, { items: [weeklyView, printTimeDetail] })
  if (key === 'GET /api/jobs/weekly-report') return reply(200, weekly)
  if (key === 'PUT /api/jobs/weekly-report/env') {
    weekly.env = (await req.json()).env
    return reply(200, weekly)
  }
  if (key === 'PUT /api/jobs/weekly-report/size') {
    const body = await req.json()
    Object.assign(weekly, { size: body.size ?? '', resources: body.resources })
    return reply(200, weekly)
  }
  if (key === 'GET /api/jobs/print-the-time') return reply(200, printTimeDetail)
  if (key === 'POST /api/jobs/print-the-time/run') return reply(201, { ...weeklyDetail.runs[1], id: 'run-1', outcome: 'Running' })
  if (key === 'POST /api/jobs/weekly-report/run') return reply(201, { ...weeklyDetail.runs[1], id: 'run-4', outcome: 'Running' })
  if (key === 'GET /api/jobs/weekly-report/runs/run-3/log') {
    return new Response(papermillLog, { headers: { 'Content-Type': 'text/plain', 'X-Log-Source': 'live' } })
  }
  // run-2 finished long ago and its pod is gone: the log saved in its run folder.
  if (key === 'GET /api/jobs/weekly-report/runs/run-2/log') {
    return new Response(papermillDone, { headers: { 'Content-Type': 'text/plain', 'X-Log-Source': 'saved' } })
  }
  if (key === 'POST /api/schedule/preview') {
    const b = await req.json()
    if (!/^\S+( \S+){4}$/.test(b.cron.trim())) {
      return reply(422, { title: 'Unprocessable Entity', errors: [{ location: 'body.cron', message: 'A schedule has five fields: minute hour day month weekday.' }] })
    }
    // A run in the small hours away from UTC meets the clock changes (the real check is the server's).
    const [mi, h] = b.cron.trim().split(/\s+/)
    const dst = b.timeZone !== 'UTC' && /^\d+$/.test(mi) && /^[12]$/.test(h)
    const time = `${h.padStart(2, '0')}:${mi.padStart(2, '0')}`
    const warnings = dst
      ? [
          { code: 'DSTSkipped', date: '2027-03-28', time, until: time, runs: 1, text: `Daylight saving on Sun 28 Mar 2027 skips the ${time} run.` },
          { code: 'DSTRepeated', date: '2027-10-31', time, until: time, runs: 1, text: `Daylight saving on Sun 31 Oct 2027 repeats the ${time} run.` },
        ]
      : []
    return reply(200, { next: ['2026-11-02T05:00:00Z', '2026-11-09T05:00:00Z', '2026-11-16T05:00:00Z'], warnings })
  }
  return reply(500, { title: `mock has no answer for ${key}` })
}

await import('../src/main')
