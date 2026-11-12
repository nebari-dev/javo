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

The picker searches people by the first three or more letters of their username or email, and your own groups. It shows an email only when the identity provider verified it.

**Copy link** copies the app's address. People you share with never get your sign-in.

## Apps shared with you

Every app someone else shares with you shows on **Home** and **Apps**, whatever the level: shared with you by name, with one of your groups, with everyone signed in, or with anyone who has the link. Each card says how: "Shared with you", "Shared with your group ml", "Shared with everyone signed in" or "Anyone with the link". Once anything is shared with you, the **Shared with me** filter appears to list only those apps. Your own apps and apps you could not open are never listed.

## What sharing hands people

A shared app runs as you, so the people you share it with can do whatever the app lets them do. The Sharing tab warns what that is for the kind of app:

| Kind of app | What people you share with get |
|---|---|
| VS Code, RStudio, marimo, Pluto.jl | They can run any code or command as you. They can read, change and delete every file in your home folder and your shared folders, including any keys or tokens kept there. |
| Static site | They can see every page and file of the site. The site cannot see your home folder. |
| Every other app (Streamlit, Panel, Bokeh, Dash, Gradio, Voila, FastAPI, Node, Go, custom command, container image) | They use the app as it runs. It runs as you, with your home folder and shared folders, so they see whatever it shows and can make it do whatever its code allows, such as reading or changing your files. |

An app with [API access](/using/api/) also lets them call the Javo API as you; the Sharing tab says so too.

**A shared app sees your files read-only.** While an app is shared at any level above Private, it can read your home folder and group folders but not change them, so nobody you share it with can change the files your other apps and jobs run. Its caches (`~/.cache`) and `/tmp` stay writable. Changing an app's sharing between Private and any other level restarts it. Give an app that must write files to others a private copy, or keep it private.

## A known limitation: apps share one address

On most installs every app is served from the same web address, with each app under its own path. Browsers keep pages apart by address, not by path, so a shared app's page can act as whoever opens it there: for example, it could drive their own JupyterLab or terminal while they have it open. Admins who open an app shared with them take the same risk. Share apps only with people who trust you, and keep apps that hold secrets private. Giving every app an address of its own (a subdomain) removes this; that will be added to the standard setup.

## Why a level may be missing

**Terminal, JupyterLab and coding agents are always private.** The Terminal, JupyterLab, Whybook, Claude Code, Harf, pi, herdr and any other coding agent hand whoever opens them code running as you, with your files. Javo refuses to share them, whatever their template says, in the web UI and the API alike.

**Code editors are shared only with people you add.** VS Code, RStudio, marimo and Pluto.jl let whoever opens them run any code as you, so they go no further than **Only people added**, whatever their template says. A share wider than that stops working.

**Each template sets the most open level its apps may use.** Every other built-in template may be shared up to **Anyone with the link**. A template an admin adds is private unless it sets `maxSharing`.

**Sharing needs apps on a separate host.** When apps are served on the same host as the web UI, a shared app could act through its visitor's Javo session. Javo then refuses every level above Private, with the message "Sharing needs apps served on a separate host from this page". Your admin fixes this by [serving apps on their own host](/admin/app-addresses/), or accepts the risk with `config.sharing.allowOneHost` (the Docker Compose install does).

## Previews

[Pull request previews](/using/previews/) copy their parent's sharing and follow it.
