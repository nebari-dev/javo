import { Pause, Pencil, Play, RotateCcw, ScrollText, Square } from 'lucide-react'
import { useId, useState } from 'react'
import { Link, useLocation, useParams } from 'react-router'
import type { JobDetail as JobDetailData, RunView } from '../../api'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { EnvironmentRow } from '../environments/EnvironmentCard'
import { CodeBlock } from '../../components/CodeBlock'
import { FailureCard } from '../logs/FailureCard'
import { KindLogo } from '../../components/KindLogo'
import { More } from '../../components/More'
import { Pill } from '../../components/Pill'
import { PartIcon } from '../../components/Section'
import { JOB_ENV_HINT } from '../environments/EnvEditor'
import { EnvSetting } from '../environments/EnvSetting'
import { choiceOf, SizeSetting } from '../sizes/SizeSetting'
import { openFileUrl } from '../../lib/files'
import { problemText } from '../../lib/http'
import { cronText, duration, engineKind, outcomeLabel, outcomeStatus, when } from './format'
import { NextRun } from './JobsList'
import { isRunning, useEngines, useJob, useJobActions, useRunJob, useSetJobEnv, useSetJobSize } from './queries'
import { RunLog } from './RunLog'
import type { SizeChoice } from '../../lib/sizes'

const basename = (p: string) => p.split('/').pop() ?? p
// Engines whose HTML output is the executed notebook.
const NOTEBOOK_ENGINES = ['papermill', 'marimo']
// Problems whose fix is another size: their card offers Change size.
const SIZE_FIXES = new Set(['JobSizeUnknown', 'RunOutOfMemory'])

/** What the job page is told when it is opened right after saving. */
export interface JobPageState {
  /** Why the first run of a new job could not start. */
  runError?: string
}

function RunRow({ job, run, logOpen, onLog }: { job: JobDetailData; run: RunView; logOpen: boolean; onLog: () => void }) {
  const { cancel } = useJobActions(job.name)
  const zone = job.schedule?.timeZone
  const running = isRunning(run)
  const logId = useId()
  return (
    <>
      <tr className={logOpen ? 'open' : undefined}>
        <th scope="row">
          <span className="block run-when">{when(run.startTime ?? run.scheduledTime, zone) || 'Waiting to start'}</span>
          <span className="block muted small">{run.trigger === 'manual' ? 'Started by hand' : 'On schedule'}</span>
        </th>
        <td>
          <Pill status={outcomeStatus(run.outcome)} label={outcomeLabel(run.outcome)} />
          {run.title && run.outcome !== 'Succeeded' && <span className="block muted small">{run.title}</span>}
        </td>
        <td className="col-file">{duration(run)}</td>
        <td className="col-type">
          {(run.outputs ?? []).map((o) => (
            <a key={o} className="block small" href={openFileUrl(o)} target="_blank" rel="noopener">
              {basename(o)}
            </a>
          ))}
        </td>
        <td className="col-act">
          <span className="btn-row">
            {running && (
              <Button size="sm" icon={Square} disabled={cancel.isPending} onClick={() => cancel.mutate(run.id)}>
                <span className="atable-act-label">Cancel</span>
              </Button>
            )}
            <Button size="sm" icon={ScrollText} className="log-toggle" aria-expanded={logOpen} aria-controls={logOpen ? logId : undefined} onClick={onLog}>
              <span className="atable-act-label">Log</span>
            </Button>
          </span>
          {cancel.isError && <span className="errmsg block">{problemText(cancel.error)}</span>}
        </td>
      </tr>
      {logOpen && (
        <tr className="run-log-row" id={logId}>
          <td colSpan={5}>
            <RunLog job={job.name} run={run.id} running={running} />
          </td>
        </tr>
      )}
    </>
  )
}

/** The size being chosen on the page (null while closed), and how often something else opened it. */
interface SizePick {
  picked: SizeChoice | null
  setPicked: (s: SizeChoice | null) => void
  reveal: number
}

