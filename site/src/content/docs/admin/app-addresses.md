---
title: App addresses
description: Serve apps under the web UI's host, on a separate apps host, or on subdomains, why sharing wants a separate host, and why only subdomains keep apps apart.
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

- a platform admin cannot open other people's apps (they get "Platform admins cannot open other people's apps while apps share one address");
- nobody can open a preview of a pull request from a fork;
- **sharing beyond Private is refused.** Serve apps on an apps host or on subdomains and sharing works (an apps host in path mode still puts all apps in one origin, see below). To allow sharing on one host anyway, knowing the risk, set:

```yaml
config:
  sharing:
    allowOneHost: true
```

The Docker Compose install does this, since it serves everything on one address.

## Path mode with an apps host: apps share one origin

An apps host keeps apps away from the web UI, but in path mode every app of every owner is served from the same origin (`https://apps.example.com`). Browsers isolate pages by origin, not by path: a cookie's `Path` does not keep it from a script on another path of the same origin. So a page of one app can load another app the visitor can open in a frame and script it as them, for example a JupyterLab terminal. **Only subdomain mode gives each app an origin of its own, and only it isolates apps from each other.**

Path mode with an apps host gets the same refusals as one host: a platform admin cannot open other people's apps, and previews of pull requests from forks do not open. Sharing is still allowed there.

### The cross-app guard

While apps share an origin (one host, or path mode with an apps host), the proxy also reads the `Sec-Fetch-Site`, `Sec-Fetch-Mode` and `Sec-Fetch-Dest` headers browsers send with every request (page scripts cannot change them) and the `Referer`. It answers 403, "This app cannot be opened from inside another app", to:

- a frame, iframe, `embed` or `object` that loads an app from any page that is not that app's own, on any site;
- a same-origin request that is not a top-level navigation (a `fetch`, an image, a script) unless its `Referer` is a page of the app itself. That refuses requests from another app's pages, and also requests with no `Referer` or one cut to the origin: a page can ask the browser for either, and nothing else in a same-origin request says which page sent it (the visitor's cookie goes with every request to the app, whoever starts it).

It lets through top-level navigations (following a link, opening a new tab, typing the address), requests from the app's own pages, requests from other sites (the browser's SameSite cookie rules keep the visitor's session off those) and requests without `Sec-Fetch-*` headers, which come from clients other than browsers and carry no cookie of the visitor's.

Browsers send the full `Referer` to the same origin unless a page asks otherwise (the default policy is `strict-origin-when-cross-origin`). JupyterLab, Streamlit and VS Code (code-server) set no policy of their own, so their pages keep working. An app whose own pages set a policy that drops or shortens the `Referer` for the same origin (`no-referrer`, `origin`, `strict-origin`) has its own requests refused in these modes; serve it in subdomain mode.

### What the guard does not stop

The guard stops pages that do not try to get around it. It does not stop a page written to attack another app:

- **A page can change its own `Referer`.** `history.pushState` moves a page's address to any path of its own origin without loading anything, so a page of app A can take an address under app B and its later requests carry app B's `Referer`.
- **Websockets carry no `Referer`** (the WebSockets standard sends none), so a websocket opened by another app's page cannot be told apart and is let through.
- **Top-level navigations are let through,** so a page that opens another app with `window.open` keeps a handle to the new window, which shares its origin and can be scripted.

So in path mode, with or without an apps host, share apps only with people who trust every app they can open. For apps that run code you do not trust, use subdomain mode.

## Open websockets

Every request is checked when it arrives. A websocket stays open after that, so the gateway checks it again every 30 seconds, with the same rules as a new request, and closes it once the visitor may no longer open the app: a share was removed, the session ended, or the app was deleted. Until that check, up to 30 seconds, an open websocket keeps working. A check that cannot reach the database closes the websocket too.

## How signing in to an app works on a separate host

The apps host never sees the web UI's session cookie. When a signed-in person opens an app there, the gateway sends them through a short hop on the UI host that issues a single-use ticket (valid for 60 seconds); the app's address redeems it for a cookie that is valid for that app only.

## What the gateway does for each request

- Checks again, on every request, that the visitor may open the app (its sharing, its template, the visitor's session), and every 30 seconds for an open websocket.
- While apps share an origin, refuses requests that another app's page starts (see [the cross-app guard](#the-cross-app-guard)).
- Removes Javo's own cookies and API tokens from the request, so the app never sees them; anonymous visitors send no cookies at all.
- Removes the app's prefix before forwarding, for templates that serve at `/` (such as Container image), and says what it removed in `X-Forwarded-Prefix`.
- Counts the traffic as use, for [idle stop](/admin/idle-stop/).
