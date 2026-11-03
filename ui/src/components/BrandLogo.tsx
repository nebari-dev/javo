import { brand } from '../lib/brand'
import { JavoMark } from './JavoMark'

/** The install's logo, with the dark theme's own when it has one; the Javo mark without branding. */
export function BrandLogo() {
  const { logoLight, logoDark } = brand()
  if (!logoLight) return <JavoMark />
  if (!logoDark) return <img className="brand-logo" src={logoLight} alt="" />
  return (
    <>
      <img className="brand-logo brand-logo-light" src={logoLight} alt="" />
      <img className="brand-logo brand-logo-dark" src={logoDark} alt="" />
    </>
  )
}

/**
 * The install's name beside its logo. A custom logo carries the name itself,
 * so then the name is kept for screen readers only and not shown twice.
 */
export function BrandName() {
  const { name, logoLight } = brand()
  return <div className={logoLight ? 'brand-name sr-only' : 'brand-name'}>{name}</div>
}
