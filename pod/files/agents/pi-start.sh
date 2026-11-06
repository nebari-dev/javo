#!/bin/sh
# The pi template's terminal command, run by tmux in the agent's window: point
# pi at the app's model (pi-setup.mjs), then start pi, with the app's first
# prompt on the app's first start only. The prompt is passed as one argument
# from the variable, never put into a command line. Quitting pi leaves a shell.
node /opt/javo/agents/pi-setup.mjs
# pi's colours: its light or dark palette unless the person picked a theme.
theme=$(node /opt/javo/agents/pi-setup.mjs theme-args 2>/dev/null || true)
# Every later start, a pod restart included, reopens pi's most recent session
# in this folder (--continue, pi 1.4.0 main.js and SessionManager.continueRecent:
# a new session when the folder has none), so the conversation is still there.
# pi saves a session from its first user message, the first prompt included,
# so a prompt pi received before the pod went away comes back with it.
if [ -n "${JAVO_AGENT_PROMPT:-}" ] && node /opt/javo/agents/pi-setup.mjs first-start; then
  # shellcheck disable=SC2086
  pi $theme -- "$JAVO_AGENT_PROMPT"
else
  # shellcheck disable=SC2086
  pi $theme --continue
fi
exec bash
