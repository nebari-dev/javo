---
title: Migrating from the data science pack
description: Copy people's JupyterHub home folders into their Javo homes, and carry over who may use which profiles.
sidebar:
  order: 13
---

The Nebari data science pack runs JupyterHub: each person's files live on a claim named `claim-<username>` in the hub's namespace, mounted at `/home/jovyan` and owned by uid 1000. Javo keeps each person's files on their own home claim, mounted at `/home/user` and owned by their own uid. `javo import jupyterhub-homes` copies the first into the second.

Profiles limited to some people map to templates and sizes limited by group or by provider role: see [Coming from the data science pack](/admin/sign-in/#coming-from-the-data-science-pack).

## What the import does

For each `claim-<username>` claim in the JupyterHub namespace:

1. It finds the Javo person whose username or email is the JupyterHub username, ignoring case. KubeSpawner's `hub.jupyter.org/username` annotation gives the username; without it, the claim name is unescaped (`claim-alice-40example-2ecom` is `alice@example.com`). A user who matches nobody, or more than one person, is skipped.
2. It makes the person's Javo home if they have none, at least as large as their JupyterHub home, and waits until it is ready. With SQLite, Javo's controller picks up a home made this way within 5 minutes.
3. A Job in the JupyterHub namespace copies the JupyterHub claim, mounted read-only, to a new staging claim. It runs as root, so it can read every file, and gives every file the person's uid and group 100. It keeps modes, times, links and hard links, and then compares both copies file by file (content by SHA-256).
4. The staging volume moves to Javo's namespace. The import sets its reclaim policy to Retain, deletes its claim, and binds the volume to a new claim in Javo's namespace. A claim can only be mounted in its own namespace, which is why this step exists.
5. A Job in Javo's namespace, running as the person without root, copies the staged files into the folder `jupyterhub` of their home and compares the copies again. This is the same copy Javo uses when it moves homes between storage modes.
6. The home claim is annotated with where its files came from (`javo.nebari.dev/imported-from: <namespace>/<claim>`), and the staging volume is deleted.

The JupyterHub claims are only ever read: nothing changes or deletes them. Delete them yourself once people have checked their files.

## Before you start

- **Stop the JupyterHub servers.** A home whose server still runs is left for later: its files are changing, and a disk that attaches to one node at a time cannot be read from another. Stop people's Javo apps too; an import into a home in use is also left for later.
- **Give Javo access to the JupyterHub namespace** for the time of the import, and upgrade the release:

  ```yaml
  jupyterhubImport:
    namespace: jupyterhub   # the data science pack's namespace
  ```

  The server may then make and delete claims and Jobs in that namespace, and get, list and patch PersistentVolumes (to move the staging volume). Empty the value again when you are done.
- **People.** By default only people who have signed in to Javo at least once are imported. To import everyone now, see [People who have not signed in yet](#people-who-have-not-signed-in-yet).
- **Space.** Each home being imported briefly needs a staging volume as large as the JupyterHub claim, on the JupyterHub claim's StorageClass.

## Run it

Look first. A dry run changes nothing and says what would happen to each home:

```sh
kubectl -n javo-system exec deploy/javo -- /javo import jupyterhub-homes --from-namespace jupyterhub --dry-run
```

Then import. `--user alice` (repeatable) limits it to some people:

```sh
kubectl -n javo-system exec deploy/javo -- /javo import jupyterhub-homes --from-namespace jupyterhub
```

It works on four homes at a time (`--parallel`), prints each step and the copy Jobs' own logs, prefixed with the username, and ends with one line per home:

```text
alice: staging
alice | copying /old to /new
alice | every entry goes to uid 100001, gid 100
alice | copied 214 files, 52318720 bytes
alice | checking the copy
alice | copy verified: no differences
alice: moving
alice: copying
alice | copying /old to /new/jupyterhub
alice | copied 214 files, 52318720 bytes
alice | checking the copy
alice | copy verified: no differences

JupyterHub home      Javo person       result
alice (claim-alice)  alice-1a2b3c4d    imported into javo-apps/home-alice-1a2b3c4d, folder jupyterhub
bob (claim-bob)      -                 skipped: no Javo person has username or email "bob" (they have not signed in; or pass --create-people)
```

Files land in `~/jupyterhub` of each home. `--into .` puts them in the home itself instead; a file of the same name already in the Javo home is then replaced.

## When something stops

Run the same command again: every step first looks at what is already there, so an import carries on where it stopped, and homes already imported are reported as such and not copied again. A copy that failed half way starts again, and skips the files it had already copied.

- **left for later**: a JupyterHub server or a Javo app has the home. Stop it and run again.
- **failed**: a copy Job failed. The line gives the copy's own message (for example `no space left on device`) and the `kubectl logs` command for the whole log.
- **stuck**: a home waited more than `--stuck-after` (6 minutes) for something other than a running copy, such as its home being made or the staging volume being bound. The line says what it waited for; `kubectl describe` the claim it names.

## People who have not signed in yet

A Javo person is their account at the identity provider (its issuer and subject), never a username, so the import cannot make one from a JupyterHub username alone. With `--create-people`, it makes people from a file of `username,subject[,email]` lines. With Keycloak the subject is the user's id; list them with:

```sh
kcadm.sh get users -r <realm> --limit 100000 --fields id,username,email \
  | jq -r '.[] | [.username, .id, (.email // "")] | @csv' > people.csv
```

Then pass it on standard input (the image has no shell to copy a file into):

```sh
kubectl -n javo-system exec -i deploy/javo -- /javo import jupyterhub-homes --from-namespace jupyterhub \
  --create-people --subjects - < people.csv
```
 People are made for the OIDC issuer Javo signs in with (`config.auth.oidc.issuer`). A subject that already belongs to a person with another username is skipped rather than changed.

## Limits

- The Job in the JupyterHub namespace runs as root with the capabilities `CHOWN`, `DAC_OVERRIDE`, `FOWNER` and `FSETID` (all allowed by Pod Security "baseline"), and pulls the Javo image there: a registry that needs pull secrets needs them in that namespace too.
- The access the chart grants covers the shared workspace namespace (`namespaces.mode: shared`).
- With storage that stays on one node (such as local-path) on a cluster of several nodes, the staging volume and the home must end up on the same node for the last copy to run.
