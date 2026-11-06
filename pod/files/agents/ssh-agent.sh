#!/bin/sh
# ssh-agent.sh SSH_DIR PUB_DIR SOCKET: the coding agent apps' ssh-agent
# sidecar. Copies the public files of SSH_DIR (the owner's ~/.ssh, mounted
# read-only) into PUB_DIR (the agent container's ~/.ssh: ssh-public.sh),
# runs ssh-agent on SOCKET and loads every private key id_* that needs no
# passphrase. A key with a passphrase is skipped with a line in the log:
# nothing here waits for input. Then touches "ready" beside SOCKET (the
# sidecar's startup probe) and keeps the agent running. Without ssh-agent on
# PATH (an environment without openssh) it says so and stays up idle, so the
# app still starts.
set -u
src=$1
pub=$2
sock=$3
ready=${sock%/*}/ready
here=${0%/*}
log() { echo "ssh-agent.sh: $*" >&2; }

rm -f "$ready"
sh "$here/ssh-public.sh" "$src" "$pub" || log "could not copy the public files of $src"
if ! command -v ssh-agent >/dev/null || ! command -v ssh-add >/dev/null; then
  log "no ssh-agent in this environment: SSH keys are not available to the agent"
  touch "$ready"
  idle=
  trap 'kill $idle 2>/dev/null; exit 0' TERM INT
  while :; do
    sleep 3600 &
    idle=$!
    wait "$idle"
  done
fi

# A socket left by an earlier run of this container in the same pod.
rm -f "$sock"
ssh-agent -D -a "$sock" >/dev/null &
agent=$!
trap 'kill "$agent" 2>/dev/null; exit 0' TERM INT
i=0
while [ ! -S "$sock" ] && [ "$i" -lt 50 ]; do
  sleep 0.1
  i=$((i + 1))
done
SSH_AUTH_SOCK=$sock
export SSH_AUTH_SOCK
# ssh-add reads each key from stdin: it refuses a key file that others may
# read, and shared storage may make it so. An askpass that fails stands in
# for the passphrase prompt, so a protected key is skipped, not waited on.
SSH_ASKPASS=$(command -v false)
SSH_ASKPASS_REQUIRE=force
export SSH_ASKPASS SSH_ASKPASS_REQUIRE
loaded=0
for key in "$src"/id_*; do
  case $key in *.pub) continue ;; esac
  [ -f "$key" ] || continue
  if ssh-add -q - < "$key" 2>/dev/null; then
    loaded=$((loaded + 1))
  else
    log "skipped ${key##*/}: it needs a passphrase or is not a key ssh-add reads"
  fi
done
log "loaded $loaded key(s) from $src into the agent at $sock"
touch "$ready"
wait "$agent"
