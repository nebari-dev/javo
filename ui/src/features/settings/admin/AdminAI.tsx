import { useId, useState, type FormEvent } from 'react'
import type { AiSettings } from '../../../api'
import { Banner } from '../../../components/Banner'
import { Button } from '../../../components/Button'
import { Card } from '../../../components/Card'
import { PartTitle } from '../../../components/Section'
import { Select } from '../../../components/Select'
import { fieldErrors, problemText } from '../../../lib/http'
import { addressOf, AI_PROVIDERS, providerAt } from './aiProviders'
import { useAISettings, useIsAdmin, useResetAISettings, useSaveAISettings } from './queries'

const CUSTOM = 'custom'

const FORM_FIELDS = new Set(['baseURL', 'model', 'apiKey'])

/** What the key in force is, in a line under the key field. */
function keyHint(s: AiSettings): string {
  if (s.keyStored) return `Stored encrypted, ending in ${s.keyLast4 || '****'}. Leave empty to keep it.`
  if (s.keyFrom === 'config') return "The platform config's key is used. It is sent only to the provider and address set there."
  return 'No key is sent. Paste one to store it encrypted; only its last four characters are shown again.'
}

function where(s: AiSettings): string {
  const address = addressOf(s.provider, s.baseURL)
  const what = [providerAt(address)?.label ?? address, s.model].filter(Boolean).join(', ')
  return s.overridden ? `Set here: ${what}.` : `From the platform config: ${what}.`
}

/** The form, started from the settings in force; remade after each save (key), so it shows what was stored. */
function AIForm({ s }: { s: AiSettings }) {
  const save = useSaveAISettings()
  const reset = useResetAISettings()
  const ids = { url: useId(), model: useId(), modelId: useId(), key: useId(), prov: useId() }
  const [baseURL, setBaseURL] = useState(() => addressOf(s.provider, s.baseURL))
  const [choice, setChoice] = useState(() => providerAt(baseURL)?.id ?? CUSTOM)
  const preset = AI_PROVIDERS.find((p) => p.id === choice)
  const [model, setModel] = useState(s.model ?? '')
  const [customModel, setCustomModel] = useState(() => !preset?.models.includes(model))
  const [apiKey, setApiKey] = useState('')
  const errs = save.isError ? fieldErrors(save.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !FORM_FIELDS.has(f)).map(([, m]) => m)]
  const fields = { baseURL: baseURL.trim(), model: model.trim() }

  const pickProvider = (id: string) => {
    setChoice(id)
    const p = AI_PROVIDERS.find((x) => x.id === id)
    if (!p) return
    setBaseURL(p.baseURL)
    if (!customModel && !p.models.includes(model)) setModel(p.models[0])
  }
  const typeURL = (v: string) => {
    setBaseURL(v)
    setChoice(providerAt(v)?.id ?? CUSTOM)
  }
  const pickModel = (v: string) => {
    if (v === CUSTOM) {
      setCustomModel(true)
      return
    }
    setCustomModel(false)
    setModel(v)
  }
  const modelChoice = customModel || !preset?.models.includes(model) ? CUSTOM : model

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
        <div className="field">
          <label htmlFor={ids.prov}>Provider</label>
          <Select id={ids.prov} value={choice} onValueChange={pickProvider}>
            {AI_PROVIDERS.map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
            <option value={CUSTOM}>Custom</option>
          </Select>
        </div>
        <div className="field">
          <label htmlFor={ids.url}>API address</label>
          <input
            id={ids.url}
            className="input mono"
            value={baseURL}
            placeholder="https://llm.example.com/v1"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => typeURL(e.target.value)}
            aria-invalid={errs.fields.baseURL ? true : undefined}
          />
          <span className="hint">The https address of an OpenAI-compatible API. Pick a provider to fill it, or type any.</span>
          {errs.fields.baseURL && <span className="hint err">{errs.fields.baseURL}</span>}
        </div>
        {preset && (
          <div className="field">
            <label htmlFor={ids.model}>Model</label>
            <Select id={ids.model} value={modelChoice} onValueChange={pickModel}>
              {preset.models.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
              <option value={CUSTOM}>Custom</option>
            </Select>
            {modelChoice !== CUSTOM && errs.fields.model && <span className="hint err">{errs.fields.model}</span>}
          </div>
        )}
        {modelChoice === CUSTOM && (
          <div className="field">
            <label htmlFor={ids.modelId}>Model id</label>
            <input
              id={ids.modelId}
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
        )}
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
          <Button type="submit" variant="primary" disabled={save.isPending || !model.trim() || !baseURL.trim()}>
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
 * Settings, AI (platform admins): the OpenAI-compatible address, model and key every
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
