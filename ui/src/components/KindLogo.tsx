import { Container, Globe, House, SquareTerminal, type LucideIcon } from 'lucide-react'
import bokeh from '../assets/logos/bokeh.svg'
import claudeCode from '../assets/logos/claudecode.svg'
import codeServer from '../assets/logos/code-server.svg'
import fastapi from '../assets/logos/fastapi.svg'
import git from '../assets/logos/git.svg'
import gradio from '../assets/logos/gradio.svg'
import golang from '../assets/logos/go.svg'
import julia from '../assets/logos/julia.svg'
import nodejs from '../assets/logos/nodedotjs.svg'
import python from '../assets/logos/python.svg'
import rlang from '../assets/logos/r.svg'
import herdr from '../assets/logos/herdr.svg'
import jupyter from '../assets/logos/jupyter.svg'
import marimo from '../assets/logos/marimo.svg'
import panel from '../assets/logos/panel.svg'
import pi from '../assets/logos/pi.svg'
import pluto from '../assets/logos/pluto.svg'
import plotly from '../assets/logos/plotly.svg'
import rstudio from '../assets/logos/rstudioide.svg'
import streamlit from '../assets/logos/streamlit.svg'
import voila from '../assets/logos/voila.svg'

// Project logos vendored from pinned sources; ui/LOGOS.md records each one.
// A logo with a tint is a one-colour mark drawn in that colour; the others
// keep their own colours. scale evens out visual weight inside the tile.
type Logo = { src: string; tint?: string; scale?: number }

const LOGOS: Record<string, Logo> = {
  jupyterlab: { src: jupyter, tint: '#f37626' },
  streamlit: { src: streamlit, tint: '#ff4b4b' },
  fastapi: { src: fastapi, tint: '#009688' },
  dash: { src: plotly, tint: '#7a76ff' },
  gradio: { src: gradio, tint: '#f97316' },
  rstudio: { src: rstudio, tint: '#75aadb' },
  marimo: { src: marimo, tint: 'var(--logo-marimo)' },
  pluto: { src: pluto },
  vscode: { src: codeServer, tint: 'var(--text)' },
  panel: { src: panel, scale: 0.9 },
  bokeh: { src: bokeh, scale: 1.1 },
  voila: { src: voila, scale: 1.15 },
  'claude-code': { src: claudeCode, tint: '#d97757' },
  pi: { src: pi },
  herdr: { src: herdr, tint: 'var(--text)' },
  python: { src: python, tint: '#3776ab' },
  // Not a template kind: the Git repository choice on New app.
  git: { src: git, tint: '#f03c2e' },
  julia: { src: julia, tint: '#9558b2' },
  r: { src: rlang, tint: '#276dc3' },
  go: { src: golang, tint: '#00add8', scale: 1.2 },
  node: { src: nodejs, tint: '#5fa04e' },
}

// Kinds that are no project of their own get a line icon.
const ICONS: Record<string, LucideIcon> = { container: Container, static: Globe, home: House }

/** The app kind's mark: the project's logo, a line icon for kinds without one, or a neutral terminal for custom and unknown kinds. */
export function KindLogo({ kind, big = false }: { kind: string; big?: boolean }) {
  const size = big ? 28 : 21
  const logo = LOGOS[kind]
  if (!logo) {
    const Icon = ICONS[kind] ?? SquareTerminal
    return (
      <div className="logo" aria-hidden="true">
        <Icon size={size} />
      </div>
    )
  }
  const px = Math.round(size * (logo.scale ?? 1))
  return (
    <div className="logo" aria-hidden="true">
      {logo.tint ? (
        <span
          className="mark"
          style={{ width: px, height: px, backgroundColor: logo.tint, maskImage: `url("${logo.src}")` }}
        />
      ) : (
        <img className="mark" src={logo.src} alt="" width={px} height={px} />
      )}
    </div>
  )
}
