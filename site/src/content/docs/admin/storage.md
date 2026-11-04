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

### Storage that ignores sizes

Some storage does not enforce a claim's size: k3s's `local-path` (the default of the Compose and single-node installs), `nfs.csi.k8s.io` and `nfs-subdir-external-provisioner`. There a home can grow until the disk under it is full. When the database (SQLite), its backups (`javo-backups`) and the nodes' own disk share that disk, one person writing a large file (`fallocate -l 500G ~/big`) stops the platform for everyone: database writes fail and the kubelet evicts pods for disk pressure. The admin Storage page warns about it ("Home folders have no size limit").

On such storage, do at least one of these:

- Put homes on a filesystem with quotas: on `local-path`, point its storage path at an XFS filesystem mounted with `prjquota` and give each home directory a project quota, or use a CSI driver that enforces capacity (Longhorn, a cloud disk).
- Keep the database and its backups off that disk: a separate volume for the SQLite claim and `javo-backups` (a StorageClass on another disk), or `database.engine: postgres` on a server elsewhere, plus `backupTarget` to copy backups off the machine.
- Watch disk use on the nodes, so a full disk is noticed before it stops anything.

The node-local space each pod uses besides its home is bounded either way; see [Scratch](#scratch).

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

Node-local disk is bounded per pod, so no app or job can fill a node's disk with it: `/tmp` holds up to `config.storage.scratch.tmpSizeLimit` (2Gi), and each app and job container may write at most `config.storage.scratch.diskLimit` (30Gi) in all (scratch, `/tmp`, its own image layer and logs). Past either limit the kubelet evicts the pod and the app shows "Temporary space is full". `diskLimit` must hold `sizeLimit` plus `tmpSizeLimit`; on small nodes lower all three. The limit reserves nothing: the pod requests no disk.

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
