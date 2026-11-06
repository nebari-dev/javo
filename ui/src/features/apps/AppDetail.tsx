import { CircleCheck, Clock3, FileText, Play, RotateCcw, Terminal, Trash2, X } from 'lucide-react'
import { PartIcon } from '../../components/Section'
import { OpenAppLink } from './OpenAppLink'
import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import type { Event as KubeEvent, PodRun, Step, Workspace } from '../../api'
import { ApiAccessChoices, apiAccessLabel, type ApiAccess } from './ApiAccess'
import { Banner } from '../../components/Banner'
import { AppApprovals } from '../approvals/AppApprovals'
import { Button } from '../../components/Button'
import { EnvironmentRow } from '../environments/EnvironmentCard'
import { CodeBlock } from '../../components/CodeBlock'
import { CopyLink } from '../../components/CopyLink'
import { IconButton } from '../../components/IconButton'
import { PreviewsCard } from './PreviewsCard'
import { APP_ENV_HINT } from '../environments/EnvEditor'
import { EnvSetting } from '../environments/EnvSetting'
import { SourceCard } from './SourceCard'
import { ConfirmDelete } from './DeleteApp'
import { StopButton } from './StopButton'
import { Card } from '../../components/Card'
import { DiagnosedFailure } from '../ai/Diagnosis'
import { IdleStopChoice, idleStopLabel } from './IdleStop'
import { KindLogo } from '../../components/KindLogo'
import { LaunchFields, launchBody, type Launch } from './LaunchFields'
import { CommandLine, StartedWith } from './RunCommand'
import { LiveLogs } from '../logs/LiveLogs'
import { StepLog } from '../logs/StepLog'
import { LogView } from '../logs/LogView'
import { More, MoreToggle } from '../../components/More'
import { Pill } from '../../components/Pill'
import { choiceOf, SizeSetting } from '../sizes/SizeSetting'
import { SharingPanel } from '../sharing/SharingPanel'
import { Tabs } from '../../components/Tabs'
import { ThumbnailCard } from './ThumbnailCard'
import { Uptime } from './Uptime'
import { Timeline, type StepState, type TimelineStep } from '../../components/Timeline'
import { fieldErrors, problemText } from '../../lib/http'
import {
  isSettling,
  useIdleStopPolicy,
  useSetApiAccess,
  useSetIdleStop,
  useSetAppEnv,
  useSetLaunch,
  useSetSize,
  useStartStop,
  useTemplate,
  useStorage,
  useTemplateName,
  useWorkspace,
  useWorkspaceEvents,
  useWorkspaceLogs,
  type StreamedStep,
} from '../../lib/queries'
import type { SizeChoice } from '../../lib/sizes'
import { startupSeconds, stepTimes } from './startup'
import { appLink, heldStep, migratingStep, pillStatus, problemStep, stopNote } from './status'
import { PromptBlock } from './AgentPrompt'
import { AGENTS } from './agents'
import { TrustBanner, TrustCard } from './Trust'

// What each startup step does, in the order the API sends them.
const STEP_NAMES: Record<string, string> = {
  Scheduled: 'Find a machine',
  HomeReady: 'Attach your home folder',
  ImageReady: 'Download the image',
  Built: 'Build the site',
  EnvReady: 'Set up the environment',
  Started: 'Start the app',
  Ready: 'Check it answers',
}

// The step of an app that builds its site.
const BUILD_STEP = 'Built'

// The steps whose containers' output is followed live, and the step whose
// output is the app's own.
const STREAMED = new Set<string>(['HomeReady', 'EnvReady', 'Built', 'Started'])
const APP_STEP = 'Started'

// Never shown: a step's event view appears only when it has events.
const NO_LINES = { title: 'No lines', body: '' }

/**
 * A step's details: what the API recorded about it (kept after its events
 * expire), its events, and for a step reached so far the live output of
 * the containers doing its work; undefined when there is nothing. A problem
 * step's recorded lines are on the failure card instead.
 */
