import type { SourceBody, Template } from '../../api'
import { Field } from './LaunchFields'
import { More } from '../../components/More'
import { TokenSecretField } from './TokenSecretField'

/** What is typed for a repository, as text: empty means left out. A build field is null until typed, so the template's own shows. */
export type Repo = {
  url: string
  ref: string
  path: string
  /** The user's secret that holds the access token; empty for none. */
  tokenSecret: string
  file: string
  buildCommand: string | null
  buildOutput: string | null
}

export const emptyRepo: Repo = { url: '', ref: '', path: '', tokenSecret: '', file: '', buildCommand: null, buildOutput: null }

/** The request's source: only what was typed, and a build only when it differs from the template's. */
export function sourceBody(tpl: Pick<Template, 'build'>, r: Repo): SourceBody {
  const command = r.buildCommand ?? tpl.build?.command ?? ''
  const output = r.buildOutput ?? tpl.build?.output ?? ''
  const changed = !!tpl.build && (command !== tpl.build.command || output !== tpl.build.output)
  const ref = r.ref.trim()
  const path = r.path.trim()
  return {
    url: r.url.trim(),
    ...(ref ? { ref } : {}),
    ...(path ? { path } : {}),
    ...(r.tokenSecret ? { tokenSecret: r.tokenSecret } : {}),
    ...(changed ? { build: { command: command.trim(), output: output.trim() } } : {}),
  }
}

/** Request fields that sit behind the options disclosure. */
const HIDDEN = ['source.path', 'source.tokenSecret', 'source.build.command', 'source.build.output']

/** The fields for running an app from a Git repository; errors are the API's, by request field. */
export function RepoFields({
  tpl,
  value,
  onChange,
  errors,
  secrets,
}: {
  tpl: Pick<Template, 'displayName' | 'fileExtensions' | 'build'>
  value: Repo
  onChange: (r: Repo) => void
  errors: Record<string, string>
  /** The names of the user's secrets, for the access token. */
  secrets: string[]
}) {
  const set = (patch: Partial<Repo>) => onChange({ ...value, ...patch })
  const exts = tpl.fileExtensions ?? []
  // A template that builds a folder serves it without an app file.
  const asksFile = exts.length > 0 && !tpl.build
  const hiddenError = HIDDEN.some((f) => errors[f])
  const options = tpl.build ? 'Folder, access token and build options' : 'Folder and access token'
  return (
    <>
      <Field
        label="Repository address"
        mono
        value={value.url}
        onChange={(url) => set({ url })}
        placeholder="https://github.com/example-org/site"
        hint="An https address. A private repository also needs an access token, under the options below."
        error={errors['source.url']}
      />
      <Field
        label="Branch, tag or commit"
        mono
        value={value.ref}
        onChange={(ref) => set({ ref })}
        placeholder="default branch"
        hint="Leave it empty for the repository's default branch."
        error={errors['source.ref']}
      />
      {asksFile && (
        <Field
          label="App file"
          mono
          value={value.file}
          onChange={(file) => set({ file })}
          placeholder={`app${exts[0]}`}
          hint={`${tpl.displayName} runs ${exts.join(' or ')} files. Type the path inside the repository folder.`}
          error={errors.file}
        />
      )}
      <More key={hiddenError ? 'open' : 'closed'} defaultOpen={hiddenError} closed={options} open={`Hide ${options.charAt(0).toLowerCase()}${options.slice(1)}`}>
        <Field
          label="Folder in the repository"
          mono
          value={value.path}
          onChange={(path) => set({ path })}
          placeholder="web"
          hint="Leave it empty to use the root of the repository."
          error={errors['source.path']}
        />
        <TokenSecretField
          value={value.tokenSecret}
          onChange={(tokenSecret) => set({ tokenSecret })}
          available={secrets}
          hint="Only needed for a private repository: the secret that holds its token. The clone reads it; it is never shown."
          error={errors['source.tokenSecret']}
        />
        {tpl.build && (
          <>
            <Field
              label="Build command"
              mono
              value={value.buildCommand ?? tpl.build.command}
              onChange={(buildCommand) => set({ buildCommand })}
              hint="Runs in the repository folder before the site is served."
              error={errors['source.build.command']}
            />
            <Field
              label="Output folder"
              mono
              value={value.buildOutput ?? tpl.build.output}
              onChange={(buildOutput) => set({ buildOutput })}
              hint="The folder the build writes, inside the repository folder."
              error={errors['source.build.output']}
            />
          </>
        )}
      </More>
    </>
  )
}
