#!/usr/bin/env bash
# Makes this folder a git repository with one commit, then changes a file,
# so the prompt shows the branch and the changed mark. Safe to run again.
set -euo pipefail
cd "$(dirname "$0")"
if [ ! -d .git ]; then
  git init -q -b main
  git add README.md tour.sh
  git commit -q -m "Start the tour"
fi
date >>README.md
echo "README.md changed: the prompt now marks the branch with *."
echo "git diff shows the change; git checkout README.md undoes it."