function stepLogs(name: string, step: Step, state: StepState, following: boolean, problem: Step | undefined, events: KubeEvent[]) {
  const facts = step === problem ? [] : (step.evidence ?? [])
  const evs = events.filter((e) => e.step === step.type)
  const streamed = STREAMED.has(step.type) && state !== 'todo'
  if (facts.length === 0 && evs.length === 0 && !streamed) return undefined
  return (
    <div className="step-logs">
      {facts.length > 0 && (
        <ul className="step-facts" aria-label="What happened">
          {facts.map((f, i) => (
            <li key={i}>{f}</li>
          ))}
        </ul>
      )}
      {evs.length > 0 && (
        <LogView
          compact
          title="Events"
          label="Events"
          empty={NO_LINES}
          lines={evs.map((e) => ({
            time: e.time,
            label: e.count > 1 ? `${e.reason} x${e.count}` : e.reason,
            text: e.message,
            warn: e.type === 'Warning',
          }))}
        />
      )}
      {streamed && <StepLog name={name} step={step.type as StreamedStep} following={following} />}
    </div>
  )
}

function timeline(ws: Workspace, problem: Step | undefined, events: KubeEvent[]): TimelineStep[] {
  const steps = ws.steps ?? []
  const current = steps.findIndex((s) => s.status !== 'True')
  const times = stepTimes(steps)
  // While the app is checked its own output is still the start step's.
  const checking = ws.phase !== 'Failed' && steps[current]?.type === 'Ready'
  return steps.map((s, i) => {
    let state: StepState = 'todo'
    if (s.status === 'True') state = 'done'
    else if (i === current) state = ws.phase === 'Failed' ? 'bad' : 'now'
    const watching = state === 'now' || (checking && s.type === APP_STEP)
    return {
      title: STEP_NAMES[s.type] ?? s.type,
      detail: s === problem ? s.title : undefined,
      state,
      time: times[i],
      logs: stepLogs(ws.name, s, state, watching, problem, events),
      logsOpen: state === 'bad' || watching,
    }
  })
}

function Actions({ ws, managed }: { ws: Workspace; managed: boolean }) {
  const act = useStartStop(ws.name)
  const [confirming, setConfirming] = useState(false)
  const deleteBtn = useRef<HTMLButtonElement>(null)
  const navigate = useNavigate()
  const open = ws.phase === 'Running' ? appLink(ws.url) : undefined
  const busy = act.isPending
  return (
    <div className="actions-col">
      <div className="actions">
        {open && (
          <OpenAppLink className="btn primary" href={open} icon={agentOpenIcon(ws.kind)}>
            {AGENTS[ws.kind] ? `Open ${AGENTS[ws.kind]}` : 'Open'}
          </OpenAppLink>
        )}
        {ws.phase === 'Stopping' || heldStep(ws) ? null : ws.phase === 'Stopped' ? (
          <Button variant="primary" icon={Play} disabled={busy} onClick={() => act.mutate('start')}>
            Start
          </Button>
        ) : (
          <>
            <Button icon={RotateCcw} disabled={busy} onClick={() => act.mutate('restart')}>
              Restart
            </Button>
            <StopButton ws={ws} act={act} />
          </>
        )}
        {!managed && (
          <Button ref={deleteBtn} variant="ghost" icon={Trash2} onClick={() => setConfirming(true)}>
            Delete
          </Button>
        )}
      </div>
      {act.isError && <span className="errmsg">{problemText(act.error)}</span>}
      {confirming && (
        <ConfirmDelete
          ws={ws}
          onDeleted={() => navigate('/apps')}
          onCancel={() => {
            setConfirming(false)
            deleteBtn.current?.focus()
          }}
        />
      )}
    </div>
  )
}

// How long the steps of an app that just started stay on screen, all done,
// before they fold into the start time; and how long the fold takes.
const STARTED_HOLD_MS = 4000
const FOLD_MS = 400

/**
 * Whether the app reached Running while this page watched it: started from
 * then until the phase changes again or the person dismisses it, and hold
 * for its first STARTED_HOLD_MS. An app already running when the page opens
 * never counts. Worked out while rendering, so the first Running render has it.
 */
function useJustStarted(phase: string): { started: boolean; hold: boolean; dismiss: () => void } {
  const [prev, setPrev] = useState(phase)
  const [started, setStarted] = useState(false)
  const [hold, setHold] = useState(false)
  if (phase !== prev) {
    const now = phase === 'Running' && prev !== ''
    setPrev(phase)
    setStarted(now)
    setHold(now)
  }
  useEffect(() => {
    if (!hold) return
    const t = setTimeout(() => setHold(false), STARTED_HOLD_MS)
    return () => clearTimeout(t)
  }, [hold])
  return { started, hold, dismiss: () => setStarted(false) }
}

// The Open button of an agent whose logo reads on the dark button carries
// that logo (herdr's mark is drawn in the text colour, so it keeps the icon).
function agentOpenIcon(kind: string): ReactNode {
  return kind === 'pi' || kind === 'claude-code' ? <KindLogo kind={kind} inline /> : undefined
}

