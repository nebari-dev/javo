---
title: Pull request previews
description: One preview app per open pull request of an app that runs from a GitHub repository.
sidebar:
  order: 2
---

An app that runs from a **GitHub** repository can get a preview app for each open pull request. Each preview runs the pull request's code with the parent app's settings, so reviewers can open the change before it is merged.

Your admin must turn previews on for the platform (`source.previews.enabled`, see [Helm values](/reference/helm-values/)). Then turn on **Preview open pull requests** when you create the app, or on the app page's Previews card.

## How previews behave

- The newest open pull requests get a preview, up to **Previews at most** (3 by default; your admin sets the platform's cap, at most 10). The others are listed with the reason, such as the limit being reached.
- A preview follows new commits to its pull request, and is removed when the pull request is closed.
- A preview is named `<app> PR #<number>`. Its short name is `<app>--pr-<number>`, so with path addresses it opens at `/u/<owner>/<app>--pr-<number>/`.
- Pull requests from forks are skipped unless you turn on **Also preview pull requests from forks**.
- A preview runs in its parent's [environment](/using/environments/).

## Who can open a preview

A preview copies its parent's [sharing](/using/sharing/), and follows it when the parent's sharing changes.

## What a preview never gets

- Your [secrets](/using/secrets/): previews of pull requests never get secrets.
- API access: always None.
- Previews of its own.
- A preview of a fork's pull request also gets no repository token.
- When apps share the web UI's host (no apps host, see [App addresses](/admin/app-addresses/)), a preview of a fork's pull request does not open: its page could act as you.

## Faster updates with a webhook

Without a webhook, Javo asks GitHub for the list of pull requests every few minutes (the admin's `source.previews.pollInterval`, 2 minutes by default). For updates within seconds, open **Set up webhook** on the Previews card. It shows a webhook address and secret once; add them to the repository's webhooks on GitHub. A delivery only tells Javo to check again; Javo never trusts its contents. A new address and secret replace the current ones, and the secret on GitHub stops working until you paste the new one.
