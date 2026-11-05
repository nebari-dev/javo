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

Javo refuses state-changing API calls from other sites: the web UI marks each call with an `X-Javo-Request` header that another site cannot add, and calls whose `Sec-Fetch-Site`, `Origin` or `Referer` point at another site or at a page under `/u/` are refused too. Clients with an API token (`Authorization: Bearer`) need no header. Reads that can create a person's home folder or start its file pod get the same checks: listing files and reading storage usage need the header, and a raw file (which the web UI also loads as an image, a frame or a link) must come from a page of the web UI itself, so a page of an app on another host of the same site cannot trigger them. None of this stops a page served under `/u/` on the UI's own host. So while apps share the UI's host:

- a platform admin or template admin cannot open other people's apps, even ones shared with them, since the app's page would act with their admin rights on the web UI itself (they get "Admins cannot open other people's apps while apps share one address"); they still open their own;
- nobody can open a preview of a pull request, from a fork or from the same repository: it runs code the owner has not necessarily read;
- **sharing beyond Private is refused**, and apps shared earlier, while the install had an apps host, no longer open for the people they were shared with. Serve apps on an apps host or on subdomains and sharing works (an apps host in path mode still puts all apps in one origin, see below). To allow sharing on one host anyway, knowing the risk, set:

```yaml
config:
  sharing:
    allowOneHost: true
```

The Docker Compose install does this, since it serves everything on one address.

## Path mode with an apps host: apps share one origin

An apps host keeps apps away from the web UI, but in path mode every app of every owner is served from the same origin (`https://apps.example.com`). Browsers isolate pages by origin, not by path: a cookie's `Path` does not keep it from a script on another path of the same origin. So a page of one app can load another app the visitor can open in a frame and script it as them, for example a JupyterLab terminal. **Only subdomain mode gives each app an origin of its own, and only it isolates apps from each other.**

In path mode with an apps host, an app shared with someone opens for them, admins included: an admin opens another person's app when it is shared with them, but never by admin rights alone. Previews of pull requests do not open. Sharing is allowed.

**Known limitation:** while apps share one origin, a shared app's page can act as whoever opens it on the apps host, for example by scripting their own JupyterLab or terminal there. Share apps only with people who trust the app's owner, and keep apps that hold secrets private. Per-app subdomain addresses, which give every app an origin of its own and remove this limit, will be added to the standard setup.

## Admins and apps that run code as their owner

In every mode, a platform admin cannot open someone else's app that runs code as its owner: a terminal, JupyterLab, VS Code, RStudio, a coding agent, or any app whose template keeps it Private. It would give the admin a shell or editor as that person, with their secrets. They get "Only its owner opens this app", and **Settings > All apps** shows no Open button for it. They still start, stop and delete it and read its logs. In subdomain mode they open other people's other apps, and a code editor its owner shared with them.

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

## Subdomain mode: apps are one site

In subdomain mode every app has an origin of its own, but all apps and the web UI's host are one site (`example.com`). Browsers send a cookie with requests from the same site whatever its `SameSite` setting, so a page of one app could send requests to another app with the visitor's cookie for it. The proxy refuses, with "This app cannot be opened from inside another app", every request whose `Sec-Fetch-Site` is `same-site` unless it is a top-level `GET` navigation (following a link, which is also how the sign-in hop through the UI host arrives): a `fetch`, an image, a frame or iframe, a websocket or a form post from another host of the site does not reach the app. Requests from the app's own pages are `same-origin` and are not affected. A frame on another site gets no cookie of the visitor's, so it opens the app only as an anonymous visitor would.

## Apps that use absolute paths

In path mode a template with `basePath: Strip` (Container image, for example) has the app's prefix removed, so the app serves at `/`. Many apps then write absolute paths in their pages: `<script src="/assets/index.js">`, `fetch("/api/items")`, a websocket to `/ws`, a redirect to `/login`. The browser asks for those outside `/u/<owner>/<app>/`. The gateway takes such a request (a root request) as the app's own, so most apps work as they are:

