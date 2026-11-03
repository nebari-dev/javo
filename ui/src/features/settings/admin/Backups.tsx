import { Archive, DatabaseBackup as DbIcon, Eye, FolderInput, History, Play } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { Link } from 'react-router'
import type { BackupRunSummary, BackupStatus, BucketState, HeldRestore, HomeBackupItem } from '../../../api'
import { Banner } from '../../../components/Banner'
import { Button } from '../../../components/Button'
import { Card } from '../../../components/Card'
import { More } from '../../../components/More'
import { OutcomeList } from './OutcomeList'
import { PART_ICONS, PartTitle } from '../../../components/Section'
import { Select } from '../../../components/Select'
import { problemText } from '../../../lib/http'
import { bytes, count, when } from './format'
import {
  runGoing,
  useBackUpEverythingNow,
  useBackUpNow,
  useBackups,
  useRelease,
  useRestore,
  useRestoreNow,
  useRestorePreview,
  useRestores,
  useSetUpBackups,
} from './queries'

const RATE = 10

function Status({ n }: { n: BackupStatus }) {
  return (
    <Banner tone={n.problem ? 'err' : 'info'} title={n.title}>
      {n.what}
      {n.problem && n.todo ? ` ${n.todo}` : ''}
    </Banner>
  )
}

/** One restore's waiting apps and the two ways to let them go. */
function Waiting({ r }: { r: HeldRestore }) {
  const release = useRelease()
  const unscheduled = r.held - r.scheduled
  return (
    <li className="held-row">
      <div className="held-main">
        <span className="mono small">{r.name}</span>
        <span className="small muted">
          {count(r.held, 'app')} wait, {r.wasRunning} were running
          {r.scheduled > 0 ? `; ${r.scheduled} start soon` : ''}
        </span>
      </div>
      {unscheduled > 0 && (
        <div className="row">
          <Button size="sm" disabled={release.isPending} onClick={() => release.mutate({ name: r.name, mode: 'stopped' })}>
            Leave stopped
          </Button>
          <Button
            size="sm"
            variant="primary"
            icon={Play}
            disabled={release.isPending}
            onClick={() => release.mutate({ name: r.name, mode: 'start', perMinute: RATE })}
          >
            Start {RATE} per minute
          </Button>
        </div>
      )}
      {release.isError && <span className="errmsg">{problemText(release.error)}</span>}
    </li>
  )
}

/** The preview of one backup, and Restore for the apps that come back. */
function Preview({ name, onClose }: { name: string; onClose: () => void }) {
  const preview = useRestorePreview()
  const restore = useRestore()
  const { mutate } = preview
  useEffect(() => {
    mutate({ name, scope: {} })
  }, [mutate, name])
  const rows = restore.data ?? preview.data
  const coming = (preview.data ?? []).filter((r) => r.outcome === 'Create').length
  return (
    <Card className="pad section-sm">
      <PartTitle part="restore" as="h3" className="card-h">
        Restore from {name}
      </PartTitle>
      <p className="sub tight">Only apps that are gone now come back, and they wait until you start the restored apps. Nothing else changes.</p>
      {preview.isError && <Banner tone="err" title={problemText(preview.error)} />}
      {restore.isError && <Banner tone="err" title={problemText(restore.error)} />}
      {preview.isPending && <div className="muted">Reading the backup</div>}
      {rows && (
        <div className="section-sm">
          {rows.length === 0 ? (
            <p className="muted tight">This backup holds no apps.</p>
          ) : (
            <OutcomeList rows={rows} label="Restore preview" ahead={(n) => (restore.data ? `${count(n, 'app')} came back` : `${count(n, 'app')} come back`)} />
          )}
        </div>
      )}
      <div className="row section-sm">
        <Button variant="ghost" onClick={onClose}>
          {restore.data ? 'Done' : 'Cancel'}
        </Button>
        {!restore.data && (
          <Button variant="primary" icon={PART_ICONS.restore} disabled={coming === 0 || restore.isPending} onClick={() => restore.mutate({ name, scope: {} })}>
            Bring back {count(coming, 'app')}
          </Button>
        )}
      </div>
    </Card>
  )
}

