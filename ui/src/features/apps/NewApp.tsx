import { File as FileIcon, Folder, Sparkles, X } from 'lucide-react'
import { useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent, type KeyboardEvent, type Ref } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { agentLabel, useAgentStatus, useAgentSuggestions, useBuildWithAgent } from '../ai/ai'
import type { DraftView, Template } from '../../api'
import { ApiAccessChoices, type ApiAccess } from './ApiAccess'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { HomePicker } from '../files/HomePicker'
import { IdleStopChoice } from './IdleStop'
import { KindLogo, kindSite } from '../../components/KindLogo'
import { LaunchFields, launchBody, type Launch } from './LaunchFields'
import { TemplateRuns } from './RunCommand'
import { More } from '../../components/More'
import { isGitHubRepo, PreviewSettings } from './PreviewsCard'
import { emptyRepo, RepoFields, sourceBody, type Repo } from './RepoFields'
import { APP_ENV_HINT, EnvEditor, envBody, type EnvRow } from '../environments/EnvEditor'
import { EnvironmentCard } from '../environments/EnvironmentCard'
import { ChosenRow, Folded } from '../../components/Folded'
import { PartTitle, Section } from '../../components/Section'
import { SizeCard } from '../sizes/SizePicker'
import { NoticeBanner } from './StorageNotices'
import { Switch } from '../../components/Switch'
import { TemplateGroups } from '../templates/TemplateGroups'
import { fieldErrors, problemText } from '../../lib/http'
import { firstFreeName } from '../../lib/names'
import {
  useCreateWorkspace,
  useCustomAllowance,
  useIdleStopPolicy,
  useNewWebhook,
  usePlacement,
  useSecrets,
  usePreviewOptions,
  useSizes,
  useTemplates,
  useWarmHome,
  useWorkspaces,
} from '../../lib/queries'
import { choiceReady, startingChoice, type SizeChoice } from '../../lib/sizes'

// Request fields the form has a place for; errors on any other field go to the top banner.
const FORM_FIELDS = new Set([
  'displayName',
  'template',
  'file',
  'size',
  'resources',
  'command',
  'image',
  'port',
  'source',
  'source.url',
  'source.ref',
  'source.path',
  'source.token',
  'source.build.command',
  'source.build.output',
  'source.previews',
  'source.previews.max',
  'source.webhook',
  'token',
  'idleStopMinutes',
  'environment',
])
// Fields of the Where is the code? card: an error on one opens it.
const CODE_FIELDS = ['file', 'token', 'source', 'source.url', 'source.ref', 'source.path', 'source.token', 'source.build.command', 'source.build.output']
// Errors on the app's variables (env, env[0].name, ...) show on the editor.
const isFormField = (f: string) => FORM_FIELDS.has(f) || f === 'env' || f.startsWith('env[')

/** "<template> app", numbered from 2 when the user already has an app with that name. */
function defaultName(template: string, taken: string[]): string {
  return firstFreeName(`${template} app`, taken)
}

function FieldError({ id, msg }: { id: string; msg?: string }) {
  return msg ? (
    <span id={id} className="errmsg">
      {msg}
    </span>
  ) : null
}

/** The note next to a field the AI filled in; it goes once the person changes the field. */
function AiMark({ show }: { show: boolean }) {
  return show ? (
    <span className="ai-mark">
      <Sparkles size={13} aria-hidden="true" />
      Filled by AI
    </span>
  ) : null
}