function Details({ job, size }: { job: JobDetailData; size: SizePick }) {
  const engines = useEngines()
  const setEnv = useSetJobEnv(job.name)
  const setSize = useSetJobSize(job.name)
  const engine = engines.data?.find((e) => e.id === job.engine)?.displayName ?? job.engine
  const s = job.schedule
  return (
    <Card className="pad">
      <div className="label">Details</div>
      <dl className="kv">
        {job.command ? (
          <>
            <dt className="wide">
              <PartIcon part="code" />
              Command
            </dt>
            <dd className="wide">
              <CodeBlock text={job.command} label="Copy command" />
            </dd>
          </>
        ) : (
          <>
            <dt className="wide">
              <PartIcon part="code" />
              Source
            </dt>
            <dd className="wide">
              <CodeBlock text={job.source} label="Copy path" />
            </dd>
          </>
        )}
        <dt>
              <PartIcon part="engine" />
              Runs with
            </dt>
        <dd>{engine}</dd>
        <EnvironmentRow env={job.environment} />
        <dt>
              <PartIcon part="schedule" />
              Schedule
            </dt>
        <dd>{s ? `${cronText(s.cron)} (${s.timeZone})` : 'Run on demand'}</dd>
        <SizeSetting
          now={choiceOf(job)}
          note="The next run gets the new size; a run already going keeps its size."
          save={setSize}
          canChange
          {...size}
        />
        <EnvSetting
          now={job.env ?? []}
          note="The next run gets the change; a run already going keeps what it started with."
          hint={JOB_ENV_HINT}
          save={setEnv}
        />
      </dl>
      <More closed="Retries, time limit and notifications" open="Hide retries, time limit and notifications">
        <dl className="kv">
          {job.kernel && (
            <>
              <dt>
              <PartIcon part="kernel" />
              Kernel
            </dt>
              <dd>{job.kernel}</dd>
            </>
          )}
          {s && (
            <>
              <dt>
              <PartIcon part="overlap" />
              If a run is still going
            </dt>
              <dd>{s.concurrency}</dd>
            </>
          )}
          <dt>
              <PartIcon part="retries" />
              Retries
            </dt>
          <dd>{job.run.retries ?? 0}</dd>
          {job.run.timeout && (
            <>
              <dt>
              <PartIcon part="timeLimit" />
              Time limit
            </dt>
              <dd>{job.run.timeout}</dd>
            </>
          )}
          <dt>
              <PartIcon part="notify" />
              Notify
            </dt>
          <dd>{[job.notify.onFailure && 'on failure', job.notify.onSuccess && 'on success'].filter(Boolean).join(', ') || 'never'}</dd>
        </dl>
      </More>
    </Card>
  )
}