/** restic's own lines, behind Show details. */
function Evidence({ lines }: { lines?: string[] | null }) {
  if (!lines || lines.length === 0) return null
  return (
    <More closed="Show details" open="Hide details">
      <pre className="evidence">{lines.join('\n')}</pre>
    </More>
  )
}

const MODES = [
  { mode: 'replace', title: 'Replace files', line: 'The home folder becomes as it was then. Files made since are removed.' },
  { mode: 'beside', title: 'Put beside current files', line: 'The files go into a restored-<date> folder in the home. Nothing else changes.' },
] as const

/** The job page that holds a run of the backup or restore job and its log. */
function jobPath(name: string): string {
  return `/jobs/${encodeURIComponent(name)}`
}

/** "the database, 2 home folders of people, 1 home volume with no account and 1 shared folder; 1 failed". */
function included(r: BackupRunSummary): string {
  const volumes = r.volumes ?? 0
  const failed = r.homesFailed + (r.volumesFailed ?? 0) + r.foldersFailed
  const parts =
    `${r.database ? 'the database, ' : ''}${count(r.homes, 'home folder of a person', 'home folders of people')}, ` +
    `${count(volumes, 'home volume with no account', 'home volumes with no account')} and ${count(r.folders, 'shared folder')}`
  return failed > 0 ? `${parts}; ${failed} failed` : parts
}

/** How a home or shared folder reads: who it belongs to, or that it has no account. */
function volumeName(v: HomeBackupItem, folder: boolean): string {
  if (folder) return v.person ? `Shared folder ${v.owner} (${v.person})` : `Shared folder ${v.owner}`
  return v.account ? v.person || v.owner : `Home volume with no account: ${v.claim || v.owner}`
}

/** The whole platform's backups to the bucket: one status, the newest run, Back up now. */
function FullBackup({ b }: { b: BucketState }) {
  const now = useBackUpEverythingNow()
  const setUp = useSetUpBackups()
  const job = b.jobs.backup
  const last = b.lastRun
  if (!job) {
    return (
      <>
        <Status n={b.status} />
        {setUp.isError && (
          <div className="section-sm">
            <Banner tone="err" title={problemText(setUp.error)} />
          </div>
        )}
        <p className="small muted section-sm tight">
          Backups go to {b.target.bucket} on {b.target.endpoint}: the database, every home folder and every shared folder, each night, as an ordinary job you can follow on the Jobs page.
        </p>
        <div className="row section-sm">
          <Button variant="primary" icon={Archive} disabled={setUp.isPending} onClick={() => setUp.mutate()}>
            Set up backups
          </Button>
        </div>
      </>
    )
  }
  return (
    <>
      <Status n={b.status} />
      {now.isError && (
        <div className="section-sm">
          <Banner tone="err" title={problemText(now.error)} />
        </div>
      )}
      {last && (
        <p className="small muted section-sm tight">
          {runGoing(last.outcome) ? 'Backing up since' : 'Last full backup'} {when(last.startedAt)}
          {!runGoing(last.outcome) && `: ${included(last)}`}. <Link to={jobPath(job.name)}>Open the run and its log</Link>
        </p>
      )}
      <p className="small muted section-sm tight">
        {job.schedule ? (
          <>
            Runs <span className="mono">{job.schedule}</span> ({job.timeZone}){job.paused ? ', paused' : ''}, the job <Link to={jobPath(job.name)}>{job.name}</Link>: change it on the Jobs page.
          </>
        ) : (
          <>
            The job <Link to={jobPath(job.name)}>{job.name}</Link> has no schedule: give it one on the Jobs page.
          </>
        )}{' '}
        Each home folder and shared folder keeps {b.keepDaily} daily, {b.keepWeekly} weekly and {b.keepMonthly} monthly backups in {b.target.bucket}.
      </p>
      <div className="row section-sm">
        <Button icon={Archive} disabled={now.isPending || runGoing(last?.outcome)} onClick={() => now.mutate()}>
          Back up now
        </Button>
        {now.data && <Link to={jobPath(now.data.job)}>Follow the backup</Link>}
      </div>
    </>
  )
}

