import type { JobParam } from '../../api'
import { CodeBlock } from '../../components/CodeBlock'

/** The variable a command or bash job reads a parameter from, named as the command engine names it. */
const envName = (name: string) => 'JAVO_PARAM_' + name.replaceAll('-', '_').replaceAll('.', '_').toUpperCase()

/** The value as Python writes it, as papermill and Quarto set it. */
function pyValue(p: JobParam) {
  const v = p.default ?? ''
  if (p.type === 'string') return JSON.stringify(v)
  if (p.type === 'boolean') return ['1', 't', 'true'].includes(v.toLowerCase()) ? 'True' : 'False'
  return v || '0'
}

const ENV_RULE = 'an environment variable: JAVO_PARAM_ plus the name in capitals, with - and . turned into _.'

/** How the values reach the code for each engine, in words and one example. */
function explain(engine: string | undefined, p: JobParam): { text: string; code?: string; more?: string } {
  switch (engine) {
    case 'command':
      return {
        text: `Your command gets each parameter as ${ENV_RULE} It runs without a shell, so read the variable in your program, for example in Python:`,
        code: `os.environ["${envName(p.name)}"]`,
        more: 'JAVO_PARAMS_FILE holds the path of a YAML file with every value.',
      }
    case 'bash':
      return { text: `Your script gets each parameter as ${ENV_RULE} For example:`, code: `echo "$${envName(p.name)}"` }
    case 'papermill':
      return {
        text: 'Each parameter becomes a Python variable in the notebook. Tag one cell parameters and set the defaults there; each run, papermill adds a cell right after it that sets the values for that run (at the top when no cell is tagged), like:',
        code: `${p.name} = ${pyValue(p)}`,
      }
    case 'quarto':
      return {
        text: 'Each parameter becomes a variable in the document. In a Python document, tag one code cell parameters and set the defaults there; each run, Quarto sets the values for that run right after it:',
        code: `#| tags: [parameters]\n${p.name} = ${pyValue(p)}`,
      }
    case 'marimo':
      return {
        text: `The notebook gets each parameter on its command line, as --${p.name} ${p.default ?? ''}. Read it with mo.cli_args():`,
        code: `${p.name} = mo.cli_args().get("${p.name}")`,
      }
    default:
      return { text: 'Choose a file to see how it gets the values.' }
  }
}

/** What a parameter is and how its value reaches the job's code, with an example built from `param`. */
export function ParamHelp({ engine, param }: { engine?: string; param: JobParam }) {
  const { text, code, more } = explain(engine, param)
  return (
    <div className="param-help">
      <p className="muted small tight">Parameters are named values your code reads, so one job can run with different inputs. {text}</p>
      {code && <CodeBlock text={code} label="Copy example" />}
      {more && <p className="muted small tight">{more}</p>}
      <p className="muted small tight">These are default values. Run now uses them; a run started from the API can give other values for that run only.</p>
    </div>
  )
}
