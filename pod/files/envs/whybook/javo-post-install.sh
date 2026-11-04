# The platform's settings for JupyterLab with Whybook, put where JupyterLab
# reads them in this environment: overrides.json (notebooks open in Whybook;
# Whybook asks where AI models run before the first call to one, not when
# the first notebook opens; no news or update checks), jupyter_lab_config.json
# (no update check) and jupyter_server_config.json (idle kernels and
# terminals are shut down after 15 minutes, as in JupyterLab's environment).
# Whybook's "Python 3 (sandboxed)" kernel is removed: it needs bubblewrap,
# which the pod does not have. internal/pixienv runs this file in the
# activated environment, once per lockfile.
set -euo pipefail
mkdir -p "$CONDA_PREFIX/share/jupyter/lab/settings" "$CONDA_PREFIX/etc/jupyter"
cp overrides.json "$CONDA_PREFIX/share/jupyter/lab/settings/overrides.json"
cp jupyter_lab_config.json jupyter_server_config.json "$CONDA_PREFIX/etc/jupyter/"
jupyter kernelspec remove -y python3-sandboxed
