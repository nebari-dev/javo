---
title: Storage
description: Home folders, group folders and scratch space, how Javo picks storage, and moving homes between storage modes.
sidebar:
  order: 6
---

Javo looks at the storage the cluster offers and picks how to store home folders. Run `javo storage report` to see what it found and what is in use:

```sh
kubectl -n javo-system exec deploy/javo -- /javo storage report
```

## Home folders

Every person gets one home folder, mounted at `/home/user` in each of their apps and job runs.

```yaml
config:
  storage:
    home:
      mode: auto          # auto, perUser (a disk per person) or shared (one shared drive)
      # storageClassName: ""
      size: 10Gi          # what a new home asks for
      # maxSize: 100Gi    # the largest a home may be given
```

- **auto** picks **shared** only for a StorageClass known or checked to offer shared (ReadWriteMany) file storage with POSIX ownership; otherwise **perUser**.
- `storageClassName` names the class for homes; by default Javo chooses from what the cluster offers.
- To grow one person's home: `javo storage home-size alice 20Gi` (the claim grows where the storage allows it).

Javo cannot detect everything about a StorageClass. State facts it cannot see with `classOverrides`:

```yaml
config:
  storage:
    classOverrides:
      - {name: longhorn, rwxFile: true, posixOwnership: true, sizeEnforced: true}
```

`javo storage verify <class>` runs a two-node test on a StorageClass (make a shared claim, write on one node, check from another) and prints each step. It may take up to `config.storage.probe.timeout` (5 minutes).

A home whose disk a machine keeps failing to attach or mount for 2 minutes fails with "Home folder could not be mounted", and the machine's own message shows on the app page and the admin Storage page. Shared storage served over NFS (Longhorn ReadWriteMany, for example) needs an NFS client (`nfs-common` or `nfs-utils`) on every node; when the message says a `mount.<type>` helper is missing, the Storage page says so. Install the client, or set `storage.home.mode: perUser` for new homes.

## Moving homes to another mode

Changing `home.mode` never touches an existing home: the new mode applies to new homes. Existing homes move only when you schedule it, on the admin Storage page or with the CLI:

```sh
kubectl -n javo-system exec deploy/javo -- /javo storage migrate plan
kubectl -n javo-system exec deploy/javo -- /javo storage migrate schedule --at 2026-12-01T02:00:00Z
kubectl -n javo-system exec deploy/javo -- /javo storage migrate status
```

People see a banner from the moment it is scheduled. At that time their apps stop, each home is copied to the new storage, checked, and only then switched. The old claim is kept for `config.storage.migration.keepOldFor` (7 days). `javo storage migrate retry` tries again for homes whose copy failed. The copy runs as `javo storage copy` in the Javo image itself, as the home's owner; it keeps links, hard links, modes, groups and times, then compares both copies file by file.

## Group folders

A group folder is shared by everyone in a group, mounted at `/shared/<name>` in their apps:

```sh
kubectl -n javo-system exec deploy/javo -- /javo storage folder add /org/data-science --size 50Gi
kubectl -n javo-system exec deploy/javo -- /javo storage folder list
```

Name the group the way the identity provider does. `config.storage.sharedFolders.defaultSize` (20Gi) is the default size. `folder remove` stops offering a folder; its claim and files stay.

## Scratch

Every app gets `/scratch`, node-local space for caches, up to `config.storage.scratch.sizeLimit` (20Gi). It is emptied when the app stops.

## Namespaces and team storage

With `config.namespaces.mode: per-team`, each team's apps run in a namespace of its own, and `storageLimit` caps the total storage its claims may request (default `config.storage.teamStorage.limit`):

```yaml
config:
  namespaces:
    mode: per-team
    prefix: javo
    teams:
      - {group: /org/ds, slug: ds, storageLimit: 200Gi}
```
