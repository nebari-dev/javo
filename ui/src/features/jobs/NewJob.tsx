import { File as FileIcon, Plus, X } from 'lucide-react'
import { useId, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import type { JobBody, JobDetail, JobParam, Template } from '../../api'
import { Banner } from '../../components/Banner'
import { SearchSelect } from '../../components/SearchSelect'
import { Select } from '../../components/Select'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { HomePicker } from '../files/HomePicker'
import { Field } from '../apps/LaunchFields'
import { More } from '../../components/More'
import { EnvEditor, envBody, JOB_ENV_HINT, type EnvRow } from '../environments/EnvEditor'
import { EnvironmentCard } from '../environments/EnvironmentCard'
import { PartTitle, Section } from '../../components/Section'
import { Segmented } from '../../components/Segmented'
import { SizeCard } from '../sizes/SizePicker'
import { fieldErrors, problemText } from '../../lib/http'
import { firstFreeName } from '../../lib/names'
import { useCustomAllowance, useSecrets, useSizes, useTemplates, useWarmHome } from '../../lib/queries'
import { choiceReady, startingChoice, type SizeChoice } from '../../lib/sizes'
import { useStartFirstRun } from './firstRun'
import { engineForFile, templateEngines } from './format'
import { useEngines, useJob, useJobs, useSaveJob, useSchedulePreview } from './queries'
import { fromCron, toCron, type Schedule } from './schedule'
import { ParamHelp } from './paramHelp'
import { ScheduleBuilder } from './ScheduleBuilder'
import { SchedulePreview } from './SchedulePreview'

// Request fields the form has a place for; errors on any other field go to the top banner.
const FORM_FIELDS = new Set(['displayName', 'template', 'source', 'command', 'kernel', 'size', 'resources', 'schedule.cron', 'schedule.timeZone', 'run.timeout', 'run.retries', 'environment'])
// Errors on the job's variables (env, env[0].name, ...) show on the editor.
const isFormField = (f: string) => FORM_FIELDS.has(f) || f === 'env' || f.startsWith('env[')
const PARAM_TYPES: JobParam['type'][] = ['string', 'integer', 'number', 'boolean']
/** The engines whose jobs are typed text instead of a file: a command line, or a script bash reads. */
const COMMAND = 'command'
const BASH = 'bash'
const typed = (engine?: string) => engine === COMMAND || engine === BASH

/**
 * Zones IANA renamed that browsers still list under the old name (ICU keeps
 * old ids stable). Kubernetes accepts both; people look for the current one.
 */
const RENAMED: Record<string, string> = {
  'Africa/Asmera': 'Africa/Asmara',
  'America/Godthab': 'America/Nuuk',
  'Asia/Calcutta': 'Asia/Kolkata',
  'Asia/Katmandu': 'Asia/Kathmandu',
  'Asia/Rangoon': 'Asia/Yangon',
  'Asia/Saigon': 'Asia/Ho_Chi_Minh',
  'Atlantic/Faeroe': 'Atlantic/Faroe',
  'Europe/Kiev': 'Europe/Kyiv',
}

/** Every IANA zone the browser knows under its current name, plus UTC and the current value, sorted. */
function zoneChoices(current: string): string[] {
  const all = new Set(Intl.supportedValuesOf('timeZone').map((z) => RENAMED[z] ?? z))
  all.add('UTC')
  if (current) all.add(current)
  return [...all].sort()
}

const localZone = (): string => {
  try {
    const z = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
    return RENAMED[z] ?? z
  } catch {
    return 'UTC'
  }
}

const stem = (p: string): string => (p.split('/').pop() ?? p).replace(/\.[^.]*$/, '')

function FieldError({ id, msg }: { id: string; msg?: string }) {
  return msg ? (
    <span id={id} className="errmsg">
      {msg}
    </span>
  ) : null
}

interface FormProps {
  /** The job being edited; absent when making a new one. */
  job?: JobDetail
  /** The template a new job starts on, for its sample job; absent for the first template. */
  sampleOf?: Template
  /** The names of the person's jobs, which a sample's name is numbered past. */
  jobNames?: string[]
  templates: Template[]
}

function JobForm({ job, sampleOf, jobNames = [], templates }: FormProps) {
  const navigate = useNavigate()
  const engines = useEngines().data ?? []
  const save = useSaveJob(job?.name)
  const startFirstRun = useStartFirstRun()
  const ids = { name: useId(), nameErr: useId(), cron: useId(), zone: useId(), kernel: useId(), tpl: useId() }

  const [templateName, setTemplateName] = useState(job?.template ?? sampleOf?.name ?? templates[0]?.name ?? '')
  // A new job starts from its template's sample job, when it has one: the
  // fields below are null until the person changes them, and the sample's
  // values show meanwhile.
  const sample = job ? undefined : templates.find((t) => t.name === templateName)?.jobs?.sample
  const [typedName, setTypedName] = useState<string | null>(job?.displayName ?? null)
  const [source, setSource] = useState(job?.source ?? '')
  const [pickedWhat, setWhat] = useState<'file' | 'command' | null>(job ? (typed(job.engine) ? 'command' : 'file') : null)
  const what = pickedWhat ?? (sample ? 'command' : 'file')
  const [typedCommand, setCommand] = useState<string | null>(job?.command ?? null)
  const command = typedCommand ?? sample?.command ?? ''
  const [picking, setPicking] = useState(false)
  const [kernel, setKernel] = useState(job?.kernel ?? '')
  const [typedParams, setParams] = useState<JobParam[] | null>(job?.parameters ?? null)
  const params = typedParams ?? (what === 'command' ? sample?.parameters : undefined) ?? []
  // Only a new job takes variables here; the job page changes them.
  const [envRows, setEnvRows] = useState<EnvRow[]>([])
  // The id of the picked environment; empty for none. Set only on create.
  const [environment, setEnvironment] = useState('')
  const mySecrets = useSecrets({ enabled: !job }).data ?? []
  // Only a new job picks its size here, starting at the default; the job page changes it.
  const sizes = useSizes().data ?? []
  const allowance = useCustomAllowance()
  const [pickedSize, setPickedSize] = useState<SizeChoice | null>(null)
  const size: SizeChoice = pickedSize ?? startingChoice(templates.find((t) => t.name === templateName)?.defaultResources, sizes, allowance)
  const [mode, setMode] = useState<'once' | 'schedule'>(job ? (job.schedule ? 'schedule' : 'once') : 'once')
  const [sched, setSched] = useState<Schedule>(job?.schedule ? fromCron(job.schedule.cron) : { kind: 'daily', hour: 9, minute: 0 })
  const cron = toCron(sched)
  const [zone, setZone] = useState(job?.schedule?.timeZone ?? localZone())
  const [overlap, setOverlap] = useState<'Allow' | 'Forbid' | 'Replace'>(job?.schedule?.concurrency ?? 'Forbid')
  const [retries, setRetries] = useState(String(job?.run.retries ?? 0))
  const [timeout, setTimeoutText] = useState(job?.run.timeout ?? '')
  // A built schedule is previewed as it changes; a custom expression once its field is left.
  const [customChecked, setCustomChecked] = useState(sched.kind === 'custom' ? cron : '')
  const previewCron = sched.kind === 'custom' ? customChecked : cron

  const tpl = templates.find((t) => t.name === templateName)
  const offered = templateEngines(tpl, engines)
  const extensions = offered.flatMap((e) => e.extensions ?? [])
  const commandEngine = offered.find((e) => typed(e.id))
  // File and Command are offered when the template runs both; otherwise it is whichever it runs.
  const runs = !commandEngine ? 'file' : extensions.length === 0 ? 'command' : what
  const engine = runs === 'command' ? commandEngine : source ? engineForFile(source, offered) : undefined
  const kernels = tpl?.jobs?.kernels ?? []
  const wantsKernel = engine?.id === 'papermill'
  const kernelValue = wantsKernel ? (kernel || kernels[0] || '') : ''
  const name =
    typedName ?? (runs === 'file' && source ? stem(source) : runs === 'command' && sample ? firstFreeName(sample.title, jobNames) : '')

  const preview = useSchedulePreview(mode === 'schedule' ? previewCron : '', zone)
  const previewErrs = preview.isError ? fieldErrors(preview.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const errs = save.isError ? fieldErrors(save.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !isFormField(f)).map(([, m]) => m)]
  const cronErr = previewErrs.fields.cron ?? errs.fields['schedule.cron']
  const zoneErr = previewErrs.fields.timeZone ?? errs.fields['schedule.timeZone']

  const missing = !tpl
    ? 'No template can run jobs'
    : runs === 'command' && !command.trim()
      ? 'Type the command to run'
      : runs === 'file' && !source
        ? 'Choose the file to run'
        : !engine
          ? 'No engine of this template runs that file'
          : !name.trim()
            ? 'Give it a name'
            : mode === 'schedule' && sched.kind === 'weekly' && sched.days.length === 0
              ? 'Pick at least one day'
              : mode === 'schedule' && !cron
                ? 'Write the schedule'
                : ''

  const body = (): JobBody | undefined => {
    if (!tpl || !engine) return undefined
    const named = params.filter((p) => p.name.trim()).map((p) => ({ ...p, name: p.name.trim() }))
    const env = envBody(envRows)
    return {
      displayName: name.trim(),
      template: tpl.name,
      engine: engine.id,
      ...(runs === 'command' ? { command: command.trim() } : { source }),
      ...(kernelValue ? { kernel: kernelValue } : {}),
      ...(named.length > 0 ? { parameters: named } : {}),
      ...(mode === 'schedule' ? { schedule: { cron, timeZone: zone, concurrency: overlap } } : {}),
      run: { retries: Number(retries) || 0, ...(timeout.trim() ? { timeout: timeout.trim() } : {}) },
      ...(!job && env.length > 0 ? { env } : {}),
      ...(!job && environment ? { environment: { id: environment } } : {}),
      ...(job ? {} : 'resources' in size ? { resources: size.resources } : size.size ? { size: size.size } : {}),
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const b = body()
    if (missing || !b || (!job && !choiceReady(allowance, size))) return
    save.mutate(b, {
      onSuccess: (saved) => {
        // "Run once" starts the first run straight away.
        if (!job && mode === 'once') startFirstRun(saved.name)
        else navigate(`/jobs/${encodeURIComponent(saved.name)}`)
      },
    })
  }

  const setParam = (i: number, patch: Partial<JobParam>) => setParams(params.map((p, j) => (j === i ? { ...p, ...patch } : p)))

  return (
    <form onSubmit={submit} noValidate>
      <h1>{job ? `Edit ${job.displayName}` : 'New job'}</h1>
      <p className="sub">Pick a file or a command and when it should run. You can change everything later.</p>
      {other.length > 0 && (
        <div className="section-sm">
          <Banner tone="err" title="Nothing was saved">
            {other.join(' ')}
          </Banner>
        </div>
      )}
      <div className="two section">
        <div className="stack">
          <Section part="code" title="Code">
          <Card className="pad">
            <h2 className="card-h">What should run?</h2>
            {templates.length > 1 && (
              <div className="field">
                <label htmlFor={ids.tpl}>Template</label>
                <Select
                  id={ids.tpl}
                  value={templateName}
                  onValueChange={(v) => {
                    setTemplateName(v)
                    setSource('')
                    setKernel('')
                  }}
                >
                  {templates.map((t) => (
                    <option key={t.name} value={t.name}>
                      {t.displayName}
                    </option>
                  ))}
                </Select>
              </div>
            )}
            {commandEngine && extensions.length > 0 && (
              <div className="field fit">
                <Segmented
                  label="What runs"
                  value={what}
                  options={[
                    { value: 'file', label: 'File' },
                    { value: 'command', label: 'Command' },
                  ]}
                  onChange={setWhat}
                />
              </div>
            )}
            {runs === 'command' ? (
              commandEngine?.id === BASH ? (
                <Field
                  label="Command"
                  mono
                  multiline
                  value={command}
                  onChange={setCommand}
                  placeholder={'echo "Hello from $(hostname)"'}
                  hint="A command or a script of several lines, run by bash in your home folder. Each parameter arrives as $JAVO_PARAM_<NAME>."
                  error={errs.fields.command}
                />
              ) : (
                <Field
                  label="Command"
                  mono
                  value={command}
                  onChange={setCommand}
                  placeholder="python etl.py --region emea"
                  hint="Runs in your home folder with this template's image, without a shell. Each parameter arrives as the environment variable JAVO_PARAM_<NAME>; {{.Home}} is filled in."
                  error={errs.fields.command}
                />
              )
            ) : (
              <div className="field">
                <span className="field-label">File</span>
                {source ? (
                  <div className={`filechip${errs.fields.source ? ' err' : ''}`}>
                    <FileIcon size={20} aria-hidden="true" className="fi" />
                    <span className="mono grow">{source}</span>
                    <Button size="sm" onClick={() => setPicking(true)}>
                      Change
                    </Button>
                    <Button size="sm" variant="ghost" icon={X} aria-label="Clear file" onClick={() => setSource('')} />
                  </div>
                ) : (
                  <button type="button" className="pickbtn" onClick={() => setPicking(true)} disabled={!tpl}>
                    <FileIcon size={20} aria-hidden="true" className="fi" />
                    <span className="grow">
                      <b>Choose a file from your home folder</b>
                      <span className="muted small block">{extensions.length > 0 ? `Runs ${extensions.join(', ')} files` : ' '}</span>
                    </span>
                    <span className="btn sm" aria-hidden="true">
                      Browse
                    </span>
                  </button>
                )}
                {engine && (
                  <span className="muted small">
                    Runs with <b>{engine.displayName}</b>
                  </span>
                )}
                <FieldError id={`${ids.nameErr}-src`} msg={errs.fields.source} />
              </div>
            )}
            {wantsKernel && (
              <div className="field">
                <label htmlFor={ids.kernel}>Kernel</label>
                <Select id={ids.kernel} value={kernelValue} onValueChange={(v) => setKernel(v)}>
                  {kernels.map((k) => (
                    <option key={k} value={k}>
                      {k}
                    </option>
                  ))}
                </Select>
                <FieldError id={`${ids.kernel}-err`} msg={errs.fields.kernel} />
              </div>
            )}
          </Card>
          </Section>

          <Section part="parameters" title="Parameters">
          <Card className="pad">
            <ParamHelp engine={engine?.id} param={params.map((p) => ({ ...p, name: p.name.trim() })).find((p) => p.name) ?? { name: 'region', type: 'string', default: 'emea' }} />
            {params.length > 0 && (
              <div className="param-head" aria-hidden="true">
                <span>Name</span>
                <span>Type</span>
                <span>Default value</span>
              </div>
            )}
            {params.map((p, i) => (
              <div key={i} className="param-row">
                <input className="input" aria-label={`Parameter ${i + 1} name`} placeholder="name" value={p.name} onChange={(e) => setParam(i, { name: e.target.value })} />
                <Select aria-label={`Parameter ${i + 1} type`} value={p.type} onValueChange={(v) => setParam(i, { type: v as JobParam['type'] })}>
                  {PARAM_TYPES.map((t) => (
                    <option key={t}>{t}</option>
                  ))}
                </Select>
                <input className="input" aria-label={`Parameter ${i + 1} value`} placeholder="value" value={p.default ?? ''} onChange={(e) => setParam(i, { default: e.target.value })} />
                <Button size="sm" variant="ghost" icon={X} aria-label={`Remove parameter ${i + 1}`} onClick={() => setParams(params.filter((_, j) => j !== i))} />
              </div>
            ))}
            <Button size="sm" icon={Plus} onClick={() => setParams([...params, { name: '', type: 'string', default: '' }])}>
              Add a parameter
            </Button>
          </Card>
          </Section>

          {!job && (
            <Section part="variables" title="Environment variables">
            <Card className="pad">
              <EnvEditor rows={envRows} available={mySecrets.map((s) => s.name)} errors={errs.fields} onChange={setEnvRows} hint={JOB_ENV_HINT} />
            </Card>
            </Section>
          )}

          {!job && <EnvironmentCard what="job" value={environment} onChange={setEnvironment} error={errs.fields.environment} />}

          {!job && sizes.length > 0 && (
            <SizeCard sizes={sizes} custom={allowance} value={size} onChange={setPickedSize} errors={errs.fields} />
          )}

          <Section part="schedule" title="Schedule">
          <Card className="pad">
            <h2 className="card-h">When should it run?</h2>
            <div role="radiogroup" aria-label="When should it run?" className="stack tight-gap">
              <label className={`card choice${mode === 'once' ? ' sel' : ''}`}>
                <input type="radio" name="mode" className="sr-only" checked={mode === 'once'} onChange={() => setMode('once')} />
                <span>
                  <h3>Run once</h3>
                  <span className="muted small block">Starts right after you create it. Run it again whenever you like.</span>
                </span>
              </label>
              <label className={`card choice${mode === 'schedule' ? ' sel' : ''}`}>
                <input type="radio" name="mode" className="sr-only" checked={mode === 'schedule'} onChange={() => setMode('schedule')} />
                <span>
                  <h3>On a schedule</h3>
                  <span className="muted small block">Runs by itself at the times you set.</span>
                </span>
              </label>
            </div>
            {mode === 'schedule' && (
              <div className="schedule">
                <ScheduleBuilder
                  value={sched}
                  onChange={(next, typed) => {
                    setSched(next)
                    // Choosing Custom previews what the builder wrote; a typed expression waits until the field is left.
                    if (!typed && next.kind === 'custom' && sched.kind !== 'custom') setCustomChecked(toCron(next))
                  }}
                  cronError={cronErr}
                  onCronBlur={() => setCustomChecked(cron)}
                />
                <div className="field">
                  <SearchSelect
                    id={ids.zone}
                    label="Time zone"
                    options={zoneChoices(zone)}
                    value={zone}
                    onChange={setZone}
                    invalid={!!zoneErr}
                    describedBy={zoneErr ? `${ids.zone}-err` : undefined}
                  />
                  <FieldError id={`${ids.zone}-err`} msg={zoneErr} />
                </div>
                {preview.data && !cronErr && !zoneErr && (
                  <SchedulePreview preview={{ next: preview.data.next ?? [], warnings: preview.data.warnings ?? [] }} timeZone={zone} onUseUtc={() => setZone('UTC')} />
                )}
              </div>
            )}
            <More
              closed={mode === 'schedule' ? 'Overlap rule, retries and time limit' : 'Retries and time limit'}
              open={mode === 'schedule' ? 'Hide overlap rule, retries and time limit' : 'Hide retries and time limit'}
            >
              {mode === 'schedule' && (
                <div className="field">
                  <label htmlFor={`${ids.cron}-overlap`}>If a run is still going when the next is due</label>
                  <Select id={`${ids.cron}-overlap`} value={overlap} onValueChange={(v) => setOverlap(v as typeof overlap)}>
                    <option value="Forbid">Skip the new run</option>
                    <option value="Allow">Run both</option>
                    <option value="Replace">Replace the old run</option>
                  </Select>
                </div>
              )}
              <div className="field">
                <label htmlFor={`${ids.cron}-retries`}>Retries</label>
                <input id={`${ids.cron}-retries`} className="input" inputMode="numeric" value={retries} onChange={(e) => setRetries(e.target.value)} />
                <FieldError id={`${ids.cron}-retries-err`} msg={errs.fields['run.retries']} />
              </div>
              <div className="field">
                <label htmlFor={`${ids.cron}-timeout`}>Time limit</label>
                <input id={`${ids.cron}-timeout`} className="input" placeholder="1h" value={timeout} onChange={(e) => setTimeoutText(e.target.value)} />
                <FieldError id={`${ids.cron}-timeout-err`} msg={errs.fields['run.timeout']} />
              </div>
            </More>
          </Card>
          </Section>
        </div>
        <Card className="pad review">
          <PartTitle part="review" as="h3">
            Review
          </PartTitle>
          <div className="field">
            <label htmlFor={ids.name}>Name</label>
            <input
              id={ids.name}
              className={`input${errs.fields.displayName ? ' err' : ''}`}
              value={name}
              placeholder="Weekly report"
              onChange={(e) => setTypedName(e.target.value)}
              aria-invalid={errs.fields.displayName ? true : undefined}
              aria-describedby={errs.fields.displayName ? ids.nameErr : undefined}
            />
            <FieldError id={ids.nameErr} msg={errs.fields.displayName} />
          </div>
          {missing && <div className="muted small todo-left">{missing}</div>}
          <Button type="submit" variant="primary" className="wide" disabled={!!missing || save.isPending}>
            {job ? 'Save changes' : 'Create job'}
          </Button>
          {job && (
            <Link className="btn wide" to={`/jobs/${encodeURIComponent(job.name)}`}>
              Cancel
            </Link>
          )}
        </Card>
      </div>
      {picking && (
        <HomePicker
          extensions={extensions}
          acceptReason={`${tpl?.displayName ?? 'This template'} runs ${extensions.join(' or ')} files`}
          onClose={() => setPicking(false)}
          onChoose={(p) => {
            setSource(p)
            setKernel('')
            setPicking(false)
          }}
        />
      )}
    </form>
  )
}

/** Templates that can run jobs; the ones that only run jobs are included. */
function useJobTemplates() {
  const t = useTemplates()
  return { ...t, jobTemplates: (t.data ?? []).filter((x) => x.jobs) }
}

/** New job; ?sample=<template> fills it in from that template's sample job. */
export function NewJob() {
  useWarmHome()
  const t = useJobTemplates()
  const jobs = useJobs()
  const [search] = useSearchParams()
  if (t.isError) return <Banner tone="err" title={problemText(t.error)} />
  const sampleOf = t.jobTemplates.find((x) => x.name === search.get('sample') && x.jobs?.sample)
  // A sample names the job, so wait for the jobs it must not clash with.
  if (t.isPending || (search.get('sample') && jobs.isPending)) return <div className="muted">Loading</div>
  return <JobForm key={sampleOf?.name} sampleOf={sampleOf} jobNames={(jobs.data ?? []).map((j) => j.displayName)} templates={t.jobTemplates} />
}

export function EditJob() {
  const { name = '' } = useParams()
  const t = useJobTemplates()
  const job = useJob(name)
  if (job.isError || t.isError) return <Banner tone="err" title={problemText(job.error ?? t.error)} />
  if (!job.data || t.isPending) return <div className="muted">Loading</div>
  return <JobForm key={job.data.name} job={job.data} templates={t.jobTemplates} />
}
