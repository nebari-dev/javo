---
title: Environment variables and secrets
description: Give apps and jobs settings and credentials. Secrets are encrypted at rest and handed over by reference.
sidebar:
  order: 5
---

## Secrets

Keep credentials under **Settings, Secrets**. A secret has a name (letters, digits and underscores), a value and an optional note. The value is write-only: after you save it, no page, API answer, log, export or backup shows it in clear. You can replace a value, change the note, or delete a secret that no app or job uses.

## Environment variables

Apps (under **Advanced** on New app, and on the app page) and jobs (on New job and the job page) take environment variables. Each row is a name and where its value comes from:

- **Value**: typed in, stored as is and shown again. Use it for settings that are not secret.
- **Secret**: one of your secrets, picked by name.

A private Git repository's access token is also one of your secrets, picked on the app form.

## When a value changes

- **Apps** that use a secret restart when its value changes, or when you change which secrets they get.
- **Scheduled jobs** get the new value from their next run. A run already going keeps the value it started with.

## What never gets a secret

[Pull request previews](/using/previews/) never get secrets, so code from a pull request cannot read them.

## Who can change an app or job that holds secrets

Only you choose which of your secrets an app or job gets. While an app or job holds any of your secrets (as a variable or as its repository token), only you can also change what it runs: a job's engine, command, file or template, an app's command, image or port, or turning on its pull request previews. An app with API access or your Nebi token, and a job whose template gives it an API token, count the same, because the token acts as you, and only you can give an app more API access. An app's or job's own API token cannot read, change or bind your secrets. A platform admin, and an app's or job's own API token, get "only its owner can change what it runs" for these; they can still rename, resize, reschedule, stop, start and delete it, and change what it runs once it holds no secrets.

## How values are protected

Values are encrypted with envelope encryption (HPKE under a data key, itself encrypted by a key kept outside the database). The code that takes values from people can only seal them; only the controller opens them, and it hands them to a pod through a Kubernetes Secret that the pod reads by reference. On installs with Postgres the controller runs apart from the web-facing gateway, which holds no key that opens a value; on the default SQLite install both run in one process, which holds the key. Before an app's logs go to an AI model, every place one of your secret values appears is replaced with `[redacted:<name>]`. Admins: see [Secrets and keys](/admin/secrets-and-keys/) for the key, its backup and rotation.
