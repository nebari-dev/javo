---
title: Deploy from your computer
description: Let a coding agent on your computer upload the app in a folder, create it in Javo and give you its link.
sidebar:
  order: 9
---

A coding agent on your computer (Claude Code, Codex, Cursor and the like) can deploy the app in a folder to Javo for you. It logs in as you once you approve, uploads the folder to your home folder, creates the app and gives you its link. It needs only `curl` and `tar`.

## Deploy an app

1. In Javo, open **New app**, or **Settings, API tokens**. The **Deploy from your computer** panel shows one line, such as:

   ```text
   Deploy the app in this folder to Javo. Follow https://javo.example.com/llms.txt
   ```

   Copy it.
2. Start your coding agent in the app's folder and paste the line. The agent reads the guide at `/llms.txt` on your Javo and follows it.
3. The agent shows a link and a code such as `BDFG-HJKL`. Open the link in the browser where you are signed in to Javo. The page shows the code: check it matches the one the agent showed, then press **Approve**. Press **Deny** when you did not start this yourself just now. The link works for 10 minutes.
4. The agent uploads the folder to `deploys/<name>` in your home folder, picks a template that fits the files, creates the app and waits for it to run. It reads the app's logs and fixes what it can when the app does not start.
5. The agent gives you the app's link. The app is in your app list like any other, private to you until you [share it](/using/sharing/).

The agent never asks for a password or a secret. When the app needs one, it asks you to store it under **Settings, Secrets** and add it to the app in the browser; see [secrets](/using/secrets/).

## Redeploy

Ask the agent again after you change the code. It uploads the folder over the same `deploys/<name>`, after checking with you, and restarts the app (or starts it when it is stopped).

## The agent's login

Approving gives the agent an API token that acts as you, with read and write access: everything you can do as a member, on your apps, jobs and files. It lasts 8 hours, or less when your admin [limits token lifetimes](/using/api/#api-tokens). After that the agent asks you to approve again.

The token is kept on your computer, readable only by you, under `~/.config/javo/<your Javo host>/` (or `$XDG_CONFIG_HOME/javo/` when that is set). It shows under **Settings, API tokens** as **Agent login**; revoke it there to cut the agent off at once.

## For admins

Folder uploads (`PUT /api/files/archive`) are bounded by `config.api.limits` in the Helm values:

- `uploadBytes`: the most a folder may unpack to, and the most its compressed upload may be. 104857600 (100 MiB) by default. The person's home needs room for a second copy while a folder is replaced.
- `uploadFiles`: the most files and folders an upload may hold, 10000 by default.
- `uploadReadTimeout`: how long an upload may take to send, `10m` by default.

Larger uploads are refused, and the agent tells the person to leave out what the app does not need. The login token follows `config.api.tokens.maxLifetime` when that is shorter than 8 hours.
