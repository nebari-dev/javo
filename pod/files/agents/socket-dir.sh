#!/bin/sh
# Names the folder a program of an app that runs as a process of someone's
# machine keeps its socket in: socket-dir.sh FOLDER NAME prints FOLDER, a
# folder of the workload's own, when a socket called NAME fits in it, and
# a new folder otherwise.
#
# The path of a socket holds 103 bytes on macOS and 107 on Linux, and the
# state directory of the machine may be deeper than that (programs count
# the path with the folder's links resolved). The new folder is in /tmp
# and only this user can open it; it stays, empty, after the app stopped.
set -eu
real=$(cd "$1" && pwd -P)
if [ "$(printf %s "$real/$2" | wc -c)" -le 103 ]; then
  printf '%s\n' "$1"
else
  mktemp -d /tmp/javo-sock.XXXXXXXX
fi
