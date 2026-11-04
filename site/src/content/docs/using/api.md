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
- **Scope**:
  - **Read**: only reading calls.
  - **Propose**: reading calls, and proposing changes that wait for you to approve them in the browser. This is the token for a [coding agent over MCP](/using/ask-ai/#coding-agents-mcp): it cannot make a change itself.
  - **Read and write**: everything you can do as a member. A read-and-write token is full access to your account: your apps, jobs and files.
  - **Admin**: shown to platform and template admins only. Read and write with your admin roles too. It must expire within 30 days, and it acts as a member whenever your groups were last confirmed by your identity provider more than a day ago (sign in again to bring its admin roles back).
- **Expires after** 7, 30 or 90 days or one year, up to your admin's limit; **Never** when your admin allows it.

The token is shown once; copy it then. The list shows each token's prefix and when it was last used, and revokes a token. A token acts with the groups you had at your last sign-in, and stops working when that sign-in is older than your admin's limit (30 days by default) until you sign in again. Tokens cannot create or revoke tokens.

Only an admin token carries admin roles. Every other token, including the token an app or a job gets (`JAVO_TOKEN`), acts as a member even when you are an admin, so code running in your app cannot use the admin pages or change other people's apps. The one exception is the built-in backup job, whose token may start and follow backup and restore runs and nothing else an admin can do.

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
