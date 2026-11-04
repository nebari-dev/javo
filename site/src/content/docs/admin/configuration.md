---
title: Configuration
description: How Javo's Helm values are organised and the ones most installs set.
sidebar:
  order: 1
---

All settings are values of the chart in `deploy/helm/javo`. Every value is listed, with its comment, in the [Helm values reference](/reference/helm-values/).

Values fall in two groups:

- **Top-level values** shape the Kubernetes objects: the image, the database, sizes, ingress, backups, resources.
- **`config`** is the platform's own settings. The chart renders it into the ConfigMap `javo-config`, which the gateway and the controller both read.

## The values most installs set

| Value | What it does |
|---|---|
| `image.repository`, `image.tag` (or `image.digest`) | The `javo` image you built and pushed. |
| `database.engine` | `sqlite` (default; one pod) or `postgres` (separate gateway and controller, more than one replica each). |
| `database.postgres.dsnSecret` | With Postgres: a Secret whose key `dsn` holds the connection string. |
| `publicHost` | The host (and port) browsers use. Panel and Bokeh check websocket origins against it. Defaults to `config.urls.uiHost`. |
| `ingress.*` | An Ingress for the web UI (and the apps host, when set). `config.urls.uiHost` defaults to `ingress.host`. |
| `gateway.secureCookies` | Marks the session cookie Secure even when `config.urls.scheme` is `http`. With `https` (the default) it is always Secure. |
| `gateway.trustedProxies` | CIDRs of reverse proxies in front of the gateway, so sign-in rate limits see the real client address. List only the proxy's own addresses: anything listed can forge a client address. Left empty behind an ingress, the gateway cannot tell clients apart and turns per-address sign-in limits off (it logs a warning). Sign-in limits are kept in each gateway replica's memory, so several replicas allow that many times the attempts. |
| `config.auth` | [Sign-in](/admin/sign-in/): local accounts and OpenID Connect. |
| `config.roles` | Which groups are platform admins, template admins and members. |
| `config.urls` | [App addresses](/admin/app-addresses/). `uiHost` and `appsHost`, like `config.auth.oidc.issuer` and `clientID`, may be Helm templates such as `javo.{{ .Values.global.domain }}`. |
| `sizes`, `defaultSize`, `customSize` | [Sizes](/admin/sizes/). |
| `config.idleStop` | [Idle stop](/admin/idle-stop/). |
| `config.limits` | [Per-person limits](/admin/limits/): apps, running apps, jobs, runs, previews, tokens and plans per person, the longest run, and quotas of per-person and per-team namespaces. |
| `database.backup`, `backupTarget` | [Backups](/admin/backups/). |
| `workspaceTemplates` | Turn built-in templates on or off, point one at an image in your own registry, or limit one to groups (`allowedGroups`) or identity provider roles (`allowedProviderRoles`). |
| `containerImages` | Allow the Container image template, and from which registries. |
| `source.previews` | [Pull request previews](/using/previews/). |
| `config.namespaces.mode` | Where apps run: one shared namespace (`shared`, default), one per person (`per-user`) or one per team (`per-team`). |

## Security defaults to keep

