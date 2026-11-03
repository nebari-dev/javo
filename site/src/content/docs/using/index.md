---
title: Use Javo
description: Apps, jobs, sharing, environments, secrets, Ask AI and the API, from a person's point of view.
sidebar:
  order: 0
  label: Overview
---

Everything you do in Javo happens in the web UI, and everything the web UI does is also in the [API](/using/api/).

- [Apps](/using/apps/): notebooks, editors, dashboards and web services, from your home folder or a Git repository.
- [Pull request previews](/using/previews/): one preview app per open pull request.
- [Sharing](/using/sharing/): who else can open an app.
- [Jobs](/using/jobs/): run scripts and notebooks on demand or on a schedule.
- [Environment variables and secrets](/using/secrets/): settings and credentials for apps and jobs.
- [Environments](/using/environments/): the packages an app or job runs with.
- [Ask AI](/using/ask-ai/): model help in the UI, and coding agents through MCP.
- [API tokens and the API](/using/api/): scripts and agents acting as you.

## Your files

Every app and job you own sees the same **home folder** at `/home/user`. Your code, data, job outputs and installed environments live there and survive restarts. Apps also get:

- `/scratch`: fast space on the machine the app runs on, for caches. It is emptied when the app stops.
- `/shared/<folder>`: folders shared with a group you belong to, when an admin [has made one](/admin/storage/#group-folders).
