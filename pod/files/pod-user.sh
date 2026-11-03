#!/bin/bash
# Runs a command as the pod's user, in the pod's environment. Templates with
# pod files start their command through it (internal/podfiles copies this
# folder to /opt/javo), and the job runner starts each step through it.
#
# The uid already has a name: the pod's /etc/passwd lists it (podfiles writes
# one with an entry for the pod's uid, named after JAVO_USER). Here USER and
# LOGNAME are set to that name and EMAIL (git's fallback when user.email is
# not configured) to name@host unless already set.
#
# An app or job that runs in a pixi environment (internal/pixienv) has its
# activation at /opt/javo-env/activate.sh: pixi's own shell hook for the
# installed environment, so its tools come first on PATH and its activation
# scripts (Julia's depot, certificates) run.
set -e
if [ -r /opt/javo-env/activate.sh ]; then
  # shellcheck disable=SC1091
  . /opt/javo-env/activate.sh
fi
USER=$(id -un 2>/dev/null || echo user)
LOGNAME=$USER
EMAIL=${EMAIL:-$USER@$(cat /proc/sys/kernel/hostname)}
SHELL=${SHELL:-/bin/bash}
# The platform's own tools (javo-api) come last.
PATH=$PATH:/opt/javo/bin
export USER LOGNAME EMAIL SHELL PATH
exec "$@"
