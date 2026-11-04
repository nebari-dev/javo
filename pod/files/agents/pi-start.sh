#!/bin/sh
# The pi template's terminal command, run by tmux in the agent's window: point
# pi at the app's model (pi-setup.mjs), then start pi, with the app's first
# prompt on the app's first start only. The prompt is passed as one argument
# from the variable, never put into a command line. Quitting pi leaves a shell.
node /opt/javo/agents/pi-setup.mjs
if [ -n "${JAVO_AGENT_PROMPT:-}" ] && node /opt/javo/agents/pi-setup.mjs first-start; then
  pi -- "$JAVO_AGENT_PROMPT"
else
  pi
fi
exec bash
