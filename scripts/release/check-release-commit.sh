#!/bin/sh
# Refuse to release a commit that is not on main, or whose CI did not pass:
# the commit must be main or an ancestor of it (GitHub's compare API), and
# each named workflow's newest run for a push to main at that exact commit
# must have completed with success. A tag on an unmerged commit, or on a
# commit whose tests failed or are still running, then publishes nothing.
# Uses gh (GH_TOKEN, with actions: read) and jq. Used by
# .github/workflows/release.yaml.
#
# Usage: scripts/release/check-release-commit.sh <commit sha> <workflow file>...
set -eu

sha=${1:-}
shift || true
repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}

if ! printf '%s\n' "$sha" | grep -Eqx '[0-9a-f]{40}'; then
  echo "::error::'$sha' is not a full commit SHA" >&2
  exit 1
fi

# compare/<sha>...main: "identical" or "ahead" (main has everything the
# commit has, and maybe more) means the commit is on main.
status=$(gh api "repos/$repo/compare/$sha...main" | jq -r '.status')
case "$status" in
  identical | ahead) echo "$sha is on main" ;;
  *)
    echo "::error::$sha is not on main (compare with main: $status); tag a commit on main" >&2
    exit 1
    ;;
esac

for wf in "$@"; do
  run=$(gh api "repos/$repo/actions/workflows/$wf/runs?head_sha=$sha&branch=main&event=push&per_page=1" |
    jq -r '.workflow_runs[0] // empty | "\(.status)|\(.conclusion // "")|\(.html_url)"')
  if [ -z "$run" ]; then
    echo "::error::no $wf run for a push to main at $sha" >&2
    exit 1
  fi
  IFS='|' read -r state conclusion url <<EOF
$run
EOF
  if [ "$state" != completed ]; then
    echo "::error::$wf at $sha has not finished ($state, $url); re-run this release once it passes" >&2
    exit 1
  fi
  if [ "$conclusion" != success ]; then
    echo "::error::$wf at $sha did not pass ($conclusion, $url)" >&2
    exit 1
  fi
  echo "$wf passed at $sha ($url)"
done
