# The platform's JupyterLab settings, put where JupyterLab reads them in this
# environment: overrides.json (Fira Code for code and the terminal, no news
# or update checks), jupyter_lab_config.json (custom.css on, no update
# check), jupyter_server_config.json (idle kernels and terminals are shut
# down after 15 minutes, open tab or not, as in the Nebari data science
# pack), jupyter_server_config.py (the Nebi kernels and the launcher's Nebi
# tile, on only in apps with Nebi access) with the tile's Nebi logo, and
# custom.css with the Fira Code font from the platform's files (/opt/javo
# in a container; JAVO_FILES_DIR on a machine that runs the app as a
# process).
# internal/pixienv runs this file in the activated environment, once per
# lockfile.
set -euo pipefail
mkdir -p "$CONDA_PREFIX/share/jupyter/lab/settings" "$CONDA_PREFIX/etc/jupyter"
cp overrides.json "$CONDA_PREFIX/share/jupyter/lab/settings/overrides.json"
cp jupyter_lab_config.json jupyter_server_config.json "$CONDA_PREFIX/etc/jupyter/"
cp jupyter_server_config.py "$CONDA_PREFIX/etc/jupyter/jupyter_server_config.py"
mkdir -p "$CONDA_PREFIX/share/javo"
cp nebi.svg "$CONDA_PREFIX/share/javo/nebi.svg"
custom=$(python -c 'import jupyter_server, os; print(os.path.join(os.path.dirname(jupyter_server.__file__), "static", "custom"))')
mkdir -p "$custom"
fonts=${JAVO_FILES_DIR:-/opt/javo}/fonts
cp custom.css "$fonts/FiraCode-VF.woff2" "$fonts/FiraCode-OFL.txt" "$custom/"
