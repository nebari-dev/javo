import { Menu, PanelLeftClose, PanelLeftOpen, Plus } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { brand } from '../lib/brand'
import { CommandPalette } from '../features/palette/CommandPalette'
import { Banner } from '../components/Banner'
import { Button } from '../components/Button'
import { PART_ICONS } from '../components/Section'
import { useIdpOutage } from '../lib/idp'
import { ThemeSwitch } from '../components/ThemeSwitch'
import { UserMenu } from './UserMenu'
import { features } from '../features'
import { useLogout, useMe, useServerInfo, useWarmHome } from '../lib/queries'
import { buildLink } from '../lib/github'
import { SlidingNav } from './NavHover'
import { BrandLogo, BrandName } from '../components/BrandLogo'
import { readStored, writeStored } from '../lib/storage'

const SIDEBAR_KEY = 'javo.sidebar'

export function AppShell() {
  const navigate = useNavigate()
  const logout = useLogout()
  const me = useMe().data
  // Signed in: the file picker may be opened any moment.
  useWarmHome(!!me)
  const idpOutage = useIdpOutage()
  const info = useServerInfo().data
  const build = info?.version ? buildLink(info) : null
  const [collapsed, setCollapsed] = useState(() => readStored(SIDEBAR_KEY) === 'collapsed')
  const toggle = () => {
    setCollapsed(!collapsed)
    writeStored(SIDEBAR_KEY, collapsed ? 'open' : 'collapsed')
  }
  const ToggleIcon = collapsed ? PanelLeftOpen : PanelLeftClose
  // On small screens the sidebar is a drawer behind the menu button; a
  // choice, Escape or the backdrop closes it.
  const [navOpen, setNavOpen] = useState(false)
  const { pathname } = useLocation()
  useEffect(() => {
    setNavOpen(false)
  }, [pathname])
  useEffect(() => {
    if (!navOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setNavOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navOpen])
  return (
    <div className={`app${collapsed ? ' collapsed' : ''}`}>
      <aside id="side-nav" className={`side${navOpen ? ' open' : ''}`}>
        <div className="brand">
          <Link to="/" className="brand-home" aria-label={`${brand().name} home`} title="Home">
            <BrandLogo />
            <div className="brand-text">
              <BrandName />
            </div>
          </Link>
          <button
            type="button"
            className="collapse-btn"
            aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            aria-expanded={!collapsed}
            onClick={toggle}
          >
            <ToggleIcon size={18} strokeWidth={1.75} aria-hidden="true" />
          </button>
        </div>
        <SlidingNav className="nav" label="Main">
          <NavLink to="/" end aria-label="Home" title="Home">
            <PART_ICONS.home size={17} aria-hidden="true" />
            <span className="lbl">Home</span>
          </NavLink>
          <NavLink to="/apps" end aria-label="Apps" title="Apps">
            <PART_ICONS.apps size={17} aria-hidden="true" />
            <span className="lbl">Apps</span>
          </NavLink>
          <NavLink to="/templates" aria-label="Templates" title="Templates">
            <PART_ICONS.templates size={17} aria-hidden="true" />
            <span className="lbl">Templates</span>
          </NavLink>
          {features.flatMap((f) => (f.nav ? [f.nav] : [])).map(({ to, label, icon: Icon }) => (
            <NavLink key={to} to={to} aria-label={label} title={label}>
              <Icon size={17} aria-hidden="true" />
              <span className="lbl">{label}</span>
            </NavLink>
          ))}
        </SlidingNav>
        {build && (
          <a className="side-version" href={build.href} target="_blank" rel="noopener noreferrer" title="This build on GitHub">
            {build.label}
          </a>
        )}
        <SlidingNav className="nav nav-foot" label="Settings and help">
          <NavLink to="/settings" aria-label="Settings" title="Settings">
            <PART_ICONS.settings size={17} aria-hidden="true" />
            <span className="lbl">Settings</span>
          </NavLink>
        </SlidingNav>
      </aside>
      {navOpen && <div className="nav-backdrop" data-testid="nav-backdrop" aria-hidden="true" onClick={() => setNavOpen(false)} />}
      <div className="app-col">
        <header className="top">
          <button
            type="button"
            className="icon-btn menu-btn"
            aria-label="Open menu"
            aria-controls="side-nav"
            aria-expanded={navOpen}
            onClick={() => setNavOpen(!navOpen)}
          >
            <Menu size={20} strokeWidth={1.75} aria-hidden="true" />
          </button>
          <CommandPalette />
          <div className="top-actions">
            <ThemeSwitch />
            <Button variant="primary" icon={Plus} className="new-app" aria-label="New app" onClick={() => navigate('/apps/new')}>
              <span className="new-app-label">New app</span>
            </Button>
            {me && (
              <UserMenu
                name={me.displayName || me.username}
                picture={me.picture}
                onSignOut={() => logout.mutate(undefined, { onSettled: () => navigate('/login?signedOut=1') })}
              />
            )}
          </div>
        </header>
        {idpOutage && (
          <div className="idp-banner">
            <Banner tone="warn" title={idpOutage} />
          </div>
        )}
        <main className="main">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
