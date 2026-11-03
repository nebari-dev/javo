import * as RS from '@radix-ui/react-select'
import { Check, ChevronDown, ChevronUp } from 'lucide-react'
import { Children, isValidElement, type ComponentPropsWithoutRef, type ReactElement, type ReactNode } from 'react'

interface SelectProps extends Omit<ComponentPropsWithoutRef<typeof RS.Trigger>, 'value' | 'defaultValue' | 'children' | 'dir'> {
  value: string | number
  onValueChange: (value: string) => void
  /** Sent with the form the select is in, under this name. */
  name?: string
  /** The choices, as `<option value>label</option>` elements. */
  children: ReactNode
}

type OptionElement = ReactElement<{ value?: string | number; disabled?: boolean; children?: ReactNode }>

// Radix keeps the empty string for "nothing chosen"; an option whose value is
// empty ("None", "Everyone") goes by this name inside instead.
const EMPTY = '\u0000empty'
const inner = (v: string | number) => (String(v) === '' ? EMPTY : String(v))
const outer = (v: string) => (v === EMPTY ? '' : v)
// An option without a value goes by its text, as in a native select.
const valueOf = (o: OptionElement) => inner(o.props.value ?? (typeof o.props.children === 'string' ? o.props.children : ''))

/**
 * A dropdown drawn like the text inputs (Radix Select, the base of shadcn's):
 * a trigger with a chevron opens a list below it, with a check on the chosen
 * option, arrow keys, typeahead and Escape. The choices are written as
 * `<option>` children, as for a native select.
 */
export function Select({ value, onValueChange, name, disabled, className, children, ...trigger }: SelectProps) {
  const options = Children.toArray(children).filter((c): c is OptionElement => isValidElement(c) && c.type === 'option')
  return (
    <>
      <RS.Root value={inner(value)} onValueChange={(v) => onValueChange(outer(v))} disabled={disabled}>
        <RS.Trigger className={`input select-trigger${className ? ` ${className}` : ''}`} {...trigger}>
          <span className="select-value">
            <RS.Value />
          </span>
          <RS.Icon className="select-chev">
            <ChevronDown size={16} aria-hidden="true" />
          </RS.Icon>
        </RS.Trigger>
        <RS.Portal>
          <RS.Content className="select-pop" position="popper" sideOffset={6} collisionPadding={8}>
            <RS.ScrollUpButton className="select-scroll">
              <ChevronUp size={14} aria-hidden="true" />
            </RS.ScrollUpButton>
            <RS.Viewport className="select-list">
              {options.map((o) => (
                <RS.Item key={valueOf(o)} value={valueOf(o)} disabled={o.props.disabled} className="select-item">
                  <RS.ItemText>{o.props.children}</RS.ItemText>
                  <RS.ItemIndicator className="select-check">
                    <Check size={15} aria-hidden="true" />
                  </RS.ItemIndicator>
                </RS.Item>
              ))}
            </RS.Viewport>
            <RS.ScrollDownButton className="select-scroll">
              <ChevronDown size={14} aria-hidden="true" />
            </RS.ScrollDownButton>
          </RS.Content>
        </RS.Portal>
      </RS.Root>
      {name && <input type="hidden" name={name} value={String(value)} />}
    </>
  )
}
