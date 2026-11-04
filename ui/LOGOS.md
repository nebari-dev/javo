# Template logos

`src/components/KindLogo.tsx` maps each WorkspaceTemplate kind to an SVG vendored under `src/assets/logos/`.
Nothing is fetched at runtime. Files are copied verbatim unless noted. One-colour marks are drawn in the
brand colour listed (via CSS mask); the others keep their own colours. Copyright notices and license
texts for the logos that need them are in [src/assets/logos/NOTICES.md](src/assets/logos/NOTICES.md).

Simple Icons files come from npm `simple-icons@16.35.0` (CC0 1.0), tarball
`https://registry.npmjs.org/simple-icons/-/simple-icons-16.35.0.tgz`,
integrity `sha512-TCfl/2+GTrx0nCo4pEWGvxhOb8c6NsqOU1OpwjtF4lwJAthB8IYVD0fuIMjLMg6JFuG1YBO0t1iETQqJLqkoJA==`, path `icons/<slug>.svg`.
Simple Icons has no mark for Panel, Bokeh, Voila, marimo or Pluto.jl; its `dash` icon is the
Dash cryptocurrency, not Plotly Dash; it has no Visual Studio Code icon.

| Kind | File | Source | License | Colour |
|---|---|---|---|---|
| jupyterlab | jupyter.svg | Simple Icons `jupyter` | CC0 1.0 | #f37626 |
| streamlit | streamlit.svg | Simple Icons `streamlit` | CC0 1.0 | #ff4b4b |
| fastapi | fastapi.svg | Simple Icons `fastapi` | CC0 1.0 | #009688 |
| dash | plotly.svg | Simple Icons `plotly` (plotly/dash ships no SVG logo of its own) | CC0 1.0 | #7a76ff |
| gradio | gradio.svg | Simple Icons `gradio` | CC0 1.0 | #f97316 |
| rstudio | rstudioide.svg | Simple Icons `rstudioide` (RStudio IDE) | CC0 1.0 | #75aadb |
| git (source choice, not a kind) | git.svg | Simple Icons `git` | CC0 1.0 | #f03c2e |
| python | python.svg | Simple Icons `python` | CC0 1.0 | #3776ab |
| julia | julia.svg | Simple Icons `julia` | CC0 1.0 | #9558b2 |
| r | r.svg | Simple Icons `r` | CC0 1.0 | #276dc3 |
| go | go.svg | Simple Icons `go` | CC0 1.0 | #00add8 |
| node | nodedotjs.svg | Simple Icons `nodedotjs` | CC0 1.0 | #5fa04e |
| panel | panel.svg | https://github.com/holoviz/panel/blob/0cdecfd06cb15c88396c4440427acbada8729d0b/doc/_static/icons/icon-vector.svg | BSD-3-Clause | own |
| bokeh | bokeh.svg | https://github.com/bokeh/bokeh/blob/4176d3f2f650f98f053c363b0cd404991223b846/bokehjs/test/assets/images/logo.svg | BSD-3-Clause | own |
| voila | voila.svg | https://github.com/voila-dashboards/voila/blob/2993413ffb950476ff7b44abf84fe56f656e7772/packages/jupyterlab-preview/style/voila.svg | BSD-3-Clause | own (1) |
| marimo | marimo.svg | https://github.com/marimo-team/marimo-lsp/blob/72c00ce7e9bd4823e52552e266957f41c0b49c1b/extension/icons/marimo-icon-light.svg | Apache-2.0 | #1c7361 light (from marimo's logotype), #cccccc dark (from marimo-icon-dark.svg) |
| pluto | pluto.svg | https://github.com/JuliaPluto/Pluto.jl/blob/1d86801cc30629a38ed9775c27e3c7b2acd58fca/frontend/img/favicon.svg (tag v1.1.0) | MIT | own |
| claude-code | claudecode.svg | Simple Icons `claudecode` (Claude Code) | CC0 1.0 | #d97757 |
| pi | pi.svg | pi's own mark, transcribed (2) from https://github.com/earendil-works/pi/blob/71023aec1546d6d677a65669c5665c0927ce0dd0/packages/coding-agent/src/modes/interactive/components/pi-logo.ts | MIT | own |
| herdr | herdr.svg | https://github.com/herdrdev/herdr/blob/2ecf19a92fc3a7fff75b6ce3655ef04f4625ec01/assets/logo.svg (tag v0.9.5) | Apache-2.0 | text colour (3) |
| vscode | vscode.png | https://github.com/nebari-dev/jhub-apps/blob/ff5faa9ef2bb9171e7fd650446bbd368a43a22fa/jhub_apps/static/img/logos/vscode.png | BSD-3-Clause | text colour (4) |
| whybook | whybook.svg | Whybook's own icon (the `svgstr` of `epiIcon`, copied verbatim) from https://github.com/openteams-ai/whybook/blob/9c486c5cda9f52679253e347bead3c07950fbb61/src/icons.ts (tag v0.5.1) | Apache-2.0 | own |
| custom | none | Lucide `square-terminal` (not a product) | ISC | text colour |

(1) The Voila icon colours its two paths through JupyterLab theme classes that do not apply outside JupyterLab.
The vendored copy drops the `<style>` block and writes the light-theme colours (#268380, #f8e14b) as `fill`
attributes; the paths are unchanged.

(2) pi ships no logo file. Its mark is defined in code (`pi-logo.ts`, tag v1.3.0) as a 4 by 4 grid of square
pixels drawn with half blocks in the terminal: coral rgb(228,138,122), blue rgb(79,142,179) and yellow
rgb(234,182,93). `pi.svg` draws the same grid and colours as rectangles; nothing else is added.

(3) herdr's logo is a dark mark (#303438) on a light grey square. The vendored copy drops the square (one
`<rect>` line) so the mark can be drawn in the text colour in light and dark mode; the path is unchanged.

(4) The vscode kind uses the VS Code logo from jhub-apps, the app launcher Nebari used before. The source is
an 858 by 390 banner with a black mark on a light grey gradient. The vendored copy is cropped to a 243 by 243
square around the mark, and each pixel's darkness becomes its opacity, so the background is transparent and
the mark can be drawn in the text colour in light and dark mode. The shape is unchanged.