/**
 * Says an app that just started is ready, with its address, a link to open
 * it and a button to copy the address. For a coding agent it says the agent
 * is running: building when it was started with a request (Build with), and
 * that the apps it deploys appear in Apps.
 */
function ReadyCard({ ws, link, building, onDismiss }: { ws: Workspace; link: string; building: boolean; onDismiss: () => void }) {
  // A path on this host is copied as a whole address, to paste anywhere.
  const address = new URL(link, window.location.href).href
  const agent = AGENTS[ws.kind]
  const title = !agent ? 'Your app is ready' : building ? `${agent} is building your app` : `${agent} is ready`
  return (
    <Banner
      tone="ok"
      className="ready-card"
      title={title}
      actions={
        <>
          <OpenAppLink className="btn primary sm" href={link} icon={agentOpenIcon(ws.kind)}>
            {agent ? `Open ${agent}` : 'Open app'}
          </OpenAppLink>
          <CopyLink link={address} />
          <IconButton icon={X} label="Dismiss" onClick={onDismiss} />
        </>
      }
    >
      {agent &&
        (building
          ? `${agent} is writing the code. When it's done, your new app appears in Apps with its own link. Open ${agent} to watch it work or answer its questions.`
          : `Open ${agent} and tell it what to build. Apps it deploys appear in Apps.`)}
    </Banner>
  )
}

function Progress({ ws, justStarted }: { ws: Workspace; justStarted: boolean }) {
  const problem = problemStep(ws)
  const settling = isSettling(ws)
  const events = useWorkspaceEvents(ws.name, settling)
  const steps = <Timeline label="Startup steps" steps={timeline(ws, problem, events.data ?? [])} />
  if (ws.phase === 'Running') {
    const secs = startupSeconds(ws.steps ?? [])
    return <StartedSteps started={secs !== undefined ? `Started in ${secs} s` : 'Started'} hold={justStarted}>{steps}</StartedSteps>
  }
  return steps
}

/**
 * The steps of a running app behind its start time. Just after the app
 * started they stay open, all done, then fold smoothly; the person can open
 * them again.
 */
