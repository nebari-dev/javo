#!/bin/sh
# The pi template's terminal command, run by tmux in the agent's window: point
# pi at the app's model (pi-setup.mjs), then start pi, with the app's first
# prompt on the app's first start only. The prompt is passed as one argument
# from the variable, never put into a command line. Quitting pi leaves a shell.
node /opt/javo/agents/pi-setup.mjs
# pi's colours: its light or dark palette unless the person picked a theme.
theme=$(node /opt/javo/agents/pi-setup.mjs theme-args 2>/dev/null || true)
if [ -n "${JAVO_AGENT_PROMPT:-}" ] && node /opt/javo/agents/pi-setup.mjs first-start; then
  # shellcheck disable=SC2086
  pi $theme -- "$JAVO_AGENT_PROMPT"
else
  # shellcheck disable=SC2086
  pi $theme
fi
exec bash
