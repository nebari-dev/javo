# Claude Code is published on npm only (not on conda-forge). npm installs it
# into this environment from package-lock.json, which pins every package and
# its integrity hash, without running install scripts; Claude Code's own
# installer then links the native binary for this machine from the platform
# packages in the lock. The env's node runs both (internal/pixienv runs this
# file in the activated environment, once per lockfile).
set -euo pipefail
npm ci --ignore-scripts --omit=dev --no-audit --no-fund --no-update-notifier --cache "$PWD/.npm-cache"
node node_modules/@anthropic-ai/claude-code/install.cjs
rm -rf .npm-cache
ln -sf "$PWD/node_modules/.bin/claude" "$CONDA_PREFIX/bin/claude"
