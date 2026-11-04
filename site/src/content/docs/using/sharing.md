---
title: Sharing
description: Keep an app private, or share it with people, groups, everyone signed in, or anyone with the link.
sidebar:
  order: 3
---

Open an app's **Sharing** tab to choose who else can open it. Only the owner changes sharing.

| Level | Who can open the app |
|---|---|
| **Private** | You (and platform admins). |
| **Only people added** | The people and groups you add with the picker. |
| **Anyone signed in** | Every person signed in to Javo. |
| **Anyone with the link** | Anyone, signed in or not. |

**Copy link** copies the app's address. People you share with never get your sign-in.

## What sharing hands people

A shared app runs as you, so the people you share it with can do whatever the app lets them do. The Sharing tab warns what that is for the kind of app:

| Kind of app | What people you share with get |
|---|---|
| VS Code, RStudio, marimo, Pluto.jl | They can run any code or command as you. They can read, change and delete every file in your home folder and your shared folders, including any keys or tokens kept there. |
| Static site | They can see every page and file of the site. The site cannot see your home folder. |
| Every other app (Streamlit, Panel, Bokeh, Dash, Gradio, Voila, FastAPI, Node, Go, custom command, container image) | They use the app as it runs. It runs as you, with your home folder and shared folders, so they see whatever it shows and can make it do whatever its code allows, such as reading or changing your files. |

An app with [API access](/using/api/) also lets them call the Javo API as you; the Sharing tab says so too.

## Why a level may be missing

**Terminal, JupyterLab and coding agents are always private.** The Terminal, JupyterLab, Claude Code, pi, herdr and any other coding agent hand whoever opens them code running as you, with your files. Javo refuses to share them, whatever their template says, in the web UI and the API alike.

**Code editors are shared only with people you add.** VS Code, RStudio, marimo and Pluto.jl let whoever opens them run any code as you, so they go no further than **Only people added**, whatever their template says. A share wider than that stops working.

**Each template sets the most open level its apps may use.** Every other built-in template may be shared up to **Anyone with the link**. A template an admin adds is private unless it sets `maxSharing`.

**Sharing needs apps on a separate host.** When apps are served on the same host as the web UI, a shared app could act through its visitor's Javo session. Javo then refuses every level above Private, with the message "Sharing needs apps served on a separate host from this page". Your admin fixes this by [serving apps on their own host](/admin/app-addresses/), or accepts the risk with `config.sharing.allowOneHost` (the Docker Compose install does).

## Previews

[Pull request previews](/using/previews/) copy their parent's sharing and follow it.
