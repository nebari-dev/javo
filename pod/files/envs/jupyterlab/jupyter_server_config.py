# The Nebi kernels (nb_nebi_kernels, pod/files/envs/jupyterlab/pixi.toml):
# javo-post-install.sh puts this file where Jupyter Server reads it in this
# environment. nb_nebi_kernels installs itself as the KernelSpecManager; it
# lists the Nebi workspaces of `nebi workspace list` (the person's pulled
# ones) and, with NEBI_REMOTE_URL and NEBI_AUTH_TOKEN, those on the Nebi
# server, each as a kernel. Javo gives an app the owner's token only as the
# file JAVO_NEBI_TOKEN_FILE, which it renews while the owner uses the app
# (internal/controller/workspace/nebi.go). So:
#   - without that file's path, the app has no Nebi access: Jupyter's own
#     KernelSpecManager stays, and nothing runs nebi;
#   - with it, every discovery reads the current token from the file and
#     hands it to nb_nebi_kernels for that discovery only, so the token is
#     never left in the server's environment, nor in its kernels'.
import os

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
