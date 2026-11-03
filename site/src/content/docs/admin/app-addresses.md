---
title: App addresses
description: Serve apps under the web UI's host, on a separate apps host, or on subdomains, and why sharing wants a separate host.
sidebar:
  order: 3
---

Every app is reached through the gateway. `config.urls` decides the address:

| Setup | Values | App address |
|---|---|---|
| Path, same host (default) | `mode: path` | `https://javo.example.com/u/<owner>/<app>/` |
| Path, apps host | `mode: path`, `appsHost: apps.example.com` | `https://apps.example.com/u/<owner>/<app>/` |
| Subdomain | `mode: subdomain`, `appsDomain: apps.example.com` | `https://<owner>-<app>.apps.example.com/` |

```yaml
config:
  urls:
    mode: subdomain
    uiHost: javo.example.com      # required whenever apps are on another host
    appsDomain: apps.example.com
    scheme: https                 # the default
```

With an Ingress, the chart adds the apps host, or `*.<appsDomain>`, to the Ingress rules and TLS hosts. Point DNS for those names at the same ingress, and give it a certificate that covers them (a wildcard certificate for subdomains).

## Why a separate host matters

An app runs code its owner chose. When apps share the web UI's host, a page in an app runs in the same origin as the web UI: its script can call Javo's API as whoever views it, with their session, and nothing on that one host can tell the two apart. **One-host mode is only for apps everyone who opens them trusts.** The clean fix is a separate origin.

Javo refuses state-changing API calls from other sites: the web UI marks each call with an `X-Javo-Request` header that another site cannot add, and calls whose `Sec-Fetch-Site`, `Origin` or `Referer` point at another site or at a page under `/u/` are refused too. Clients with an API token (`Authorization: Bearer`) need no header. None of this stops a page served under `/u/` on the UI's own host. So while apps share the UI's host:

- a platform admin cannot open other people's apps (they get "Platform admins cannot open other people's apps while apps share the platform's address");
- nobody can open a preview of a pull request from a fork;
- **sharing beyond Private is refused.** Serve apps on an apps host or on subdomains and sharing works. To allow sharing on one host anyway, knowing the risk, set:

```yaml
config:
  sharing:
    allowOneHost: true
```

The Docker Compose install does this, since it serves everything on one address.

## How signing in to an app works on a separate host

The apps host never sees the web UI's session cookie. When a signed-in person opens an app there, the gateway sends them through a short hop on the UI host that issues a single-use ticket (valid for 60 seconds); the app's address redeems it for a cookie that is valid for that app only.

## What the gateway does for each request

- Checks again, on every request and websocket, that the visitor may open the app (its sharing, its template, the visitor's session).
- Removes Javo's own cookies and API tokens from the request, so the app never sees them; anonymous visitors send no cookies at all.
- Removes the app's prefix before forwarding, for templates that serve at `/` (such as Container image), and says what it removed in `X-Forwarded-Prefix`.
- Counts the traffic as use, for [idle stop](/admin/idle-stop/).
