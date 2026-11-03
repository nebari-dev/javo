#!/usr/bin/env bash
# The sample job of the jobs-python template ("Print the time"): prints the
# date and time and what a run sees. javo runs it as a command job,
#   bash /opt/javo/samples/jobs/print-time.sh
# in the person's home folder, with each parameter as JAVO_PARAM_<NAME>.
set -euo pipefail
echo "The time is $(date '+%Y-%m-%d %H:%M:%S %Z')"
echo "Host: ${HOSTNAME:-unknown}"
# A run has the owner's uid; the pod's /etc/passwd names it.
uid=$(id -u)
if getent passwd "$uid" >/dev/null; then echo "User: $(id -un) (uid $uid)"; else echo "User: uid $uid"; fi
echo "Folder: $PWD"
echo "Greeting: ${JAVO_PARAM_GREETING:-no greeting parameter}"