/** Describe it: plain words go to the coding agent, which builds and deploys the app; its page opens. */
function DescribeIt({ agent }: { agent: { template: string; templateName: string; mode: string } }) {
  const build = useBuildWithAgent()
  const navigate = useNavigate()
  const [text, setText] = useState('')
  // Dismissed by Escape or leaving the box, until the next keystroke.
  const [closed, setClosed] = useState(false)
  // Suggestions come from the admin's agent model, so only with that one in effect.
  const fetched = useAgentSuggestions(text, agent.mode === 'platform' && !closed)[0]
  // The last suggestion stays while the typing goes on along it; deleting
  // drops it, and a new one replaces it after the next pause.
  const [suggestion, setSuggestion] = useState<string>()
  useEffect(() => {
    if (fetched) setSuggestion(fetched)
  }, [fetched])
  // The rest is drawn after the text, so it shows only with the caret there.
  const [caretAtEnd, setCaretAtEnd] = useState(true)
  // The rest of the suggestion, shown after the text in the box while the
  // typing still matches it; Tab takes it.
  const rest =
    !closed && caretAtEnd && suggestion && suggestion.startsWith(text) && suggestion.length > text.length ? suggestion.slice(text.length) : ''
  // The grey copy behind the box scrolls with it, so the rest stays after the text.
  const ghostRef = useRef<HTMLDivElement>(null)
  const boxRef = useRef<HTMLTextAreaElement>(null)
  const syncScroll = () => {
    if (ghostRef.current && boxRef.current) ghostRef.current.scrollTop = boxRef.current.scrollTop
  }
  // The box grows with its text and the suggested rest, between the min and
  // max height the styles set (CSS field-sizing is not in every browser
  // yet), then scrolls.
  useLayoutEffect(() => {
    const b = boxRef.current
    const g = ghostRef.current
    if (!b || !g) return
    b.style.height = 'auto'
    b.style.height = `${Math.max(b.scrollHeight, g.scrollHeight) + b.offsetHeight - b.clientHeight}px`
    syncScroll()
  }, [text, rest])
  const name = agent.templateName || agent.template
  const site = kindSite(agent.template)
  const id = useId()
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (!rest) return
    if (e.key === 'Tab' && !e.shiftKey) {
      e.preventDefault()
      setText(text + rest)
      setClosed(true)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setClosed(true)
    }
  }
  return (
    <Card className="pad describe" aria-label="Describe it">
      <PartTitle part="ai" className="card-h">
        Describe it
      </PartTitle>
      <div className="field">
        <label htmlFor={id}>Describe the app</label>
        <div className="ghost-wrap">
          {/* The typed text, invisible, then the rest in grey: drawn behind the box, in its font and padding. */}
          <div ref={ghostRef} className="input ghost" aria-hidden="true">
            <span className="ghost-typed">{text}</span>
            <span className="ghost-rest">{rest}</span>
          </div>
          <textarea
            id={id}
            className="input"
            rows={2}
            value={text}
            placeholder="A Streamlit dashboard of sales/data.csv"
            aria-describedby={rest ? `${id}-hint` : undefined}
            ref={boxRef}
            onChange={(e) => {
              const next = e.target.value
              if (!next.startsWith(text)) setSuggestion(undefined)
              setText(next)
              setClosed(false)
            }}
            onSelect={(e) => {
              const b = e.currentTarget
              setCaretAtEnd(b.selectionStart === b.value.length && b.selectionEnd === b.value.length)
            }}
            onScroll={syncScroll}
            onKeyDown={onKeyDown}
            onBlur={() => setClosed(true)}
          />
        </div>
        {rest && (
          <span id={`${id}-hint`} className="sr-only">
            Suggestion: {text + rest}. Press Tab to accept.
          </span>
        )}
      </div>
      <div className="row describe-foot">
        <Button
          disabled={!text.trim() || build.isPending}
          onClick={() => build.mutate(text.trim(), { onSuccess: (w) => navigate(`/apps/${encodeURIComponent(w.name)}`, { state: { building: true } }) })}
        >
          <KindLogo kind={agent.template} inline />
          Build with {agentLabel(agent.template)}
        </Button>
        <span className="faint small">
          {site ? (
            <a href={site} target="_blank" rel="noopener">
              {name}
            </a>
          ) : (
            name
          )}{' '}
          writes the code and deploys the app.
        </span>
      </div>
      {build.isError && <p role="alert">{problemText(build.error)}</p>}
    </Card>
  )
}

function SourceCard({ value, title, line, checked, onPick }: { value: 'home' | 'repo'; title: string; line: string; checked: boolean; onPick: (v: 'home' | 'repo') => void }) {
  return (
    <label className={`card choice tpl${checked ? ' sel' : ''}`}>
      <input type="radio" name="source" className="sr-only" aria-label={title} value={value} checked={checked} onChange={() => onPick(value)} />
      <KindLogo kind={value === 'repo' ? 'git' : 'home'} />
      <span>
        <h3>{title}</h3>
        <span className="muted small block one-line">{line}</span>
      </span>
    </label>
  )
}

