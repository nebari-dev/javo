---
title: API tokens and the API
description: Personal API tokens, calling the REST API, the API docs page and the OpenAPI document.
sidebar:
  order: 8
---

Everything the web UI does goes through the REST API under `/api/`, so scripts and agents can do it too.

## API tokens

Create a token under **Settings, API tokens**, **New token**:

- **Name**: to recognise it later.
- **Scope**: **Read** (only reading calls) or **Read and write**.
- **Expires after** 7, 30 or 90 days or one year; **Never** when your admin allows it.

The token is shown once; copy it then. The list shows each token's prefix and when it was last used, and revokes a token. A token acts with the groups you had at your last sign-in, and stops working when that sign-in is older than your admin's limit (30 days by default) until you sign in again. Tokens cannot create or revoke tokens.

## Call the API

Send the token as a bearer token:

```sh
curl -H "Authorization: Bearer $JAVO_TOKEN" https://javo.example.com/api/workspaces
```

Apps given [API access](/using/apps/#advanced-options) get `JAVO_URL` and `JAVO_TOKEN` in their environment for the same purpose.

## API docs

- **The OpenAPI document** is served at `/api/openapi.json` on every install, without sign-in.
- **The API docs page** at `/api/docs/` is an interactive Swagger UI, when your admin turns it on (`config.api.docs.enabled`). An **API docs** button then shows in Settings, and **Authorize** takes a token to try calls.
- The [REST API reference](/reference/api/) on this site lists every endpoint.
