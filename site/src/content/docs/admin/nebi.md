---
title: Nebi environments
description: Optional. Let people pick one of their Nebi environments for an app or a job.
sidebar:
  order: 10
---

[Nebi](https://github.com/nebari-dev/nebi) manages pixi environments. With the integration on, New app and New job show an **Environment** card that lists the person's Nebi environments (workspaces they own or that are shared with them, in the ready state). Picking one copies the `pixi.toml` and `pixi.lock` of its newest version into Javo with the app or job; every start installs that lockfile as described in [Environments](/using/environments/). Nebi is asked only while the form is filled in: a running app or a scheduled run never calls Nebi.

Off (the default), nothing talks to Nebi, `GET /api/environments` answers `available: false` and no form shows the card.

## Settings

```yaml
config:
  auth:
    oidc:
      issuer: https://keycloak.example.com/realms/example
      clientID: javo
  nebi:
    enabled: true
    url: https://nebi.example.com   # as the gateway and the apps reach it
  environments:
    pixiImage: ghcr.io/prefix-dev/pixi:<version>@sha256:<digest>   # the chart sets a default
```

- `nebi.url`: Nebi's address, `http` or `https`, with any path prefix.
- `environments.pixiImage`: the image that installs every environment, the templates' own too. Any image with `pixi`, `sh`, `flock`, `sha256sum`, `base64` and `gunzip` works; it runs as the pod's own user. The chart sets the upstream `ghcr.io/prefix-dev/pixi`, pinned by digest.
- `nebi.enabled` needs `nebi.url`, `environments.pixiImage` and `auth.oidc`; the controller and the gateway refuse to start otherwise.

## How Javo signs in to Nebi

As the person, with their own OpenID Connect ID token. The gateway keeps each browser session's ID token (it renews it with the refresh token when the session is revalidated, and before it expires) and sends it to Nebi's `POST /api/v1/auth/device-token`, which checks it and answers with a Nebi token for the Nebi user bound to the token's issuer and subject. Nebi then lists and serves only what that person may see.

A shared service token is not used: Nebi tokens expire after 24 hours, and Nebi lists and serves a workspace only to its owner and the people and groups it is shared with (admins included), so a service account could not see a person's environments.

This needs:

1. Nebi and Javo trust the same OIDC issuer: Nebi's `oidc_issuer_url` is exactly Javo's `auth.oidc.issuer`.
2. The ID tokens Javo receives name Nebi's client in their audience (Nebi checks that `aud` contains its own `oidc_client_id`). In Keycloak, add an **Audience** mapper to Javo's client: Included Client Audience is Nebi's client, Add to ID token on. Without it, the card says Nebi did not accept the sign-in.

People signed in with a local account, and calls made with an API token, have no ID token: the card says so and the API refuses an environment on create.

## Nebi inside JupyterLab

With the integration on, a JupyterLab app (and any template with `nebiToken: true`) also works with Nebi from the inside, as in the Nebari data science pack:

- The **`nebi` CLI** is on `PATH` in terminals and notebooks, signed in as the app's owner: `nebi workspace list`, `nebi pull <workspace>`, `nebi push` and so on.
- The **kernel picker** lists the owner's Nebi environments ([nb_nebi_kernels](https://github.com/nebari-dev/nb-nebi-kernels)). Environments pulled into the home (`nebi pull`) start as kernels; ones only on the server show as not pulled until they are.

### How the app signs in, and who it acts as

The app acts as its **owner** in Nebi. The token reaches it like this:

1. When the owner creates, starts or restarts the app, and again when they open it in the browser, the gateway trades the ID token of the owner's own sign-in for a Nebi token (the same `POST /api/v1/auth/device-token` as the Environment card), before the pod is built, with 5 seconds for Nebi to answer. So the token is there when the app first starts. Someone the app is shared with never mints one.
2. The gateway seals the token with the public half of the secrets key (as for [secrets](/using/secrets/)) bound to the app, and stores only the sealed value (table `app_nebi_tokens`). The gateway has no Kubernetes Secret rights and cannot read the token back; the database never holds it in clear.
3. The controller opens it and writes it to the Secret `<app object>-javo-nebi` (labelled with the app, deleted with it). The pod mounts it read-only and sees it as the file `/var/run/javo/nebi/token` (`JAVO_NEBI_TOKEN_FILE`), never as an environment variable or in the pod spec. The mount is optional, so a pod made before the first token starts without it; the kubelet adds the file later and replaces it on renewal, without a restart. Nebi's address is `NEBI_REMOTE_URL`.
4. The `nebi-cli` init step copies the nebi binary out of the Javo image (`/opt/javo-nebi-files`, the nebi 0.15 release binary) to `/opt/javo-nebi`. `/opt/javo/bin/nebi` and the kernel list read the token file at every use and pass it to that one process only. The gateway renews the token once less than 12 of its 24 hours are left, while the owner keeps using the app.

So the token outlives the owner's last use of the app (or their sign-out) by at most a day. Only the owner's own use renews it.

**Shared apps.** As with [API tokens](/using/api/), anyone the app is shared with acts as the owner in Nebi while they use it. JupyterLab can only be private (`maxSharing: Private`), so this applies only to a template an admin opens up.

**Previews** (pull request apps) never get a Nebi token: they run code the owner has not necessarily written.

People who sign in with a local account have no ID token, so their apps get no Nebi token; `nebi` then says the sign-in has not reached the app.

### Network

Apps reach Nebi at `nebi.url` too: it must be an address both the gateway and the apps' pods can reach (an in-cluster Service address works for both). Apps' outbound traffic is not limited by Javo's NetworkPolicies.

## Limits

- The environment is fixed when the app or job is created. To move to a newer version, create it again.
- The two files, compressed, must fit in 512 KiB: they travel in the install step's environment in the pod spec (and in a job's CronJob), so no extra object is needed. A larger environment is refused on the form. Anyone who can read the pod spec in the app's namespace can read them, so never put credentials in a `pixi.toml`.
- `pixi install --frozen` installs the lockfile as it is, without solving, so it needs the cluster's platform (`linux-64`, or the nodes' platform).
- The environment is installed into the owner's home, under `~/.javo/envs/<sha256 of pixi.toml and pixi.lock>`, with pixi's package cache in `~/.javo/pixi-cache`: the first start installs it, every later start of any app or job with the same files reuses it. An install that did not finish is cleared and done again. A preview's home is scratch, so it installs at every start.
- It replaces the template's environment, so it must bring the program the template starts (streamlit, panel and so on); the install step checks that the program is on the environment's `PATH`, and the app stops with "Environment lacks the program" when it does not. Commands started through `/opt/javo/pod-user.sh`, which every built-in template uses, run with pixi's activation of it (its `bin` first on `PATH`, `CONDA_PREFIX`, its activation scripts). Templates that run an image a person names do not offer the card. JupyterLab (a template with `servesKernels`) is the exception: the environment goes beside JupyterLab's own and each of its Jupyter kernels is offered under its name (`javo env-kernels`); `javo-env <name>` enters it in a terminal, and one without a kernel still starts, with the Started step saying so ([Environments](/using/environments/#jupyterlab-and-a-picked-environment)).
- An app from Git on a template with an environment installs the `pixi.toml` and `pixi.lock` at the root of its repository, when both are there, instead of the template's; a picked Nebi environment wins over both.
- A preview of a pull request runs in its parent's environment.
- An exported app does not carry its environment.

## When it fails

- The app or job says "Environment not available" when its stored files are missing or too large, or `environments.pixiImage` is not set. A job then waits as Blocked.
- It says "Environment did not install" with pixi's own error when the install step fails.
- It says "Environment lacks the program" when a picked environment (or a repository's lockfile) does not have the program the template starts.
- The gateway logs `nebi token: ...` (never the token) when it could not renew an app's Nebi token; the app keeps the one it has.
