# Starts Pluto for the pluto WorkspaceTemplate:
#   julia /opt/javo/pluto/run.jl <port> <url-prefix> [notebook]
# in the pluto environment (pod/files/envs/pluto), whose default project has
# Pluto.
# The prefix starts and ends with "/". Without a notebook Pluto starts on its
# file menu. The arguments arrive as data (ARGS), never as Julia code.
#
# No secret: the gateway authenticates every request, and the javo-apps
# NetworkPolicy only lets the gateway reach the pod. No update check and no
# AI editor features: nothing calls out on its own.
import Pluto

# A notebook's own packages (Pluto's package manager) install from the
# registry into the environment's Julia depot.
ENV["JULIA_PKG_OFFLINE"] = "false"

port, base_url = parse(Int, ARGS[1]), ARGS[2]
notebook = get(ARGS, 3, "")

Pluto.run(;
    # Every address, or the one Javo names where the app runs as a process.
    host = get(ENV, "JAVO_LISTEN_ADDR", "0.0.0.0"),
    port,
    base_url,
    notebook = isempty(notebook) ? nothing : notebook,
    launch_browser = false,
    dismiss_update_notification = true,
    enable_ai_editor_features = false,
    require_secret_for_access = false,
    require_secret_for_open_links = false,
)