function TemplateCard({ t, checked, onPick }: { t: Template; checked: boolean; onPick: () => void }) {
  return (
    <label className={`card choice tpl${checked ? ' sel' : ''}`}>
      <input type="radio" name="template" className="sr-only" value={t.name} checked={checked} onChange={onPick} />
      <KindLogo kind={t.kind} />
      <span>
        <h3>{t.displayName}</h3>
        <span className="muted small block one-line" title={t.description}>
          {t.description}
        </span>
      </span>
    </label>
  )
}

/** The chosen template as one row; pressing it opens the full list again. */
function ChosenTemplate({ t, buttonRef, animate, aiFilled, describedBy, onChange }: {
  t: Template
  buttonRef: Ref<HTMLButtonElement>
  animate: boolean
  aiFilled: boolean
  describedBy?: string
  onChange: () => void
}) {
  return (
    <ChosenRow
      icon={<KindLogo kind={t.kind} />}
      title={
        <>
          {t.displayName} <AiMark show={aiFilled} />
        </>
      }
      line={t.description}
      label={t.displayName}
      buttonRef={buttonRef}
      animate={animate}
      describedBy={describedBy}
      onChange={onChange}
    />
  )
}

export function NewApp() {
  useWarmHome()
  const templates = useTemplates()
  const create = useCreateWorkspace()
  const navigate = useNavigate()
  const [templateName, setTemplateName] = useState('')
  const workspaces = useWorkspaces()
  // null until the user types; then their text always wins over the default.
  const [typedName, setTypedName] = useState<string | null>(null)
  const [file, setFile] = useState('')
  // True once the user moves off the template's sample to code of their own.
  const [ownCode, setOwnCode] = useState(false)
  const [picking, setPicking] = useState(false)
  // True while the full template list is open again after a template was chosen.
  const [choosing, setChoosing] = useState(false)
  // True once the person opens or closes the list, so the change animates but the first paint does not.
  const [animate, setAnimate] = useState(false)
  // Where focus goes once the list opens or closes; the element pressed is gone by then.
  const focusAfter = useRef<'chosen' | 'list' | null>(null)
  // True from an arrow key down to its key up: arrowing through the list picks without closing it.
  const arrowing = useRef(false)
  const chosenRef = useRef<HTMLButtonElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  // null until the user picks a size or an amount; the default size is preselected.
  const [pickedSize, setPickedSize] = useState<SizeChoice | null>(null)
  // null until the user types; the sample's command (or nothing) shows meanwhile.
  const [typedCommand, setTypedCommand] = useState<string | null>(null)
  const [image, setImage] = useState('')
  // null until the user types; the template's port shows meanwhile.
  const [typedPort, setTypedPort] = useState<string | null>(null)
  // null until the user picks; the template's default is chosen meanwhile.
  const [pickedAccess, setPickedAccess] = useState<ApiAccess | null>(null)
  // null until the user picks; the template's default (the admin's unless it has its own) is chosen meanwhile.
  const [pickedIdle, setPickedIdle] = useState<number | null>(null)
  // Fields the AI filled (shown with a note until edited) and the field
  // errors its filled form would get from Create.
  const agentStatus = useAgentStatus().data
  const agent = agentStatus?.available && agentStatus.mode !== 'login' ? agentStatus : undefined
  const [filledBy, setFilledBy] = useState<Set<string>>(new Set())
  const [draftErrs, setDraftErrs] = useState<Record<string, string>>({})
  const edited = (field: string) => {
    setFilledBy((s) => (s.has(field) ? new Set([...s].filter((f) => f !== field)) : s))
    setDraftErrs((e) => {
      if (!(field in e)) return e
      const { [field]: _gone, ...rest } = e
      return rest
    })
  }
  const idlePolicy = useIdleStopPolicy().data
  const [envRows, setEnvRows] = useState<EnvRow[]>([])
  // The id of the picked environment; empty for none.
  const [environment, setEnvironment] = useState('')
  const mySecrets = useSecrets().data ?? []
  // null until the user picks; a template that may run from a repository starts from the home folder.
  const [where, setWhere] = useState<'home' | 'repo' | null>(null)
  const [repo, setRepo] = useState<Repo>(emptyRepo)
  // Previews the app starts with; max is null until picked, so the platform's default shows.
  const [previews, setPreviews] = useState<{ enabled: boolean; max: number | null; forks: boolean }>({ enabled: false, max: null, forks: false })
  // The webhook made for the app before it exists; its secret is shown in this answer only.
  const hook = useNewWebhook()
  const sizes = useSizes().data ?? []
  const allowance = useCustomAllowance()
  const size: SizeChoice = pickedSize ?? startingChoice(templates.data?.find((t) => t.name === templateName)?.defaultResources, sizes, allowance)
  // A custom amount has no size name; the preview then uses the template's requests.
  const placement = usePlacement(templateName, 'size' in size ? size.size : '').data
  const ids = { name: useId(), nameErr: useId(), fileErr: useId(), tplErr: useId(), srcErr: useId(), tplHead: useId(), prevErr: useId() }

  // Job-only templates run jobs, never apps; the Jobs screens use them.
  const list = (templates.data ?? []).filter((t) => !t.jobOnly)
  const tpl = list.find((t) => t.name === templateName)
  const name =
    typedName ?? (tpl ? defaultName(tpl.displayName, (workspaces.data ?? []).map((w) => w.displayName)) : '')
  const exts = tpl?.fileExtensions ?? []
  const repoMode = tpl?.fromRepo === 'Required' || (tpl?.fromRepo === 'Optional' && where === 'repo')
  const previewOptions = usePreviewOptions(repoMode).data
  // Previews work with GitHub repositories, on a platform that has them.
  const offersPreviews = repoMode && !!previewOptions?.available && isGitHubRepo(repo.url)
  const previewMax = previews.max ?? previewOptions?.max ?? 1
  const wantsFile = exts.length > 0
  const takesCommand = !!tpl && tpl.userCommand !== 'None'
  const takesImage = !!tpl?.userImage
  // The sample whose files the app opens; an image sample instead fills the
  // image fields when the user asks for it (fillImageSample).
  const sample = repoMode || ownCode || takesImage ? undefined : tpl?.sample
  const imageSample = takesImage ? tpl?.sample : undefined
  // A typed command runs in the folder of a file the user may pick.
  const picksFile = wantsFile || takesCommand
  // A command template's Change leaves its sample for a command of the user's own;
  // other templates open the picker at the sample.
  const changeOpensPicker = wantsFile || !takesCommand
  const launch: Launch = { command: typedCommand ?? sample?.command ?? '', image, port: typedPort ?? String(tpl?.port ?? '') }
  const errs = create.isError ? fieldErrors(create.error) : { fields: draftErrs, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !isFormField(f)).map(([, m]) => m)]
  const env = envBody(envRows)
  const missing = !tpl
    ? 'Pick what to run'
    : !name.trim()
      ? 'Give it a name'
      : repoMode && !repo.url.trim()
        ? 'Enter the repository address'
        : repoMode && wantsFile && !tpl.build && !repo.file.trim()
          ? 'Type the app file'
          : !repoMode && wantsFile && !sample && !file
            ? 'Choose the app file'
            : takesImage && !image.trim()
          ? 'Name the image'
          : tpl.userCommand === 'Required' && !launch.command.trim()
            ? 'Type the command'
            : ''

  const pickTemplate = (t: Template) => {
    if (t.name !== templateName) {
      setFile('')
      setOwnCode(false)
      setTypedCommand(null)
      setImage('')
      setTypedPort(null)
      setPickedAccess(null)
      setWhere(null)
      setRepo(emptyRepo)
      setPreviews({ enabled: false, max: null, forks: false })
      hook.reset()
      // An image template starts from its sample, as the others do.
      if (t.userImage && t.sample) fillImageSample(t.sample)
    }
    setTemplateName(t.name)
  }

  // A pick from the list closes it on the chosen row, unless it came from an arrow key.
  const choose = (t: Template, close: boolean) => {
    pickTemplate(t)
    edited('template')
    if (close) {
      setChoosing(false)
      setAnimate(true)
      focusAfter.current = 'chosen'
    }
  }
  const reopen = () => {
    setChoosing(true)
    setAnimate(true)
    focusAfter.current = 'list'
  }
  // Enter or Space on a template keeps it and closes the list; Enter must not submit the form.
  const listKeys = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key.startsWith('Arrow')) arrowing.current = true
    const t = e.target instanceof HTMLInputElement ? list.find((x) => x.name === (e.target as HTMLInputElement).value) : undefined
    if (t && (e.key === 'Enter' || e.key === ' ')) {
      e.preventDefault()
      choose(t, true)
    }
  }
  const listOpen = !tpl || choosing
  useEffect(() => {
    if (focusAfter.current === 'chosen') chosenRef.current?.focus()
    if (focusAfter.current === 'list') listRef.current?.querySelector<HTMLInputElement>('input:checked')?.focus()
    focusAfter.current = null
  }, [listOpen])
  // A refused template opens the list again, so another can be picked.
  const tplRefusal = errs.fields.template
  useEffect(() => {
    if (tplRefusal) setChoosing(true)
  }, [tplRefusal])

  const fillImageSample = (s: NonNullable<Template['sample']>) => {
    setImage(s.image ?? '')
    setTypedPort(s.port ? String(s.port) : null)
    setTypedCommand(s.command)
  }

  // A sample card links here with ?template=<name>: choose it (with its sample
  // and default name) so Create is the only click left. An image sample comes
  // with &sample=1 and fills the image fields; &sample=none asks for the
  // user's own code instead.
  const [params] = useSearchParams()
  const wanted = params.get('template')
  const wantsSample = params.get('sample') === '1'
  const noSample = params.get('sample') === 'none'
  useEffect(() => {
    const t = wanted ? list.find((x) => x.name === wanted) : undefined
    if (t) {
      pickTemplate(t)
      setOwnCode(noSample)
    }
    if (t?.userImage && t.sample && wantsSample) fillImageSample(t.sample)
    if (t?.userImage && noSample) {
      setImage('')
      setTypedPort(null)
      setTypedCommand(null)
    }
    // pickTemplate is recreated every render; rerun only when the request or the list changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wanted, wantsSample, noSample, list.length])

  const fill = (r: DraftView) => {
    const t = list.find((x) => x.name === r.draft.template)
    if (t) {
      pickTemplate(t)
      setChoosing(false)
    }
    if (r.draft.file) {
      setFile(r.draft.file)
      setOwnCode(true)
    }
    if (r.draft.displayName) setTypedName(r.draft.displayName)
    setFilledBy(new Set(r.setBy ?? []))
    const errsBy: Record<string, string> = {}
    for (const e of r.errors ?? []) errsBy[e.location.replace(/^body\./, '')] = e.message
    setDraftErrs(errsBy)
  }

  // Ask AI in the palette links here with a draft in the navigation state:
  // fill the form with it once the templates are in, as Describe it would.
  const handed = (useLocation().state as { draft?: DraftView } | null)?.draft
  useEffect(() => {
    if (handed && list.length) fill(handed)
    // fill is recreated every render; rerun only when the draft or the list changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [handed, list.length])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (missing || !tpl || !choiceReady(allowance, size)) return
    create.mutate(
      {
        displayName: name.trim(),
        template: tpl.name,
        ...(repoMode
          ? {
              source: {
                ...sourceBody(tpl, repo),
                ...(offersPreviews && previews.enabled
                  ? {
                      previews: { enabled: true, max: previewMax, forks: previews.forks },
                      ...(hook.data ? { webhook: { id: hook.data.id, secret: hook.data.secret } } : {}),
                    }
                  : {}),
              },
              ...(repo.file.trim() ? { file: repo.file.trim() } : {}),
            }
          : sample
            ? { sample: true }
            : file
              ? { file }
              : {}),
        ...('resources' in size ? { resources: size.resources } : size.size ? { size: size.size } : {}),
        ...launchBody(tpl, launch),
        // Left out at the default, so the template's own default applies.
        ...(pickedAccess && pickedAccess !== tpl.apiAccessDefault ? { apiAccess: pickedAccess } : {}),
        // Left out at the template's default, which then applies.
        ...(pickedIdle !== null && pickedIdle !== tpl.defaultIdleStopMinutes ? { idleStopMinutes: pickedIdle } : {}),
        ...(env.length ? { env } : {}),
        ...(environment ? { environment: { id: environment } } : {}),
      },
      {
        onSuccess: (ws) => navigate(`/apps/${encodeURIComponent(ws.name)}`),
      },
    )
  }

  return (
    <form onSubmit={submit} noValidate>
      <h1>New app</h1>
      <p className="sub">Pick what to run. You can change everything later.</p>
      {other.length > 0 && (
        <div className="section-sm">
          <Banner tone="err" title="Nothing was created">
            {other.join(' ')}
          </Banner>
        </div>
      )}
      <div className="two section">
        <div className="stack">
          {agent &&
            (wanted ? (
              // Arriving with a template chosen, describing is a detour: kept behind a toggle.
              <More closed={`Build it with ${agentLabel(agent.template)}`} open="Hide describe it">
                <DescribeIt agent={agent} />
              </More>
            ) : (
              <DescribeIt agent={agent} />
            ))}
          <Section part="app" title="App">
          {tpl && !listOpen ? (
            <div className="stack tight-gap">
              <ChosenTemplate
                t={tpl}
                buttonRef={chosenRef}
                animate={animate}
                aiFilled={filledBy.has('template')}
                describedBy={errs.fields.template ? ids.tplErr : undefined}
                onChange={reopen}
              />
              <FieldError id={ids.tplErr} msg={errs.fields.template} />
            </div>
          ) : !tpl && wanted && list.some((x) => x.name === wanted) ? null : (
            <Card className={`pad${animate ? ' tpl-in' : ''}`}>
              <h2 id={ids.tplHead} className="card-h">
                What do you want to run? <AiMark show={filledBy.has('template')} />
              </h2>
              {templates.isError ? (
                <Banner tone="err" title={problemText(templates.error)} />
              ) : templates.isPending ? (
                <div className="muted">Loading</div>
              ) : (
                <div
                  ref={listRef}
                  role="radiogroup"
                  aria-labelledby={ids.tplHead}
                  aria-describedby={errs.fields.template ? ids.tplErr : undefined}
                  onKeyDown={listKeys}
                  onKeyUp={() => (arrowing.current = false)}
                >
                  <TemplateGroups
                    templates={list}
                    card={(t) => <TemplateCard key={t.name} t={t} checked={t.name === templateName} onPick={() => choose(t, !arrowing.current)} />}
                  />
                </div>
              )}
              <FieldError id={ids.tplErr} msg={errs.fields.template} />
            </Card>
          )}
          </Section>
          {tpl && !takesImage && (picksFile || tpl.sample || tpl.fromRepo !== 'None') && (
            <Section part="code" title="Code">
            <Folded
              // A new template starts folded again.
              key={tpl.name}
              foldable={!repoMode && (!!sample || !wantsFile || !!file)}
              // An error inside, or a file the AI filled in, is shown for review.
              attention={filledBy.has('file') || CODE_FIELDS.some((f) => errs.fields[f])}
              icon={<KindLogo kind="home" />}
              title="Code"
              value={['Home folder', sample ? `Sample: ${sample.title}` : file].filter(Boolean).join(' · ')}
            >
              <Card className="pad">
                <h2 className="card-h">Where is the code?</h2>
                {tpl.fromRepo === 'Optional' && (
                  <div className="grid choices source-choices" role="radiogroup" aria-label="Where is the code?">
                    <SourceCard value="home" title="Home folder" line="Files you keep here" checked={!repoMode} onPick={setWhere} />
                    <SourceCard value="repo" title="Git repository" line="Clone from GitHub" checked={repoMode} onPick={setWhere} />
                  </div>
                )}
                {repoMode ? (
                  <>
                    <RepoFields tpl={tpl} value={repo} onChange={setRepo} errors={errs.fields} secrets={mySecrets.map((s) => s.name)} />
                    <FieldError id={ids.srcErr} msg={errs.fields.source} />
                    {tpl.build && (
                      <p className="tight muted small">
                        {tpl.build.ifFile ? `A folder with a ${tpl.build.ifFile} is built first; one without is served as it is. ` : ''}
                        The site is served under its own address prefix, so use relative links to its files.
                      </p>
                    )}
                  </>
                ) : (
                <div className="field">
                  <span className="field-label">
                    {picksFile ? 'App file' : 'Folder'} <AiMark show={filledBy.has('file')} />
                  </span>
                  {sample ? (
                    <div className="filechip">
                      {picksFile ? <FileIcon size={20} aria-hidden="true" className="fi" /> : <Folder size={20} aria-hidden="true" className="fi" />}
                      <span className="grow">{`Sample: ${sample.file} (${sample.title})`}</span>
                      <Button size="sm" onClick={() => (changeOpensPicker ? setPicking(true) : setOwnCode(true))}>
                        Change
                      </Button>
                    </div>
                  ) : !picksFile ? (
                    <div className={`filechip${errs.fields.file ? ' err' : ''}`}>
                      <Folder size={20} aria-hidden="true" className="fi" />
                      <span className={`grow${file ? ' mono' : ''}`}>{file || 'Home folder'}</span>
                      <Button size="sm" onClick={() => setPicking(true)}>
                        Change
                      </Button>
                      {file && <Button size="sm" variant="ghost" icon={X} aria-label="Clear folder" onClick={() => setFile('')} />}
                    </div>
                  ) : file ? (
                    <div className={`filechip${errs.fields.file ? ' err' : ''}`}>
                      <FileIcon size={20} aria-hidden="true" className="fi" />
                      <span className="mono grow">{file}</span>
                      <Button size="sm" onClick={() => setPicking(true)}>
                        Change
                      </Button>
                      <Button size="sm" variant="ghost" icon={X} aria-label="Clear file" onClick={() => setFile('')} />
                    </div>
                  ) : (
                    <button
                      type="button"
                      className="pickbtn"
                      onClick={() => setPicking(true)}
                      aria-describedby={errs.fields.file ? ids.fileErr : undefined}
                    >
                      <FileIcon size={20} aria-hidden="true" className="fi" />
                      <span className="grow">
                        <b>Choose a file from your home folder</b>
                        <span className="muted small block">
                          {tpl.build
                            ? `${tpl.displayName} serves one ${exts.join(' or ')} file, copied when the app starts`
                            : wantsFile
                            ? `${tpl.displayName} runs ${exts.join(' or ')} files`
                            : 'Optional. The command runs in its folder, or in your home folder.'}
                        </span>
                      </span>
                      <span className="btn sm" aria-hidden="true">
                        Browse
                      </span>
                    </button>
                  )}
                  {tpl.sample && !sample && (
                    <button type="button" className="textlink small" onClick={() => setOwnCode(false)}>
                      Use the sample again
                    </button>
                  )}
                  <FieldError id={ids.fileErr} msg={errs.fields.file} />
                </div>
                )}
              </Card>
            </Folded>
            </Section>
          )}
          {tpl && offersPreviews && previewOptions && (
            <Section part="previews" title="Previews">
            <Card className="pad">
              <div className="stack">
                <div className="stack tight-gap">
                  <Switch label="Preview open pull requests" checked={previews.enabled} onChange={(enabled) => setPreviews({ ...previews, enabled })} />
                  <p className="tight muted small">Each open pull request gets its own app and link. It follows new commits and goes away when the pull request closes.</p>
                </div>
                {previews.enabled && (
                  <More closed="Limit, pull requests from forks and webhook" open="Hide limit, forks and webhook">
                    <PreviewSettings
                      id="new-app"
                      max={previewMax}
                      maxAllowed={previewOptions.maxAllowed}
                      forks={previews.forks}
                      forksWarning={previewOptions.forksWarning}
                      onChange={(patch) => setPreviews({ ...previews, max: patch.max ?? previewMax, forks: patch.forks ?? previews.forks })}
                      webhook={{ configured: false, shown: hook.data, pending: hook.isPending, error: hook.isError ? hook.error : undefined, make: () => hook.mutate(repo.url.trim()) }}
                    />
                  </More>
                )}
                <FieldError id={ids.prevErr} msg={errs.fields['source.previews'] ?? errs.fields['source.previews.max'] ?? errs.fields['source.webhook']} />
              </div>
            </Card>
            </Section>
          )}
          {tpl && (takesImage || takesCommand) && (
            <Section part={takesImage ? 'image' : 'start'} title={takesImage ? 'Container' : 'Start'}>
            <Card className="pad">
              <h2 className="card-h">{takesImage ? 'Which image?' : 'How does it start?'}</h2>
              <LaunchFields
                tpl={tpl}
                value={launch}
                onChange={(l) => {
                  setTypedCommand(l.command)
                  setImage(l.image)
                  setTypedPort(l.port)
                }}
                errors={errs.fields}
              />
              {imageSample && image.trim() !== imageSample.image && (
                <button type="button" className="textlink small" onClick={() => fillImageSample(imageSample)}>
                  {`Use the sample: ${imageSample.title}`}
                </button>
              )}
            </Card>
            </Section>
          )}
          {tpl?.run && <TemplateRuns run={tpl.run} file={file} command={launch.command} image={launch.image} />}
          {tpl && !takesImage && <EnvironmentCard what="app" value={environment} onChange={setEnvironment} error={errs.fields.environment} />}
          {tpl && sizes.length > 0 && (
            <SizeCard sizes={sizes} custom={allowance} value={size} onChange={setPickedSize} errors={errs.fields} />
          )}
          {tpl && placement?.coLocated && placement.notice && <NoticeBanner n={placement.notice} />}
          {tpl && (
            <Section part="advanced" title="Advanced">
            <More closed="Show advanced settings" open="Hide advanced settings" defaultOpen={envRows.length > 0}>
              <div className="stack">
                {tpl.maxApiAccess !== 'None' && (
                  <Card className="pad">
                    <PartTitle part="apiAccess" className="card-h">
                      API access
                    </PartTitle>
                    <ApiAccessChoices value={pickedAccess ?? tpl.apiAccessDefault} max={tpl.maxApiAccess} onChange={setPickedAccess} />
                  </Card>
                )}
                <Card className="pad">
                  <PartTitle part="variables" className="card-h">
                    Environment variables
                  </PartTitle>
                  <EnvEditor rows={envRows} available={mySecrets.map((s) => s.name)} errors={errs.fields} onChange={setEnvRows} hint={APP_ENV_HINT} />
                </Card>
                {idlePolicy && (
                  <Card className="pad">
                    <PartTitle part="idle" className="card-h">
                      When nobody uses it
                    </PartTitle>
                    <IdleStopChoice
                      // The template's default is the one this app starts from.
                      policy={{ ...idlePolicy, defaultMinutes: tpl.defaultIdleStopMinutes }}
                      value={pickedIdle ?? tpl.defaultIdleStopMinutes}
                      onChange={setPickedIdle}
                      error={errs.fields.idleStopMinutes}
                    />
                  </Card>
                )}
              </div>
            </More>
            </Section>
          )}
        </div>
        <Card className="pad review">
          <PartTitle part="review" as="h3">
            Review
          </PartTitle>
          <div className="field">
            <label htmlFor={ids.name}>
              Name <AiMark show={filledBy.has('displayName')} />
            </label>
            <input
              id={ids.name}
              className={`input${errs.fields.displayName ? ' err' : ''}`}
              value={name}
              placeholder="Sales app"
              onChange={(e) => {
                setTypedName(e.target.value)
                edited('displayName')
              }}
              aria-invalid={errs.fields.displayName ? true : undefined}
              aria-describedby={errs.fields.displayName ? ids.nameErr : undefined}
            />
            <FieldError id={ids.nameErr} msg={errs.fields.displayName} />
          </div>
          {missing && <div className="muted small todo-left">{missing}</div>}
          <Button type="submit" variant="primary" className="wide" disabled={!!missing || create.isPending || Object.keys(draftErrs).length > 0}>
            Create app
          </Button>
        </Card>
      </div>
      {picking && tpl && (
        <HomePicker
          extensions={exts}
          acceptReason={
            !picksFile
              ? `${tpl.displayName} opens a folder`
              : wantsFile
                ? `${tpl.displayName} runs ${exts.join(' or ')} files`
                : 'Any file can be chosen'
          }
          folders={!picksFile}
          sample={sample?.file || undefined}
          onClose={() => setPicking(false)}
          onChoose={(p) => {
            // Choosing the sample keeps it; anything else is the user's own code.
            edited('file')
            if (!sample || p !== sample.file) {
              setFile(p)
              setOwnCode(true)
            }
            setPicking(false)
          }}
        />
      )}
    </form>
  )
}