- **By `Referer`.** A request outside `/u/` whose `Referer` is a page of an app on the same host goes to that app, with its path unchanged (the app sees it at its root, as with the prefix stripped). When a request has a `Referer`, it alone decides: one from a page of the web UI or of another host goes to no app.
- **By cookie, for requests without a `Referer`.** Websockets carry none. When the gateway serves an app's page to a top-level navigation it sets a cookie (`__Host-javo_app_route`, `HttpOnly`, `SameSite=Lax`, `Secure`, `Path=/`, valid for an hour) naming the app, sealed with the sessions key so it cannot be made to name another app. A root request with no `Referer` goes to the app it names.
- **Redirects.** A redirect of a stripped app to one of its own absolute paths (`Location: /login`, or an absolute URL on the same host) gets the prefix added, unless it already starts with it.
- **Cookies.** As for every app in path mode, a cookie the app sets with `Path=/` or no `Path` gets the app's own path (see below).

A root request is decided exactly like a request for the same path under the app's prefix: the visitor must be able to open that app, and the cross-app guard applies with the `Referer` as given (it is that app's own page). A request from another site (`Sec-Fetch-Site` other than `same-origin`) never uses the cookie or a `Referer`. Templates that keep the prefix (`basePath: Prefix`) get no root requests: their apps already write it in their links. Subdomain mode needs none of this, since each app is at the root of its own host.

On an apps host, the app's session cookie lives on the app's path, which a root request does not match, so opening an app's page also sets `__Host-javo_app_root_<app>` on `/` with the same app session. Like every Javo cookie, apps never see it.

On one host, a root request reaches an app only for a path the gateway does not serve itself: never `/api`, `/u`, a route of the gateway, a file of the built web UI, or a top-level navigation (the web UI routes its own pages in the browser, so any path may be one of them). A link in an app's page to an absolute path therefore opens the web UI, not the app, and requests without `Sec-Fetch-*` headers are never an app's there.

Limits:

- **Two apps open at once.** The cookie names the app whose page the browser opened last. A request without a `Referer` from one app (a websocket) while another app was opened later in another tab goes to the later one.
- **Pages that drop the `Referer`.** A `fetch` from a page with `Referrer-Policy: no-referrer` is refused by the cross-app guard, as it would be under the prefix.
- **An app's own cookies.** An app's cookies are kept on its path, so its requests to absolute paths do not carry them; an app that needs its cookies on such requests (a login of its own read by `/api/...`) needs a base-path setting.
- **Links and bodies are not rewritten.** The gateway does not change HTML, CSS or JavaScript. An app that has a public URL or root path setting should still get it: it is more reliable than the fallback.

## Open websockets

Every request is checked when it arrives. A websocket, a stream of server-sent events or a long download stays open after that, so the gateway checks it again every 30 seconds, with the same rules as a new request, and closes it once the visitor may no longer open the app: a share was removed, the session ended, or the app was deleted. Until that check, up to 30 seconds, an open connection keeps working. A check that cannot reach the database closes the connection too.

## How signing in to an app works on a separate host

The apps host never sees the web UI's session cookie. When a signed-in person opens an app there, the gateway sends them through a short hop on the UI host that issues a single-use ticket (valid for 60 seconds); the app's address redeems it for a cookie that is valid for that app only.

## What the gateway does for each request

- Checks again, on every request, that the visitor may open the app (its sharing, its template, the visitor's session), and every 30 seconds for a websocket, event stream or download still open.
- While apps share an origin, refuses requests that another app's page starts (see [the cross-app guard](#the-cross-app-guard)).
- Removes Javo's own cookies and API tokens from the request, so the app never sees them; anonymous visitors send no cookies at all.
- Keeps the cookies an app sets within the app: it removes their `Domain` attribute and, in path mode, sets their `Path` to the app's own path (an app's `__Host-` cookie is dropped there, since it needs `Path=/`), and drops any cookie named like Javo's own (`javo_...`). A page's script can still set cookies itself with `document.cookie`, which the gateway does not see; an apps domain that is not under the web UI's domain keeps those away from the web UI.
- Removes the app's prefix before forwarding, for templates that serve at `/` (such as Container image), and says what it removed in `X-Forwarded-Prefix`.
- For those templates in path mode, sends the app's requests for absolute paths to it and adds the prefix to its redirects (see [apps that use absolute paths](#apps-that-use-absolute-paths)).
- Counts the traffic as use, for [idle stop](/admin/idle-stop/).
