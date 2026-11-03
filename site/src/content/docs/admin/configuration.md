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
| `gateway.trustedProxies` | CIDRs of reverse proxies in front of the gateway, so sign-in rate limits see the real client address. |
| `config.auth` | [Sign-in](/admin/sign-in/): local accounts and OpenID Connect. |
| `config.roles` | Which groups are platform admins, template admins and members. |
| `config.urls` | [App addresses](/admin/app-addresses/). `uiHost` and `appsHost`, like `config.auth.oidc.issuer` and `clientID`, may be Helm templates such as `javo.{{ .Values.global.domain }}`. |
| `sizes`, `defaultSize`, `customSize` | [Sizes](/admin/sizes/). |
| `config.idleStop` | [Idle stop](/admin/idle-stop/). |
| `database.backup`, `backupTarget` | [Backups](/admin/backups/). |
| `workspaceTemplates` | Turn built-in templates on or off, point one at an image in your own registry, or limit one to groups (`allowedGroups`) or identity provider roles (`allowedProviderRoles`). |
| `containerImages` | Allow the Container image template, and from which registries. |
| `source.previews` | [Pull request previews](/using/previews/). |
| `config.namespaces.mode` | Where apps run: one shared namespace (`shared`, default), one per person (`per-user`) or one per team (`per-team`). |

## Security defaults to keep

- **`networkPolicy.enabled: true`**. Apps have no sign-in of their own; the controller keeps a default-deny NetworkPolicy in every app namespace that lets only gateway pods in. It also checks every `networkPolicy.probeInterval` (6 hours) that the network plugin really enforces it, and templates whose apps have no login start only while it does. The chart refuses to render with it off unless you also set `networkPolicy.iUnderstandAppsAreUnauthenticated: true`.
- **`securityContext`**: the gateway and controller run as non-root with a read-only root filesystem and no capabilities. App namespaces are labelled for Pod Security "restricted".
- **`networkPolicy.egress`**. The controller also keeps a `limit-egress` NetworkPolicy in every app namespace. By default app and job pods may connect anywhere except the cloud metadata addresses in `denyCIDRs` (`169.254.169.254/32` and `fd00:ec2::254/128`), which hand out the node's cloud credentials on AWS, GCP, Azure and most other clouds. Set `denyCIDRs: []` to deny nothing. Set `allowCIDRs` to a list to let pods reach only those ranges (less `denyCIDRs`), plus DNS (port 53), the gateway pods and the pods of their own namespace. With a list, everything else is blocked: other pods in the cluster, Nebi, package mirrors, Git hosts, and the ingress address apps call the API at must be listed by address. Some network plugins (Cilium) never match pods by address, so with a list pods in other namespaces stay unreachable there. File picker pods keep no egress at all either way.
- **Admission policies on the controller** (Kubernetes 1.30+). The controller holds cluster-wide rights to create RoleBindings and patch namespaces, because app namespaces are created later. The chart adds two ValidatingAdmissionPolicies that make the API server refuse anything else from the controller's service account: RoleBindings only in namespaces labelled `javo.nebari.dev/managed=true` and only to the chart's two app ClusterRoles, and namespace patches only on managed namespaces (or, before they are adopted, ones named `<config.namespaces.prefix>-...`, or the shared namespace), changing only `javo.nebari.dev/` labels and annotations and setting Pod Security labels only to `restricted`. The RoleBinding policy is rendered only in `per-user` and `per-team` modes. The chart renders them only when the cluster serves `admissionregistration.k8s.io/v1` ValidatingAdmissionPolicy: `helm install` and `helm upgrade` find that out themselves, but `helm template` (and Argo CD) must be told with `--api-versions admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy`. On older clusters the chart installs without them. With the default SQLite engine the one pod runs the internet-facing gateway as the controller's account, so these policies also bound what a compromised gateway could do there.

## Failure notices for jobs

`notifications.enabled` lets the controller send notices of failed job runs with [notifications-engine](https://github.com/argoproj/notifications-engine): services, templates, triggers and subscriptions go in the ConfigMap named by `notifications.configMapName`, credentials in the Secret named by `notifications.secretName`. Set `notifications.webhookURL` and the chart renders an example ConfigMap that posts failed runs to it.