export function JobDetail() {
  const { name = '' } = useParams()
  const q = useJob(name)
  const { setPaused } = useJobActions(name)
  const run = useRunJob()
  const startError = (useLocation().state as JobPageState | null)?.runError
  const [openLog, setOpenLog] = useState<string | null>(null)
  // The run whose log opened by itself while it went; it stays open once the run ends.
  const [followed, setFollowed] = useState<string | null>(null)
  const [confirmResume, setConfirmResume] = useState(false)
  const [pickedSize, setPickedSize] = useState<SizeChoice | null>(null)
  const [revealSize, setRevealSize] = useState(0)
  const job = q.data

  if (q.isError) {
    return (
      <Card className="empty">
        {problemText(q.error)} <Link to="/jobs">Back to Jobs</Link>
      </Card>
    )
  }
  if (!job) return <div className="muted">Loading</div>

  const runs = job.runs ?? []
  const going = runs.find(isRunning)
  if (going && going.id !== followed) setFollowed(going.id)
  const shownLog = openLog ?? going?.id ?? followed
  const last = job.lastRun
  // The gateway lists the HTML first; it opens in the browser, while a notebook downloads.
  const output = last?.outputs?.[0]
  const notebookHtml = !!output?.endsWith('.html') && NOTEBOOK_ENGINES.includes(job.engine)
  // A broken definition (Invalid, Blocked) has its own problem; the last run's text and actions belong only to a problem that came from that run.
  const fromLastRun = !!last && job.problem?.reason === last.reason
  // A run that kept no inputs cannot be repeated for a job that has some: an empty body would run the saved defaults.
  const inputsLost = (job.parameters?.length ?? 0) > 0 && !last?.parameters
  const paused = job.schedule?.paused ?? false
  const resume = () => {
    setConfirmResume(false)
    setPaused.mutate(false)
  }
  const err = [run.error, setPaused.error].find(Boolean)
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumbs">
        <Link to="/jobs">Jobs</Link> / {job.displayName}
      </nav>
      <div className="dhead">
        <KindLogo kind={engineKind(job.engine)} big />
        <div className="grow">
          <div className="row">
            <h1 className="flush">{job.displayName}</h1>
            {last && <Pill status={outcomeStatus(last.outcome)} label={outcomeLabel(last.outcome)} />}
          </div>
          <div className="urlbar muted small">
            Next: <NextRun job={job} />
          </div>
        </div>
        <div className="actions-col">
          <div className="actions">
            <Button variant="primary" icon={Play} disabled={run.isPending} onClick={() => run.mutate({ name })}>
              Run now
            </Button>
            {job.schedule && (
              <Button icon={paused ? Play : Pause} disabled={setPaused.isPending} onClick={() => (!paused ? setPaused.mutate(true) : job.resumeNotice ? setConfirmResume(true) : resume())}>
                {paused ? 'Resume schedule' : 'Pause schedule'}
              </Button>
            )}
            <Link className="btn" to={`/jobs/${encodeURIComponent(job.name)}/edit`}>
              <Pencil size={16} aria-hidden="true" />
              Edit
            </Link>
          </div>
          {err && <span className="errmsg">{problemText(err)}</span>}
        </div>
      </div>

      <div className="stack section">
        {startError && <Banner tone="err" title="The first run did not start">{startError}</Banner>}
        {paused && confirmResume && job.resumeNotice && (
          <Banner
            tone="info"
            title={job.resumeNotice}
            actions={
              <>
                <Button size="sm" variant="primary" disabled={setPaused.isPending} onClick={resume}>
                  Resume anyway
                </Button>
                <Button size="sm" onClick={() => setConfirmResume(false)}>
                  Cancel
                </Button>
              </>
            }
          />
        )}
        {job.problem && (
          <FailureCard
            title={job.problem.title}
            what={
              <>
                {job.problem.what}
                {fromLastRun && last?.message && <pre className="evidence run-message">{last.message}</pre>}
              </>
            }
            todo={
              <>
                {job.problem.todo}
                {SIZE_FIXES.has(job.problem.reason) && (
                  <div className="row wrap fail-actions">
                    <Button
                      size="sm"
                      onClick={() => {
                        setPickedSize(choiceOf(job))
                        setRevealSize((n) => n + 1)
                      }}
                    >
                      Change size
                    </Button>
                  </div>
                )}
                {fromLastRun && (
                  <div className="row wrap fail-actions">
                    <Button
                      size="sm"
                      icon={RotateCcw}
                      disabled={run.isPending || inputsLost}
                      onClick={() => run.mutate({ name, parameters: last?.parameters })}
                    >
                      Re-run with same inputs
                    </Button>
                    {inputsLost && <span className="muted">This run did not keep its inputs.</span>}
                    {output && (
                      <a className="btn sm" href={openFileUrl(output)} target="_blank" rel="noopener">
                        {notebookHtml ? 'Open executed notebook' : 'Open output'}
                      </a>
                    )}
                  </div>
                )}
              </>
            }
            blame={job.problem.blame}
            who={job.problem.who}
            evidence={job.problem.evidence ?? []}
          />
        )}
        <div className="two">
          <div className="card atable-wrap">
            <table className="atable">
              <caption className="sr-only">Runs of {job.displayName}</caption>
              <thead>
                <tr>
                  <th scope="col">Started</th>
                  <th scope="col">Outcome</th>
                  <th scope="col" className="col-file">
                    Took
                  </th>
                  <th scope="col" className="col-type">
                    Output
                  </th>
                  <th scope="col" className="col-act">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {runs.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="muted">
                      No runs yet.
                    </td>
                  </tr>
                ) : (
                  runs.map((r) => (
                    <RunRow
                      key={r.id}
                      job={job}
                      run={r}
                      logOpen={shownLog === r.id}
                      onLog={() => setOpenLog(shownLog === r.id ? '' : r.id)}
                    />
                  ))
                )}
              </tbody>
            </table>
          </div>
          <Details job={job} size={{ picked: pickedSize, setPicked: setPickedSize, reveal: revealSize }} />
        </div>
      </div>
    </>
  )
}