/** Restore a full backup: pick a date, then everyone or one person. */
function RestoreFull({ b }: { b: BucketState }) {
  const restore = useRestoreNow()
  const runs = b.runs ?? []
  const homes = b.homes ?? []
  const [picked, setPicked] = useState('')
  const [person, setPerson] = useState('')
  const [mode, setMode] = useState<'replace' | 'beside'>('replace')
  const ids = { date: useId(), who: useId(), how: useId() }
  const from = picked || runs[0]?.run || ''
  const last = b.lastRestore
  if (!b.jobs.restore) return null
  return (
    <Card className="pad section-sm">
      <PartTitle part="restore" as="h3" className="card-h">
        Restore
      </PartTitle>
      {runs.length === 0 ? (
        <p className="muted tight">There is no full backup to restore yet.</p>
      ) : (
        <>
          <div className="field fit">
            <label htmlFor={ids.date}>Backup to restore</label>
            <Select id={ids.date} value={from} onValueChange={(v) => setPicked(v)}>
              {runs.map((r) => (
                <option key={r.run} value={r.run}>
                  {when(r.startedAt)}
                  {r.status.problem ? ', with problems' : ''}
                </option>
              ))}
            </Select>
          </div>
          <div className="field fit">
            <label htmlFor={ids.who}>Whose files</label>
            <Select id={ids.who} value={person} onValueChange={(v) => setPerson(v)}>
              <option value="">Everyone</option>
              {homes.map((h) => (
                <option key={h.id} value={h.owner}>
                  {volumeName(h, false)}
                </option>
              ))}
            </Select>
          </div>
          {person === '' ? (
            <p className="small muted tight">
              Every home folder becomes as it was then, and apps deleted since come back and wait for you under Restored apps. Each person's apps stop while their files come back.
            </p>
          ) : (
            <div className="field tight">
              <span id={ids.how} className="field-label">
                How the files come back
              </span>
              <div role="radiogroup" aria-labelledby={ids.how} className="grid choices">
                {MODES.map((m) => (
                  <label key={m.mode} className={`card choice${mode === m.mode ? ' sel' : ''}`}>
                    <input type="radio" name={`mode-${ids.how}`} className="sr-only" value={m.mode} checked={mode === m.mode} onChange={() => setMode(m.mode)} />
                    <span className="logo" aria-hidden="true">
                      {m.mode === 'replace' ? <History size={20} /> : <FolderInput size={20} />}
                    </span>
                    <span>
                      <h3>{m.title}</h3>
                      <span className="muted small block">{m.line}</span>
                    </span>
                  </label>
                ))}
              </div>
              <p className="small muted tight">{person}'s apps stop during the restore and start again after.</p>
            </div>
          )}
          {restore.isError && (
            <div className="section-sm">
              <Banner tone="err" title={problemText(restore.error)} />
            </div>
          )}
          <div className="row section-sm">
            <Button
              variant="primary"
              icon={PART_ICONS.restore}
              disabled={!from || restore.isPending}
              onClick={() => restore.mutate(person ? { from, person, mode } : { from })}
            >
              Restore
            </Button>
            {restore.data && <Link to={jobPath(restore.data.job)}>Follow the restore</Link>}
          </div>
        </>
      )}
      {last && (
        <p className={`small section-sm tight ${last.status.problem ? 'errmsg' : 'muted'}`}>
          Last restore {when(last.startedAt)}: {runGoing(last.outcome) ? 'running' : last.outcome === 'Succeeded' ? 'done' : 'with problems'}.{' '}
          <Link to={jobPath(b.jobs.restore.name)}>Open the run and its log</Link>
        </p>
      )}
    </Card>
  )
}

/** One home or shared folder: its newest backup. */
function VolumeRow({ v, folder }: { v: HomeBackupItem; folder: boolean }) {
  return (
    <li className="backup-row">
      <div className="backup-main">
        <span className="small volume-name">{volumeName(v, folder)}</span>
        <span className="mono small muted">{v.owner}</span>
        {v.status.problem ? (
          <span className="small errmsg">{v.status.title}</span>
        ) : (
          <span className="small muted">{v.lastAt && v.status.reason === 'HomeBackedUp' ? `Backed up ${when(v.lastAt)}` : v.status.title}</span>
        )}
        {v.status.problem && <Evidence lines={v.evidence} />}
      </div>
    </li>
  )
}

