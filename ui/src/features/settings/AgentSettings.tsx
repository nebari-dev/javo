import { useId, useState, type FormEvent } from 'react'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { fieldErrors, problemText } from '../../lib/http'
import { useAgentStatus, useDeleteAgentSettings, useSaveAgentSettings } from '../ai/ai'

const IN_EFFECT: Record<string, string> = {
  platform: 'Your coding agents use the model your admin set up',
  own: 'Your coding agents use your own key',
  login: 'Your coding agents have no model yet: sign in inside the agent (pi: /login), or add your own key here',
}

/**
 * Settings, AI agent: which model the person's coding agents use, and their
 * own OpenAI-compatible model and key for when the admin set none up. The key
 * is write-only: only its last four characters come back.
 */
export function AgentSettings() {
  const status = useAgentStatus()
  const save = useSaveAgentSettings()
  const remove = useDeleteAgentSettings()
  const s = status.data
  const ids = { url: useId(), model: useId(), key: useId(), head: useId() }
  const [baseURL, setBaseURL] = useState('')
  const [model, setModel] = useState('')
  const [apiKey, setApiKey] = useState('')
  if (!s?.available) return null
  const errs = save.isError ? fieldErrors(save.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate({ baseURL: baseURL.trim(), model: model.trim(), ...(apiKey ? { apiKey } : {}) }, { onSuccess: () => setApiKey('') })
  }
  return (
    <section className="section" aria-labelledby={ids.head}>
      <h2 id={ids.head}>AI agent</h2>
      <p className="sub">
        {IN_EFFECT[s.mode]}
        {s.model ? `: ${s.model}` : ''}.
      </p>
      {s.mode !== 'platform' && (
        <Card className="pad tok-form">
          <form onSubmit={submit} noValidate aria-label="Your own agent model">
            {(errs.other.length > 0 || save.isError) && Object.keys(errs.fields).length === 0 && (
              <div className="field">
                <Banner tone="err" title={problemText(save.error)} />
              </div>
            )}
            <div className="field">
              <label htmlFor={ids.url}>API address</label>
              <input id={ids.url} className="input mono" value={baseURL} placeholder={s.baseURL || 'https://openrouter.ai/api/v1'} onChange={(e) => setBaseURL(e.target.value)} />
              {errs.fields.baseURL && <span className="hint err">{errs.fields.baseURL}</span>}
            </div>
            <div className="field">
              <label htmlFor={ids.model}>Model</label>
              <input id={ids.model} className="input mono" value={model} placeholder={s.mode === 'own' ? s.model : 'anthropic/claude-sonnet-5.5'} onChange={(e) => setModel(e.target.value)} />
              {errs.fields.model && <span className="hint err">{errs.fields.model}</span>}
            </div>
            <div className="field">
              <label htmlFor={ids.key}>API key</label>
              <input id={ids.key} className="input mono" type="password" autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />
              <span className="hint">{s.keyLast4 ? `Stored, ending in ${s.keyLast4}. Leave empty to keep it.` : 'Stored encrypted and never shown again; only your coding-agent apps get it.'}</span>
              {errs.fields.apiKey && <span className="hint err">{errs.fields.apiKey}</span>}
            </div>
            <div className="row">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                Save
              </Button>
              {s.mode === 'own' && (
                <Button type="button" disabled={remove.isPending} onClick={() => remove.mutate()}>
                  Remove my key
                </Button>
              )}
            </div>
          </form>
        </Card>
      )}
    </section>
  )
}
