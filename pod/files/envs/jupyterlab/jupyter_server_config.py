# The Nebi kernels (nb_nebi_kernels, pod/files/envs/jupyterlab/pixi.toml):
# javo-post-install.sh puts this file where Jupyter Server reads it in this
# environment. nb_nebi_kernels installs itself as the KernelSpecManager; it
# lists the Nebi workspaces of `nebi workspace list` (the person's pulled
# ones) and, with NEBI_REMOTE_URL and NEBI_AUTH_TOKEN, those on the Nebi
# server, each as a kernel. Javo gives an app the owner's token only as the
# file JAVO_NEBI_TOKEN_FILE, which it renews while the owner uses the app
# (internal/controller/workspace/nebi.go). So:
#   - without that file's path, the app has no Nebi access: Jupyter's own
#     KernelSpecManager stays, no Nebi tile shows, and nothing runs nebi;
#   - with it, every discovery reads the current token from the file and
#     hands it to nb_nebi_kernels for that discovery only, so the token is
#     never left in the server's environment, nor in its kernels'.
#     The launcher gets a Nebi tile (below).
import os
import sys

_token_file = os.environ.get("JAVO_NEBI_TOKEN_FILE", "")

if not _token_file:
    c.ServerApp.kernel_spec_manager_class = "jupyter_client.kernelspec.KernelSpecManager"  # noqa: F821
else:
    from nb_nebi_kernels import NebiKernelSpecManager

    class JavoNebiKernelSpecManager(NebiKernelSpecManager):
        """NebiKernelSpecManager with the owner's token read from a file."""

        def _discover(self, *, force=False):
            try:
                with open(_token_file) as f:
                    token = f.read().strip()
            except OSError:
                token = ""
            if token:
                os.environ["NEBI_AUTH_TOKEN"] = token
            try:
                super()._discover(force=force)
            finally:
                os.environ.pop("NEBI_AUTH_TOKEN", None)

    c.ServerApp.kernel_spec_manager_class = JavoNebiKernelSpecManager  # noqa: F821

    # The Nebi tile of the launcher, as in the Nebari data science pack:
    # jupyter-server-proxy starts `nebi serve` in local mode on the first
    # click and opens its UI at <base_url>nebi/, in a browser tab of its own.
    # Not in a JupyterLab tab as the pack does: that is an iframe JupyterLab
    # loads without a Referer, which the gateway refuses while apps share an
    # origin (internal/gateway/proxy/isolation.go). So the pack's instant
    # kernel refresh after a Nebi build (nebi:job-completed, sent only to a
    # parent frame) does not happen; the kernels refresh on their poll. The nebi
    # wrapper (pod/bin/nebi) hands it the owner's token at start, and with
    # NEBI_REMOTE_URL (Nebi's address, from the controller) local Nebi
    # connects to that server as the owner. It keeps the same database as
    # the nebi CLI in terminals (~/.local/share/nebi/nebi.db).
    _nebi_data = os.path.join(
        os.environ.get("XDG_DATA_HOME", os.path.expanduser("~/.local/share")),
        "nebi",
    )
    c.ServerProxy.servers = {  # noqa: F821
        "nebi": {
            "command": ["/opt/javo/bin/nebi", "serve", "--port", "{port}"],
            "timeout": 20,
            "absolute_url": True,
            "new_browser_tab": True,
            "environment": {
                "NEBI_SERVER_BASE_PATH": "{base_url}nebi",
                "NEBI_MODE": "local",
                "NEBI_DATABASE_DSN": os.path.join(_nebi_data, "nebi.db"),
                "NEBI_STORAGE_WORKSPACES_DIR": os.path.join(_nebi_data, "workspaces"),
            },
            # Local Nebi accepts only loopback Host and Origin headers, and
            # the proxy forwards the browser's: without this its UI loads
            # blank, every API call refused.
            "request_headers_override": {
                "Host": "localhost:{port}",
                "Origin": "http://localhost:{port}",
            },
            "launcher_entry": {
                "title": "Nebi",
                "enabled": True,
                # The Nebi logo (nebi's assets/nebi-icon.svg), put here by
                # javo-post-install.sh.
                "icon_path": os.path.join(sys.prefix, "share", "javo", "nebi.svg"),
            },
        }
    }