/** Every home folder and shared folder, problems first, behind Show. */
function Volumes({ b }: { b: BucketState }) {
  const all = [...(b.homes ?? []), ...(b.folders ?? [])]
  if (all.length === 0) return null
  return (
    <div className="section-sm">
      <More closed="Show each home folder and shared folder" open="Hide">
        <Card>
          <ul className="backup-list" aria-label="Each home folder and shared folder">
            {all.map((v, i) => (
              <VolumeRow key={v.id} v={v} folder={i >= (b.homes ?? []).length} />
            ))}
          </ul>
        </Card>
      </More>
    </div>
  )
}

/** Settings, Backups: database backups, home folder backups, restore. */
export function Backups() {
  const q = useBackups()
  const now = useBackUpNow()
  const restores = useRestores()
  const [previewing, setPreviewing] = useState<string | null>(null)
  const ids = { db: useId(), homes: useId(), held: useId(), full: useId() }
  if (q.isError) return <Banner tone="err" title={problemText(q.error)} />
  if (q.isPending) return <div className="muted">Loading</div>
  const { database: db, homeFolders, bucket } = q.data
  const items = db.items ?? []
  const waiting = restores.data ?? []
  return (
    <>
      {waiting.length > 0 && (
        <section className="section-sm" aria-labelledby={ids.held}>
          <PartTitle part="restore" id={ids.held}>
            Restored apps
          </PartTitle>
          <p className="sub tight">They wait so they do not all start at once and their files are back first.</p>
          <Card className="section-sm">
            <ul className="held-list">
              {waiting.map((r) => (
                <Waiting key={r.name} r={r} />
              ))}
            </ul>
          </Card>
        </section>
      )}
      {bucket && (
        <section className={waiting.length > 0 ? 'section' : 'section-sm'} aria-labelledby={ids.full}>
          <div className="section-head">
            <PartTitle part="everything" id={ids.full}>
              Everything
            </PartTitle>
          </div>
          <FullBackup b={bucket} />
          <RestoreFull b={bucket} />
          <Volumes b={bucket} />
        </section>
      )}
      <section className={waiting.length > 0 || bucket ? 'section' : 'section-sm'} aria-labelledby={ids.db}>
        <div className="section-head">
          <PartTitle part="database" id={ids.db}>
            Database
          </PartTitle>
          {db.canBackUpNow && !bucket && (
            <div className="right">
              <Button icon={DbIcon} disabled={now.isPending} onClick={() => now.mutate()}>
                Back up now
              </Button>
            </div>
          )}
        </div>
        <Status n={db.status} />
        {now.isError && (
          <div className="section-sm">
            <Banner tone="err" title={problemText(now.error)} />
          </div>
        )}
        {db.schedule && (
          <p className="small muted section-sm tight">
            Every <span className="mono">{db.schedule}</span>, keeping the newest {db.keep}. Next {when(db.nextAt)}.
          </p>
        )}
        {items.length > 0 && (
          <Card className="section-sm">
            <ul className="backup-list" aria-label="Database backups">
              {items.map((b) => (
                <li key={b.name} className="backup-row">
                  <div className="backup-main">
                    <span className="mono small">{b.name}</span>
                    <span className="small muted">
                      {when(b.takenAt)}, {bytes(b.size)}
                    </span>
                  </div>
                  <Button size="sm" icon={Eye} aria-label={`Preview restore from ${b.name}`} onClick={() => setPreviewing(b.name)}>
                    Preview restore
                  </Button>
                </li>
              ))}
            </ul>
          </Card>
        )}
        {previewing && <Preview key={previewing} name={previewing} onClose={() => setPreviewing(null)} />}
      </section>
      {homeFolders && (
        <section className="section" aria-labelledby={ids.homes}>
          <PartTitle part="homes" id={ids.homes}>
            Home folders
          </PartTitle>
          <Status n={homeFolders} />
        </section>
      )}
    </>
  )
}