- **`networkPolicy.enabled: true`**. Apps have no sign-in of their own; the controller keeps a default-deny NetworkPolicy in every app namespace that lets only gateway pods in. It also checks every `networkPolicy.probeInterval` (6 hours) that the network plugin really enforces it, and templates whose apps have no login start only while it does. The chart refuses to render with it off unless you also set `networkPolicy.iUnderstandAppsAreUnauthenticated: true`.
- **`securityContext`**: the gateway and controller run as non-root with a read-only root filesystem and no capabilities. App namespaces are labelled for Pod Security "restricted".
- **`networkPolicy.egress`**. The controller also keeps a `limit-egress` NetworkPolicy in every app namespace. By default app and job pods reach the public internet and, inside the cluster, only DNS (port 53 of the `k8s-app=kube-dns` pods in `kube-system` and of the name servers pods are given, for a node-local DNS cache), the gateway pods on their HTTP port (`gateway.port`; with SQLite the same pod serves the controller's metrics and health ports, which stay closed to apps) and the pods of their own namespace. Everything else in the cluster is blocked: pods in other namespaces (the database, the identity provider, the controller's metrics, Nebi), the private, carrier-grade NAT, link-local and loopback ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`, `169.254.0.0/16`, `127.0.0.0/8`, `fc00::/7`, `fe80::/10`), where nodes, the API server and storage servers usually are, the reserved ranges some clusters run pods or services on (`0.0.0.0/8`, `192.0.0.0/24`, `198.18.0.0/15`, `224.0.0.0/4`, `240.0.0.0/4`, `::/128`, `::1/128`, `ff00::/8`), the IPv6 ranges that reach IPv4 addresses (NAT64 `64:ff9b::/96` and `64:ff9b:1::/48`, 6to4 `2002::/16`), and every node's own addresses and pod ranges even when they are public (a single machine, bare metal and many small clouds give nodes public addresses; traffic from a pod to its node never crosses a cloud firewall, so the node's SSH, kubelet and NodePorts would otherwise be open to apps). The controller follows the nodes: a node that joins is denied within moments. A Service range outside all of these must be added to `denyCIDRs`. `denyCIDRs` blocks more on top: by default the cloud metadata addresses (`169.254.169.254/32`, `fd00:ec2::254/128`, GCP's `fd20:ce::254/128`, Alibaba's `100.100.100.200/32`) and Azure's WireServer (`168.63.129.16/32`), which stay denied with `allowCIDRs` too. To let pods reach chosen in-cluster services, list their namespaces in `allowNamespaces` (pods are matched by namespace, since some network plugins, Cilium, never match pods by address) or their private ranges (or a node's address, for a service that runs on the nodes) in `allowClusterCIDRs`. Apps with API access call the API at `config.urls.uiHost`: if that address leads to an ingress controller in the cluster rather than straight to the gateway pods, add the ingress controller's namespace to `allowNamespaces`. Set `allowCIDRs` to a list to let pods reach only those ranges (less `denyCIDRs`) instead of the public internet; DNS, the gateway, their own namespace and the two lists above stay reachable. File picker pods keep no egress at all either way.
- **Admission policies on the controller** (Kubernetes 1.30+). The controller holds cluster-wide rights to create RoleBindings and patch namespaces, because app namespaces are created later. The chart adds ValidatingAdmissionPolicies that make the API server refuse anything else from the components' service accounts. RoleBindings: only in namespaces labelled `javo.nebari.dev/managed=true` whose Pod Security level is `restricted`, only to the chart's two app ClusterRoles, and each only to its own component's service account. Namespace creation: only the names the placement produces (`<config.namespaces.prefix>-...` other than the release's own namespace, the probe namespace `<release namespace>-netprobe`, or in shared mode the workspace namespace), always labelled managed with all three Pod Security labels `restricted`. Namespace patches: only on managed namespaces (and in shared mode the workspace namespace), changing only `javo.nebari.dev/` labels and annotations and setting Pod Security labels only to `restricted`. The controller never takes over a namespace someone else made, even one named like Javo's: to have Javo use a namespace you created yourself in `per-user` or `per-team` mode, label it `javo.nebari.dev/managed=true` and `pod-security.kubernetes.io/enforce=restricted` (and `warn`, `audit`) yourself. Neither component may create a `kubernetes.io/service-account-token` Secret, which Kubernetes would fill with a token that never expires. The RoleBinding policy is rendered only in `per-user` and `per-team` modes. The chart renders them only when the cluster serves `admissionregistration.k8s.io/v1` ValidatingAdmissionPolicy: `helm install` and `helm upgrade` find that out themselves, but `helm template` (and Argo CD) must be told with `--api-versions admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy`. On older clusters the chart installs without them in `shared` mode only: `per-user` and `per-team` give the controller cluster-wide RoleBinding rights that only these policies keep to Javo's namespaces, so the chart refuses to render those modes without the API. With the default SQLite engine the one pod runs the internet-facing gateway as the controller's account, so these policies also bound what a compromised gateway could do there.
- **Admission policy on the gateway** (Kubernetes 1.30+, `database.engine: postgres`). The internet-facing gateway may create pods in app namespaces for the file picker, and a pod creator there could mount any Secret or claim. A third ValidatingAdmissionPolicy lets the gateway's service account create only file pods: labelled `javo.nebari.dev/role=files`, running only `files.image` with no init containers, mounting only claims read-only and `emptyDir` volumes, with no environment variables from Secrets or ConfigMaps and no service account token. With SQLite the one pod runs the gateway as the controller's account, which creates every kind of app pod, so the two cannot be told apart and this policy is not rendered; use Postgres where that separation matters.

## Request limits

`config.api.limits` bounds what requests may hold in the gateway, so one person or a slow client cannot use up its connections or memory. The defaults suit most installs.

| Value | Default | What it does |
|---|---|---|
| `streamsPerPerson` | `8` | How many followed logs (an app's live log, a startup step's log, a running job's log) one person may have open at once, all kinds together. Each holds a connection to the cluster. Another is refused with 429 until one is closed. |
| `thumbnailDecodes` | `4` | How many uploaded app pictures the gateway decodes at once, for everyone. Each may take up to 64 MB of memory. An upload beyond that is answered 503 with `Retry-After`, and the person tries again. |
| `bodyReadTimeout` | `60s` | How long a request to the API, sign-in or MCP may take to send its body (an upload, a saved file). A slower client is cut off. Requests without a body, such as log streams, and the traffic of apps themselves are not affected. Raise it if people upload over very slow links. |

Paths in the file browser and editor may have at most 64 folders and names; deeper paths are refused.

## Failure notices for jobs

`notifications.enabled` lets the controller send notices of failed job runs with [notifications-engine](https://github.com/argoproj/notifications-engine): services, templates, triggers and subscriptions go in the ConfigMap named by `notifications.configMapName`, credentials in the Secret named by `notifications.secretName`. Set `notifications.webhookURL` and the chart renders an example ConfigMap that posts failed runs to it. A job's failure message and display name come from the job's owner, so before they reach a template they are cut to 500 characters and escaped for the service type: an `@` that starts a word gets a zero-width space after it (no `@channel` or `@here`), links are written as `evil[.]example/x` and `https[:]//`, and `&`, `<` and `>` become entities. For a webhook, quotes become apostrophes, backslashes become slashes and line breaks become spaces, so the text is safe inside a JSON string written by hand or with `toJson`. One job sends at most one notice every 5 minutes; the failed runs in between are not sent.
