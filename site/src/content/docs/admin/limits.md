---
title: Per-person limits
description: How many apps, jobs, runs, previews, tokens and plans one person may have, the longest run, and the quotas of per-person and per-team namespaces.
sidebar:
  order: 5.5
---

Each person may have only so much at once, so one person cannot fill the cluster or the database. Every limit has a default and applies to platform admins too. Raise one with a bigger number; there is no "unlimited".

```yaml
config:
  limits:
    runningApps: 10          # apps running at once (previews not counted)
    apps: 50                 # apps, running or stopped
    previews: 20             # previews of pull requests over all the person's apps
    jobs: 50
    activeRunsPerJob: 3      # runs of one job going at once, requested ones included
    activeRunsPerPerson: 10  # runs of all the person's jobs going at once
    apiTokens: 50            # personal API tokens not yet expired
    pendingPlans: 20         # proposed changes waiting for the person's approval
    jobMaxTimeout: 24h       # the longest any run may take
```

What a person sees at a limit:

- **Creating, starting or importing an app, creating a job, Run now, creating a token or proposing a change** is refused with a message that names the limit (HTTP 409). Nothing is created. An import lists the app it could not bring in with the same message; an admin restore from a backup does the same and restores the rest.
- **A scheduled run** while the job or the person is at their run limit is not started: the controller holds the job's schedule until a run ends, then starts the newest run that came due meanwhile, and the job page shows **Too many runs going**. A run asked for with Run now that finds the room taken by scheduled runs fails with that reason. This also caps a schedule whose overlap setting is **run both** (`Allow`).
- **A pull request** past the person's preview limit gets no preview and lists **Your preview limit reached**. The per-app limit (`source.previews.maxPerApp`) still applies as well.
- **A run** with no time limit, or a longer one than `jobMaxTimeout` (from the job or its template's `maxTimeout`), is stopped at `jobMaxTimeout` and fails as timed out.

Lowering a limit stops nothing that already exists: a person over it keeps what they have and can add nothing until they are under it again.

Schedules may run at most once a minute; `@every` below a minute is refused.

## Disk inside a pod

`/tmp` and the scratch folder of every app and job pod are node disk with a size limit each: `config.storage.scratch.tmpSizeLimit` (2Gi by default) for `/tmp`, `config.storage.scratch.sizeLimit` (20Gi by default) for scratch. A pod that writes more is evicted alone, before the node runs short of disk.

The container's own writable layer (files written anywhere but the home, `/tmp` and scratch) has no limit of its own. A template whose `resources` name an `ephemeral-storage` limit gives it to its pods, since sizes keep resources other than CPU, memory and GPUs; the built-in templates name none. Kubernetes then also reserves that much node disk for each pod when it places it, since a limit with no request asks for the same request. In per-user and per-team mode, `limits.namespace` below sets a default for every container instead.

## Process count

Javo sets no limit on how many processes a pod may start. The kubelet's `podPidsLimit` (in the kubelet configuration of each node, `--pod-max-pids` on its command line) does: set it, for example to `4096`, so a fork bomb in one app cannot exhaust the node's process table. With k3s that is `--kubelet-arg=pod-max-pids=4096`.

## Quotas of per-person and per-team namespaces

With `config.namespaces.mode` `per-user` or `per-team`, each namespace the controller keeps can also get a ResourceQuota (what all pods there may ask for together) and a LimitRange (what a container that names nothing gets). Both are named `javo-limits`; leaving them out (the default) makes none, and clearing them removes the ones made. They are refused in `shared` mode, where one quota would cap everyone together.

```yaml
config:
  limits:
    namespace:
      quota: {requests.cpu: "16", requests.memory: 64Gi, pods: "30", requests.ephemeral-storage: 100Gi}
      containerDefaults: {ephemeral-storage: 20Gi}
      containerDefaultRequests: {ephemeral-storage: 1Gi}
```

A quota on a resource makes Kubernetes refuse every pod there that does not ask for it, and some of Javo's own helper containers (such as the one that installs the job runner) name no CPU, memory or disk of their own. Give a `containerDefaultRequests` (or `containerDefaults`) entry for each resource the quota names. In per-team mode the storage allowance (`teamStorage`) is a separate quota, `javo-storage`.
