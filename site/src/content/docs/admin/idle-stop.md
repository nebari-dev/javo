---
title: Idle stop
description: Stop apps that nobody uses, and the limits people pick within.
sidebar:
  order: 5
---

An app with an idle timeout stops after that many minutes with no requests and no open connections through the gateway. An open terminal tab holds a connection, so it counts as use.

JupyterLab is the exception: its tab holds websockets open whether anyone uses it or not, so while one is open the gateway asks the Jupyter server instead. Use is what Jupyter counts (its `/api/status` last activity: API requests, kernel output, terminal input), and a kernel running code counts as use for as long as it runs. A visible JupyterLab tab polls the server every few seconds, which Jupyter counts as use; a hidden one (another tab in front, a minimized window) stops polling. Kernels and terminals idle for 15 minutes are shut down by the server itself, open tab or not; a kernel running code never is. The app's files and settings are kept; it shows **Stopped when idle** and starts again from its card. Job runs are not affected.

```yaml
config:
  idleStop:
    defaultMinutes: 0     # what a new app gets when its creator picks nothing; 0 is Off
    minMinutes: 10        # the shortest timeout people may pick
    maxMinutes: 10080     # the longest (7 days)
    allowOff: true        # whether people may pick Off; platform admins always may
```

It is off by default, so an upgrade never stops a shared dashboard or a long computation that nobody watches. Set `defaultMinutes: 60` to free unused apps' CPU and memory after an hour. With `allowOff: false`, `defaultMinutes` must be between `minMinutes` and `maxMinutes`.

A template can set its own default with `defaultIdleStopMinutes` (0 is Off), which replaces `defaultMinutes` for apps made from it. The built-in Terminal template sets 10 minutes, so a terminal nobody has open stops soon after its tab closes. A built-in template's default outside `minMinutes` and `maxMinutes` is moved to the nearest bound, and Off with `allowOff: false` gives `defaultMinutes`; a template saved through the API is refused instead. People still pick another time or Off when they create the app, or later on its page.
