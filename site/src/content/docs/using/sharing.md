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

**Copy link** copies the app's address. People you share with can use the app; they never get your files, tokens or sign-in.

## Why a level may be missing

**Each template sets the most open level its apps may use.** A template that gives the visitor a terminal with your files, such as JupyterLab, Terminal, Claude Code, pi and herdr, is private only: anyone who opens it could act on your files. Of the built-in templates, Streamlit and Static site may be shared up to **Anyone with the link**; the others are private unless a template admin raises their limit.

**Sharing needs apps on a separate host.** When apps are served on the same host as the web UI, a shared app could act through its visitor's Javo session. Javo then refuses every level above Private, with the message "Sharing needs apps served on a separate host from this page". Your admin fixes this by [serving apps on their own host](/admin/app-addresses/), or accepts the risk with `config.sharing.allowOneHost` (the Docker Compose install does).

## Previews

[Pull request previews](/using/previews/) copy their parent's sharing and follow it.
