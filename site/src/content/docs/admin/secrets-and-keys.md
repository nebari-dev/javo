---
title: Secrets and keys
description: How people's secrets are encrypted, backing up the key-encryption key, bringing your own key, and rotating the data key.
sidebar:
  order: 8
---

People keep named [secrets](/using/secrets/) under Settings, Secrets. An app's or a job's environment variables each take either a typed value (stored as is and shown again, for settings that are not secret) or the value of one of the owner's secrets; a private repository's access token is also picked from the owner's secrets. A secret's value is write-only: no page, API answer, log line, audit row, export or database backup holds it in clear. Exports list typed values and secret names, never a secret's value.

## How values are stored

Values are encrypted with [Google Tink](https://developers.google.com/tink) (`github.com/tink-crypto/tink-go/v2`), using envelope encryption with a public-key data keyset:

- Each value is sealed with HPKE (RFC 9180: X25519, HKDF-SHA256, AES-256-GCM) under a key made for that value alone. The ciphertext is bound to its row (the secret's id, owner and name), so a ciphertext copied onto another row does not open.
- The data keyset is in the database table `secret_keysets`: its public half in clear (it can only seal) and its private half encrypted with AES-256-GCM by the **key-encryption key** (KEK).
- The KEK never goes into the database. It is 32 random bytes in the Kubernetes Secret `javo-secrets-key`, which only the controller reads. The gateway code, which takes values from people, seals with the public half and never opens a value. Where the KEK is held depends on the database engine:
  - `database.engine=postgres`: the gateway and the controller are separate Deployments. Only the controller's pods read the KEK; the internet-facing gateway pods hold no key that opens anything.
  - `database.engine=sqlite` (the default): the gateway and the controller run in one process in the one pod `javo`, because one process owns the database file. That process serves the web and also holds the KEK and the database, so a flaw that lets someone read its files or memory opens every stored secret. Use Postgres when you need the split.
- The controller opens an app's secrets and writes them to a Secret `<app>-javo-secrets` in the app's namespace (owned by the app's Service, so it goes with the app). The pod reads them by reference (`env.valueFrom.secretKeyRef`); values are never in the pod spec or the database status. A new value, or a change to which secrets an app gets, restarts the app. Previews of pull requests never get secrets.
- A private repository's token is the value of the secret picked for it. The controller opens it into the app's `<app>-javo-git-token` Secret, which the clone reads as a file; a new value replaces the pod once. A token pasted before tokens came from secrets keeps working: a controller with the key moves it into a secret of the owner called `GIT_TOKEN_<APP>` (or `GIT_TOKEN_<APP>_2` and so on when that name is taken) and drops the pasted copy.
- Job runs read their values the same way, by reference, never from the CronJob or Job spec. Scheduled runs read `<job>-javo-secrets`, owned by the job's CronJob and rewritten when a value or binding changes, so the next run gets the change. A run started by hand gets `<run Job>-javo-secrets`, written with the values at its start and owned by its Job, so it goes when the run's Job does (an hour after it ends). Deleting the job deletes both at once. A run already going keeps what it started with.

## Back up the key

The controller makes the KEK on its first start, when the Secret `javo-secrets-key` does not exist, and uses the one it finds on every start after. The chart does not render it, so `helm template` and Argo CD need no special handling and see no change between renders. The Secret is immutable, has no owner, and is annotated `helm.sh/resource-policy: keep` and `argocd.argoproj.io/sync-options: Prune=false,Delete=false`: an upgrade, an uninstall or a prune leaves it.

:::caution
Without the KEK every stored secret is unreadable, and people would have to enter their values again. Back it up **apart from the database backups**: a copy of both opens everything.
:::

```sh
kubectl -n javo-system get secret javo-secrets-key -o jsonpath='{.data.kek}' > javo-secrets-key.b64
```

### Upgrading from a chart that rendered the key

Earlier charts rendered `javo-secrets-key` themselves. An upgrade keeps that Secret (it carries `helm.sh/resource-policy: keep` and Argo CD's `Prune=false,Delete=false`), and the controller finds it and uses its key. With Argo CD, drop the `ignoreDifferences` block for the Secret from the Application, and remove Argo CD's tracking from the old Secret so it is no longer counted as part of the Application (metadata can change on an immutable Secret; its key cannot):

```sh
kubectl -n javo-system label secret javo-secrets-key app.kubernetes.io/instance-
kubectl -n javo-system annotate secret javo-secrets-key argocd.argoproj.io/tracking-id-
```

## Bring your own key

To use a key made elsewhere (a copy restored from a backup, or one from your own secret store), make the Secret before installing and name it. The controller then only reads it and never makes one; it does not start while the Secret is missing.

```sh
head -c 32 /dev/urandom > kek
kubectl -n javo-system create secret generic my-javo-kek --from-file=kek
rm kek
```

```yaml
secrets:
  existingKeySecret: my-javo-kek
```

A controller whose KEK does not open the stored keyset never replaces it. It logs the mismatch at start; apps that get secrets stay stopped and jobs that get secrets are blocked, with the reason "Secrets could not be read", until the right key is in place. Other apps and jobs run as usual.

## Rotate the data key

```sh
kubectl -n javo-system exec deploy/javo -- /javo secrets rotate --key-secret javo-secrets-key
```

With Postgres, run it in the controller:

```sh
kubectl -n javo-system exec deploy/javo-controller -- /javo secrets rotate --key-secret javo-secrets-key \
  --database-engine=postgres --database-dsn-file=/etc/javo-db/dsn
```

It adds a new data key, makes new values use it, and encrypts every stored value again under it. No value changes and no app restarts. Values encrypted before keep working while it runs; running it again is safe.

## Sessions

A session signed in with OpenID Connect keeps the provider's refresh token and id token in the database, to stay signed in and to sign out at the provider. The gateway seals both with AES-256-GCM (Google Tink) under its own key, bound to the session's row, so a copy of the database, such as a backup, holds no token that works. The key is 32 random bytes in the Secret `javo-sessions-key`, mounted only in the gateway (`--sessions-key-file`) and made and kept like the KEK. For `helm template` or Argo CD, make it yourself and set `secrets.existingSessionsKeySecret` (key `key`, `head -c 32 /dev/urandom`). Losing or replacing it signs everyone out; nothing else is lost. A gateway started without the flag makes a key in memory, so its restarts sign everyone out.

Upgrading to the release that seals them ends every OpenID Connect session once (its tokens were stored in clear and the database has no key to seal them); people sign in again. Local account sessions carry no tokens and stay.

## Secrets and Ask AI

Before an app's logs go to the model (Ask AI, "Explain"), the gateway replaces every place one of the owner's secret values appears with `[redacted:<secret name>]`, and does the same for the typed values of variables named like credentials (`*PASSWORD*`, `*TOKEN*`, `*SECRET*`, `API_KEY`, `DATABASE_URL` and so on). Only then does the gitleaks pattern scan run. It does not look for personal data such as names or email addresses.

The gateway cannot open values, so it looks for fingerprints: for each secret the controller stores the value's length and an HMAC-SHA256 of it in the table `secret_fingerprints`, keyed by 32 random bytes in the Secret `javo-secret-fingerprint-key` (mounted in the gateway and the controller, `--secret-fingerprint-key-file`; `secrets.existingFingerprintKeySecret` names your own). The key is not in the database, so a database copy cannot be used to test guesses against them; the copy does show each value's length. The controller fingerprints new and changed values within 15 seconds. Until all of an owner's values have a current fingerprint, or when no key is mounted, none of that owner's log lines are sent ("could not be checked for secrets"). Values shorter than 4 characters are not looked for.

## Removing a person

`javo user remove` also deletes the person's secrets.
