import { useId } from 'react'
import type { Template } from '../../api'

/** What an app runs beyond its template, as typed: port is text until sent. */
export type Launch = { command: string; image: string; port: string }

/** The request fields for launch: the command when typed, and for an image template the image and port. */
export function launchBody(tpl: Pick<Template, 'userCommand' | 'userImage'>, l: Launch) {
  const command = l.command.trim()
  return {
    ...(tpl.userCommand !== 'None' && command ? { command } : {}),
    // A port that is not a number goes as null, so the API names the field.
    ...(tpl.userImage ? { image: l.image.trim(), ...(l.port.trim() ? { port: Number(l.port) } : {}) } : {}),
  }
}

const PLACEHOLDERS = '{{.Port}}, {{.BaseURL}}, {{.Home}} and {{.File}}'

/** A labelled text input with a hint and the API's error; mono for commands and images. */
export function Field(props: {
  label: string
  value: string
  onChange: (v: string) => void
  hint: string
  error?: string
  mono?: boolean
  placeholder?: string
  numeric?: boolean
  /** A password input that the browser neither fills in nor offers to save, for tokens. */
  secret?: boolean
  /** A box of several lines, for a script. */
  multiline?: boolean
}) {
  const ids = { input: useId(), hint: useId(), err: useId() }
  const shared = {
    id: ids.input,
    className: `input${props.mono ? ' mono' : ''}${props.multiline ? ' script' : ''}${props.error ? ' err' : ''}`,
    value: props.value,
    placeholder: props.placeholder,
    spellCheck: false,
    autoCapitalize: 'off',
    autoComplete: props.secret ? 'new-password' : 'off',
    'aria-invalid': props.error ? true : undefined,
    'aria-describedby': props.error ? `${ids.hint} ${ids.err}` : ids.hint,
  } as const
  return (
    <div className="field">
      <label htmlFor={ids.input}>{props.label}</label>
      {props.multiline ? (
        <textarea {...shared} rows={5} onChange={(e) => props.onChange(e.target.value)} />
      ) : (
        <input
          {...shared}
          type={props.secret ? 'password' : undefined}
          inputMode={props.numeric ? 'numeric' : undefined}
          onChange={(e) => props.onChange(e.target.value)}
        />
      )}
      <span id={ids.hint} className="hint">
        {props.hint}
      </span>
      {props.error && (
        <span id={ids.err} className="errmsg">
          {props.error}
        </span>
      )}
    </div>
  )
}

/** The image, port and command fields a template asks for; errors are the API's, by request field. */
export function LaunchFields({
  tpl,
  value,
  onChange,
  errors,
}: {
  tpl: Pick<Template, 'userCommand' | 'userImage' | 'fromRepo'>
  value: Launch
  onChange: (v: Launch) => void
  errors: Record<string, string | undefined>
}) {
  const set = (k: keyof Launch) => (v: string) => onChange({ ...value, [k]: v })
  return (
    <>
      {tpl.userImage && (
        <>
          <Field
            label="Image"
            mono
            value={value.image}
            onChange={set('image')}
            placeholder="registry.example.com/team/app:1.0"
            hint="It runs as the user the image names, never as root."
            error={errors.image}
          />
          <Field label="Port" numeric value={value.port} onChange={set('port')} hint="The port the image serves on." error={errors.port} />
        </>
      )}
      {tpl.userCommand !== 'None' && (
        <Field
          label={tpl.userCommand === 'Optional' ? 'Command (optional)' : 'Command'}
          mono
          value={value.command}
          onChange={set('command')}
          placeholder={tpl.userImage ? '' : 'python app.py --port={{.Port}}'}
          hint={`${tpl.userImage ? "Empty runs the image's own command. " : ''}Fills in ${PLACEHOLDERS}. Runs without a shell.${
            tpl.fromRepo === 'None' ? '' : ' The server must listen on the port in $PORT, the number {{.Port}} stands for.'
          }`}
          error={errors.command}
        />
      )}
    </>
  )
}
