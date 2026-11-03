---
title: Administer Javo
description: Configure, secure, back up and upgrade a Javo install.
sidebar:
  order: 0
  label: Overview
---

Javo is configured through the values of its Helm chart (the Docker Compose install uses the same chart). Change a value, then `helm upgrade` the release.

- [Configuration](/admin/configuration/): how values are organised, and the ones most installs set.
- [Sign-in and roles](/admin/sign-in/): local accounts, OpenID Connect (Keycloak and others), groups to roles.
- [App addresses](/admin/app-addresses/): path or subdomain, and why sharing wants a separate host.
- [Sizes](/admin/sizes/): CPU, memory and GPU sizes, and custom amounts.
- [Idle stop](/admin/idle-stop/): stop apps nobody uses.
- [Storage](/admin/storage/): home folders, group folders and scratch space.
- [Backups and restore](/admin/backups/): database copies, home folders, an S3 bucket.
- [Secrets and keys](/admin/secrets-and-keys/): the key that protects people's secrets.
- [AI](/admin/ai/): connect a model.
- [Nebi environments](/admin/nebi/) (optional).
- [App thumbnails](/admin/thumbnails/) (optional).
- [Branding](/admin/branding/): your own name, logo, favicon and accent colour.
- [Upgrades](/admin/upgrades/).

Platform admins also get these tabs under **Settings**: **All apps** (every person's apps, with a Stop button that tells the owner an admin stopped it), **Usage** (app-hours by person or group over 7 or 30 days), **Backups** and **Storage**.
