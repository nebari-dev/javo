import { useId, useState, type FormEvent } from 'react'
import type { AiSettings } from '../../../api'
import { Banner } from '../../../components/Banner'
import { Button } from '../../../components/Button'
import { Card } from '../../../components/Card'
import { PartTitle } from '../../../components/Section'
import { Segmented } from '../../../components/Segmented'
import { fieldErrors, problemText } from '../../../lib/http'
import { useAISettings, useIsAdmin, useResetAISettings, useSaveAISettings } from './queries'

type Provider = 'anthropic' | 'openai'

const PROVIDERS: { value: Provider; label: string }[] = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI-compatible' },
]

const ADDRESS_HINT: Record<Provider, string> = {
  anthropic: "Empty is Anthropic's own API.",
  openai: "Empty is OpenAI's own API. For another server give its https address, such as https://openrouter.ai/api/v1.",
}

const FORM_FIELDS = new Set(['provider', 'baseURL', 'model', 'apiKey'])

/** What the key in force is, in a line under the key field. */
function keyHint(s: AiSettings): string {
  if (s.keyStored) return `Stored encrypted, ending in ${s.keyLast4 || '****'}. Leave empty to keep it.`
  if (s.keyFrom === 'config') return "The platform config's key is used. It is sent only to the provider and address set there."
  return 'No key is sent. Paste one to store it encrypted; only its last four characters are shown again.'
}

function where(s: AiSettings): string {
  const what = [s.provider, s.model].filter(Boolean).join(', ')
  return s.overridden ? `Set here: ${what}.` : `From the platform config: ${what}.`
}

/** The form, started from the settings in force; remade after each save (key), so it shows what was stored. */
function AIForm({ s }: { s: AiSettings }) {
  const save = useSaveAISettings()
  const reset = useResetAISettings()
  const ids = { url: useId(), model: useId(), key: useId(), prov: useId() }
  const [provider, setProvider] = useState<Provider>(s.provider === 'openai' ? 'openai' : 'anthropic')
  const [baseURL, setBaseURL] = useState(s.baseURL ?? '')
  const [model, setModel] = useState(s.model ?? '')
  const [apiKey, setApiKey] = useState('')
  const errs = save.isError ? fieldErrors(save.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !FORM_FIELDS.has(f)).map(([, m]) => m)]
  const fields = { provider, baseURL: baseURL.trim(), model: model.trim() }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate({ ...fields, ...(apiKey ? { apiKey } : {}) }, { onSuccess: () => setApiKey('') })
  }

  return (
    <Card className="pad tok-form">
      <form onSubmit={submit} noValidate aria-label="AI settings">
        {(other.length > 0 || reset.isError) && (
          <div className="field">
            <Banner tone="err" title={reset.isError ? problemText(reset.error) : other.join(' ')} />
          </div>
        )}
        <div className="field" role="group" aria-labelledby={ids.prov}>
          <span id={ids.prov} className="field-label">
            Provider
          </span>
          <Segmented label="Provider" value={provider} options={PROVIDERS} onChange={setProvider} />
          {errs.fields.provider && <span className="hint err">{errs.fields.provider}</span>}
        </div>
        <div className="field">
          <label htmlFor={ids.url}>API address</label>
          <input
            id={ids.url}
            className="input mono"
            value={baseURL}
            placeholder={provider === 'openai' ? 'https://api.openai.com/v1' : 'https://api.anthropic.com'}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setBaseURL(e.target.value)}
            aria-invalid={errs.fields.baseURL ? true : undefined}
          />
          <span className="hint">{ADDRESS_HINT[provider]}</span>
          {errs.fields.baseURL && <span className="hint err">{errs.fields.baseURL}</span>}
        </div>
        <div className="field">
          <label htmlFor={ids.model}>Model</label>
          <input
            id={ids.model}
            className="input mono"
            value={model}
            maxLength={200}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setModel(e.target.value)}
            aria-invalid={errs.fields.model ? true : undefined}
          />
          {errs.fields.model && <span className="hint err">{errs.fields.model}</span>}
        </div>
        <div className="field">
          <label htmlFor={ids.key}>API key</label>
          <input
            id={ids.key}
            className="input mono"
            type="password"
            autoComplete="off"
            value={apiKey}
            maxLength={4096}
            onChange={(e) => setApiKey(e.target.value)}
            aria-invalid={errs.fields.apiKey ? true : undefined}
          />
          <span className="hint">{keyHint(s)}</span>
          {errs.fields.apiKey && <span className="hint err">{errs.fields.apiKey}</span>}
        </div>
        <div className="row">
          <Button type="submit" variant="primary" disabled={save.isPending || !model.trim()}>
            Save
          </Button>
          {s.keyStored && (
            <Button type="button" disabled={save.isPending} onClick={() => save.mutate({ ...fields, removeKey: true }, { onSuccess: () => setApiKey('') })}>
              Remove key
            </Button>
          )}
          {s.overridden && (
            <Button type="button" variant="ghost" disabled={reset.isPending} onClick={() => reset.mutate()}>
              Use the platform config
            </Button>
          )}
        </div>
      </form>
    </Card>
  )
}

/**
 * Settings, AI (platform admins): the provider, address, model and key every
 * AI feature and the coding agents use, in place of the platform config's.
 * A change applies at once. The key is write-only.
 */
export function AdminAI() {
  const admin = useIsAdmin()
  const q = useAISettings(admin)
  const head = useId()
  if (!admin) return null
  if (q.isError) return <Banner tone="err" title={problemText(q.error)} />
  if (!q.data) return <div className="muted">Loading</div>
  const s = q.data
  return (
    <section className="section" aria-labelledby={head}>
      <PartTitle part="ai" id={head}>
        AI
      </PartTitle>
      {!s.enabled ? (
        <p className="sub">AI is off in the platform config (ai.enabled), so there is nothing to set here.</p>
      ) : (
        <>
          <p className="sub">
            {where(s)} Every AI feature and the coding agents use these at once. Each change is on the audit log, never the key.
          </p>
          <div className="section-sm">
            <AIForm key={`${s.updatedAt ?? 'config'}-${s.keyFrom}`} s={s} />
          </div>
        </>
      )}
    </section>
  )
}
