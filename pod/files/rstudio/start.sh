#!/bin/sh
# Starts RStudio Server as the pod user, through /opt/javo/pod-user.sh; the
# pod's /etc/passwd names the uid (internal/podfiles). The server reads its
# settings and a secret key (shared with the R sessions it starts) from
# RSTUDIO_CONFIG_DIR; the platform's settings (the .conf files beside this
# script) are copied to /tmp, which the user can write, and the key is
# generated fresh for every start.
set -eu
cfg=/tmp/rstudio-config
mkdir -p "$cfg" /tmp/rstudio-server-data /tmp/rstudio-server-db
cp /opt/javo/rstudio/*.conf "$cfg/"
(umask 077 && od -An -tx1 -N32 /dev/urandom | tr -d ' \n' > "$cfg/session-rpc-key")
RSTUDIO_CONFIG_DIR=$cfg
export RSTUDIO_CONFIG_DIR
# Without root, rserver runs everything as itself and requires server-user to
# name the uid it runs as: the name the pod's passwd gives it. auth-none signs
# everyone in as the user named in USER, and that user's passwd entry gives
# the R session its home.
USER=$(id -un)
export USER
# Packages the user installs go to the home, which persists.
R_LIBS_USER=$HOME/R/library
export R_LIBS_USER
mkdir -p "$R_LIBS_USER"
exec rserver --server-user="$USER" "$@"
