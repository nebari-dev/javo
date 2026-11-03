import type { Workspace } from '../../api'
import { brand } from '../../lib/brand'
import { Segmented } from '../../components/Segmented'

export type ApiAccess = Workspace['apiAccess']

const LEVELS: { value: ApiAccess; label: string }[] = [
  { value: 'None', label: 'None' },
  { value: 'Read', label: 'Read' },
  { value: 'ReadWrite', label: 'Read and write' },
]
const rank = (a: ApiAccess) => LEVELS.findIndex((l) => l.value === a)

/** The words for an API access level, as the choices show it. */
export const apiAccessLabel = (a: ApiAccess) => LEVELS.find((l) => l.value === a)?.label ?? a

/** None / Read / Read and write, up to the template's max, with what it means for sharing. */
export function ApiAccessChoices({ value, max, onChange }: { value: ApiAccess; max: ApiAccess; onChange: (a: ApiAccess) => void }) {
  return (
    <div className="field fit tight">
      <Segmented
        label="API access"
        value={value}
        onChange={onChange}
        options={LEVELS.map((l) => ({ ...l, disabled: rank(l.value) > rank(max) }))}
      />
      <span className="hint">Gives the app JAVO_URL and JAVO_TOKEN so it can call the {brand().name} API as you.</span>
      {value !== 'None' && <span className="hint">The app acts as you. Anyone you share it with can make it act as you.</span>}
    </div>
  )
}