function StartedSteps({ started, hold, children }: { started: string; hold: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(hold)
  const [shown, setShown] = useState(hold)
  const regionId = useId()
  useEffect(() => {
    if (hold) {
      setOpen(true)
      setShown(true)
    } else setOpen(false)
  }, [hold])
  useEffect(() => {
    if (open) {
      setShown(true)
      return
    }
    // The steps stay in the page while they fold, then go (and with them their streams).
    const t = setTimeout(() => setShown(false), FOLD_MS)
    return () => clearTimeout(t)
  }, [open])
  return (
    <div className="more">
      <MoreToggle open={open} onToggle={() => setOpen(!open)} controls={regionId}>
        <CircleCheck size={15} aria-hidden="true" className="started-ok" />
        {open ? `${started}: hide the startup steps` : `${started}: show the startup steps`}
      </MoreToggle>
      <div id={regionId} className="fold" data-open={open ? 'true' : 'false'}>
        <div className="fold-inner">{shown && children}</div>
      </div>
    </div>
  )
}

// The log card reads the app's restart count from its latest lines.
const LOGS_CARD_TAIL = 20

/** The running app's own log: folded until asked for, then followed live. */
function LogsCard({ name, run }: { name: string; run?: PodRun }) {
  const [open, setOpen] = useState(false)
  const logs = useWorkspaceLogs(name, { tail: LOGS_CARD_TAIL, enabled: open })
  return (
    <LiveLogs
      name={name}
      title="Running app's log"
      top={run && <StartedWith run={run} />}
      restarts={logs.data?.restarts ?? 0}
      fold={{ open, onToggle: () => setOpen(!open) }}
    />
  )
}

// Failures whose fix is another size: their card offers Change size.
const SIZE_FIXES = new Set(['SizeTooBig', 'SizeUnknown'])

/** The app's size, and under it the sizes to move it to (SizeSetting). */
function SizeRow({ ws, managed, picked, setPicked, reveal }: { ws: Workspace; managed: boolean; picked: SizeChoice | null; setPicked: (s: SizeChoice | null) => void; reveal: number }) {
  const set = useSetSize(ws.name)
  return (
    <SizeSetting
      now={choiceOf(ws)}
      note={ws.phase === 'Stopped' ? 'The new size applies when the app starts.' : 'Saving restarts the app at the new size.'}
      save={set}
      canChange={!managed}
      picked={picked}
      setPicked={setPicked}
      reveal={reveal}
    />
  )
}

/** What the app runs beyond its template (image, port, command), and behind Change the fields to edit them. */
function LaunchRows({ ws, managed }: { ws: Workspace; managed: boolean }) {
  const loaded = useTemplate(ws.template)
  // Without the template list, show what the app has.
  const tpl = loaded ?? { userImage: !!ws.image, userCommand: ws.command ? ('Optional' as const) : ('None' as const), fromRepo: ws.source ? ('Optional' as const) : ('None' as const) }
  const set = useSetLaunch(ws.name)
  const [editing, setEditing] = useState<Launch | null>(null)
  if (!tpl.userImage && tpl.userCommand === 'None') return null
  const errs = set.isError ? fieldErrors(set.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = set.isError && Object.keys(errs.fields).every((f) => !['command', 'image', 'port'].includes(f)) ? problemText(set.error) : undefined
  return (
    <>
      {tpl.userImage && (
        <>
          <dt className="wide">
            <PartIcon part="image" />
            Image
          </dt>
          <dd className="wide">
            <CodeBlock text={ws.image ?? ''} label="Copy image" />
          </dd>
          <dt>
            <PartIcon part="start" />
            Port
          </dt>
          <dd>{ws.port}</dd>
        </>
      )}
      {tpl.userCommand !== 'None' && (
        <>
          <dt className={ws.command ? 'wide' : undefined}>
            <PartIcon part="start" />
            Command
          </dt>
          {ws.command ? (
            <dd className="wide">
              <CodeBlock text={ws.command} label="Copy command" />
            </dd>
          ) : (
            <dd className="muted">The image&apos;s own</dd>
          )}
        </>
      )}
      {!managed && (
        <dd className="size-change">
          {editing === null ? (
            <Button size="sm" onClick={() => setEditing({ command: ws.command, image: ws.image, port: ws.port ? String(ws.port) : '' })}>
              {tpl.userImage ? 'Change image' : 'Change command'}
            </Button>
          ) : (
            <div className="stack">
              <div>
                <LaunchFields tpl={tpl} value={editing} onChange={setEditing} errors={errs.fields} />
              </div>
              <p className="tight muted small">
                {ws.phase === 'Stopped' ? 'The change applies when the app starts.' : 'Saving restarts the app with the change.'}
              </p>
              {other && <span className="errmsg">{other}</span>}
              <div className="row">
                <Button size="sm" variant="primary" disabled={set.isPending} onClick={() => set.mutate(launchBody(tpl, editing), { onSuccess: () => setEditing(null) })}>
                  Save
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    set.reset()
                    setEditing(null)
                  }}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </dd>
      )}
    </>
  )
}

/** Whether the app can call the API as its owner, and behind Change the levels to move it to. */
function ApiAccessRow({ ws, managed }: { ws: Workspace; managed: boolean }) {
  const tpl = useTemplate(ws.template)
  const set = useSetApiAccess(ws.name)
  const [picked, setPicked] = useState<ApiAccess | null>(null)
  const err = set.isError ? (fieldErrors(set.error).fields.apiAccess ?? problemText(set.error)) : undefined
  const max = tpl?.maxApiAccess ?? ws.apiAccess
  return (
    <>
      <dt>
        <PartIcon part="apiAccess" />
        API access
      </dt>
      <dd>{apiAccessLabel(ws.apiAccess)}</dd>
      {max !== 'None' && !managed && (
        <dd className="size-change">
          {picked === null ? (
            <Button size="sm" onClick={() => setPicked(ws.apiAccess)}>
              Change API access
            </Button>
          ) : (
            <div className="stack">
              <ApiAccessChoices value={picked} max={max} onChange={setPicked} />
              <p className="tight muted small">
                {ws.phase === 'Stopped' ? 'The change applies when the app starts.' : 'Saving restarts the app with the change.'}
              </p>
              {err && <span className="errmsg">{err}</span>}
              <div className="row">
                <Button
                  size="sm"
                  variant="primary"
                  disabled={set.isPending || picked === ws.apiAccess}
                  onClick={() => set.mutate(picked, { onSuccess: () => setPicked(null) })}
                >
                  Save API access
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    set.reset()
                    setPicked(null)
                  }}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </dd>
      )}
    </>
  )
}

/** The app's environment variables, and behind Change the editor. Previews never get secrets. */
function EnvRow({ ws }: { ws: Workspace }) {
  const set = useSetAppEnv(ws.name)
  return (
    <EnvSetting
      now={ws.env ?? []}
      note={ws.phase === 'Stopped' ? 'The change applies when the app starts.' : 'Saving restarts the app with the change.'}
      hint={APP_ENV_HINT}
      save={set}
    />
  )
}

/** When the app stops for being idle, and behind Change the switch and times to move it to. */
function IdleStopRow({ ws, managed }: { ws: Workspace; managed: boolean }) {
  const policy = useIdleStopPolicy().data
  const set = useSetIdleStop(ws.name)
  const [picked, setPicked] = useState<number | null>(null)
  const err = set.isError ? (fieldErrors(set.error).fields.idleStopMinutes ?? problemText(set.error)) : undefined
  return (
    <>
      <dt>
        <PartIcon part="idle" />
        Stop when idle
      </dt>
      <dd>{idleStopLabel(ws.idleStopMinutes)}</dd>
      {policy && !managed && (
        <dd className="size-change">
          {picked === null ? (
            <Button size="sm" onClick={() => setPicked(ws.idleStopMinutes)}>
              Change when it stops
            </Button>
          ) : (
            <div className="stack">
              <IdleStopChoice policy={policy} value={picked} onChange={setPicked} error={err} />
              <p className="tight muted small">Saving takes effect at once; the app keeps running.</p>
              <div className="row">
                <Button
                  size="sm"
                  variant="primary"
                  disabled={set.isPending || picked === ws.idleStopMinutes}
                  onClick={() => set.mutate(picked, { onSuccess: () => setPicked(null) })}
                >
                  Save
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    set.reset()
                    setPicked(null)
                  }}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </dd>
      )}
    </>
  )
}

/** The folders that are rebuilt on every start and not saved, in the words of the storage notice. */
function ScratchFolders() {
  const storage = useStorage().data
  const paths = storage?.scratchPaths ?? []
  if (paths.length === 0) return null
  const note = storage?.notices?.find((n) => n.code === 'ScratchPaths')
  return (
    <More closed="Folders that are not saved" open="Hide">
      {note && <p className="tight muted small">{note.text}</p>}
      <ul className="plain">
        {paths.map((p) => (
          <li key={p} className="mono">
            {p}
          </li>
        ))}
      </ul>
    </More>
  )
}

/** On a preview: whose it is and which pull request it shows. */
function PreviewOfBanner({ of }: { of: NonNullable<Workspace['previewOf']> }) {
  const parent = useWorkspace(of.name).data
  return (
    <Banner
      tone="info"
      title={
        <>
          Preview of <Link to={`/apps/${encodeURIComponent(of.name)}`}>{parent?.displayName ?? of.name}</Link> for pull request #{of.pullNumber}
        </>
      }
    >
      It follows the pull request and goes away when it closes. Change its settings on the app it belongs to.
    </Banner>
  )
}

/** The image, environment and exact command line the app's pod runs. */
function WhatRuns({ run }: { run: PodRun }) {
  return (
    <Card className="pad">
      <div className="label">
        <Terminal size={15} aria-hidden="true" />
        What runs
      </div>
      <dl className="kv">
        <dt className="wide">
          <PartIcon part="image" />
          Image
        </dt>
        <dd className="wide">
          <CodeBlock text={run.image} label="Copy image" />
        </dd>
        <dt>
          <PartIcon part="environment" />
          Environment
        </dt>
        <dd className={run.environment ? undefined : 'muted'}>{run.environment || "The image's own"}</dd>
        <dt className={run.line ? 'wide' : undefined}>
          <PartIcon part="start" />
          Command
        </dt>
        {run.line ? (
          <dd className="wide">
            <CommandLine line={run.line} />
          </dd>
        ) : (
          <dd className="muted">The image&apos;s own</dd>
        )}
      </dl>
    </Card>
  )
}

function TemplateName({ id }: { id: string }) {
  return <>{useTemplateName(id)}</>
}

export function AppDetail() {
  const { name = '' } = useParams()
  const q = useWorkspace(name)
  const ws = q.data
  const [pickedSize, setPickedSize] = useState<SizeChoice | null>(null)
  const [revealSize, setRevealSize] = useState(0)
  const justStarted = useJustStarted(ws?.phase ?? '')
  const building = !!(useLocation().state as { building?: boolean } | null)?.building

  if (q.isError) {
    return (
      <Card className="empty">
        {problemText(q.error)} <Link to="/apps">Back to Apps</Link>
      </Card>
    )
  }
  if (!ws) return <div className="muted">Loading</div>

  const migrating = migratingStep(ws)
  const problem = migrating ?? problemStep(ws)
  const managed = !!ws.previewOf
  const stopped = migrating ? undefined : ws.phase === 'Stopped' ? ws.steps?.[0] : heldStep(ws)
  const readyLink = justStarted.started && ws.phase === 'Running' ? appLink(ws.url) : undefined
  const overview = (
    <>
      <AppApprovals name={ws.name} />
      <div className="two">
        <div className="stack">
          {readyLink && <ReadyCard ws={ws} link={readyLink} building={building} onDismiss={justStarted.dismiss} />}
          {ws.previewOf && <PreviewOfBanner of={ws.previewOf} />}
          {stopped && (
            <Banner tone="info" title={stopNote(ws) ?? stopped.title}>
              {stopped.what}
            </Banner>
          )}
          {problem && (
            <DiagnosedFailure
              name={ws.name}
              problem={problem}
              action={
                SIZE_FIXES.has(problem.reason) && (
                  <Button
                    size="sm"
                    onClick={() => {
                      setPickedSize(choiceOf(ws))
                      setRevealSize((n) => n + 1)
                    }}
                  >
                    Change size
                  </Button>
                )
              }
              log={problem.type === BUILD_STEP ? { title: 'Build output', lines: problem.evidence ?? [] } : undefined}
              tone={migrating ? 'info' : 'err'}
            />
          )}
          {migrating && <StepLog name={ws.name} step="HomeReady" following />}
          {!stopped && !migrating && (
            <Card className="pad">
              <div className="label">
                <Clock3 size={15} aria-hidden="true" />
                Progress
              </div>
              <Progress ws={ws} justStarted={justStarted.hold} />
            </Card>
          )}
          {!stopped && !migrating && <LogsCard name={ws.name} run={ws.run} />}
        </div>
        <div className="stack">
          <SourceCard ws={ws} />
          <Card className="pad">
            <div className="label">
              <FileText size={15} aria-hidden="true" />
              What you asked for
            </div>
            <dl className="kv">
              <dt>
                <PartIcon part="app" />
                Template
              </dt>
              <dd>
                <TemplateName id={ws.template} />
              </dd>
              <SizeRow ws={ws} managed={managed} picked={pickedSize} setPicked={setPickedSize} reveal={revealSize} />
              {ws.file && (
                <>
                  <dt className="wide">
                    <PartIcon part="code" />
                    File
                  </dt>
                  <dd className="wide">
                    <CodeBlock text={ws.filePath || ws.file} label="Copy path" path={ws.filePath} />
                  </dd>
                </>
              )}
              <LaunchRows ws={ws} managed={managed} />
              <EnvironmentRow env={ws.environment} />
              <ApiAccessRow ws={ws} managed={managed} />
              {!managed && <EnvRow ws={ws} />}
              <IdleStopRow ws={ws} managed={managed} />
            </dl>
            <ScratchFolders />
          </Card>
          {!managed && <TrustCard ws={ws} />}
          {ws.run && <WhatRuns run={ws.run} />}
          {ws.source?.previewsAvailable && !managed && <PreviewsCard ws={ws} />}
          {!managed && <ThumbnailCard ws={ws} />}
        </div>
      </div>
    </>
  )
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumbs">
        <Link to="/apps">Apps</Link> / {ws.displayName}
      </nav>
      <div className="dhead">
        <KindLogo kind={ws.kind} big />
        <div className="grow">
          <div className="row">
            <h1 className="flush">{ws.displayName}</h1>
            <Pill status={pillStatus(ws.phase)} />
            <Uptime ws={ws} />
          </div>
        </div>
        <Actions ws={ws} managed={managed} />
      </div>
      <PromptBlock prompt={ws.agentPrompt} />
      <TrustBanner ws={ws} />

      <div className="section">
        {ws.canShare && !managed ? (
          <Tabs
            label="App"
            tabs={[
              { id: 'overview', label: 'Overview', icon: 'overview', panel: overview },
              { id: 'sharing', label: 'Sharing', icon: 'sharing', panel: <SharingPanel ws={ws} /> },
            ]}
          />
        ) : (
          overview
        )}
      </div>
    </>
  )
}
