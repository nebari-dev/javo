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
- The KEK never goes into the database. It is 32 random bytes in the Kubernetes Secret `javo-secrets-key`, mounted only in the controller. The gateway, which takes values from people, seals with the public half and holds no key that opens anything.
- The controller opens an app's secrets and writes them to a Secret `<app>-javo-secrets` in the app's namespace (owned by the app's Service, so it goes with the app). The pod reads them by reference (`env.valueFrom.secretKeyRef`); values are never in the pod spec or the database status. A new value, or a change to which secrets an app gets, restarts the app. Previews of pull requests never get secrets.
- A private repository's token is the value of the secret picked for it. The controller opens it into the app's `<app>-javo-git-token` Secret, which the clone reads as a file; a new value replaces the pod once. A token pasted before tokens came from secrets keeps working: a controller with the key moves it into a secret of the owner called `GIT_TOKEN_<APP>` (or `GIT_TOKEN_<APP>_2` and so on when that name is taken) and drops the pasted copy.
- Job runs read their values the same way, by reference, never from the CronJob or Job spec. Scheduled runs read `<job>-javo-secrets`, owned by the job's CronJob and rewritten when a value or binding changes, so the next run gets the change. A run started by hand gets `<run Job>-javo-secrets`, written with the values at its start and owned by its Job, so it goes when the run's Job does (an hour after it ends). Deleting the job deletes both at once. A run already going keeps what it started with.

## Back up the key

The chart makes the KEK once and keeps it: upgrades look up the live Secret, uninstall leaves it (`helm.sh/resource-policy: keep`), and it is immutable, so a render that cannot look it up fails to apply instead of replacing it.

:::caution
Without the KEK every stored secret is unreadable, and people would have to enter their values again. Back it up **apart from the database backups**: a copy of both opens everything.
:::

```sh
kubectl -n javo-system get secret javo-secrets-key -o jsonpath='{.data.kek}' > javo-secrets-key.b64
```

## Bring your own key

Tools that render the chart without the cluster (`helm template`, Argo CD) cannot look the key up and would make a new one each time. Make the Secret yourself and name it:

```sh
head -c 32 /dev/urandom > kek
kubectl -n javo-system create secret generic my-javo-kek --from-file=kek
rm kek
```

```yaml
secrets:
  existingKeySecret: my-javo-kek
```

A controller whose KEK does not open the stored keyset never replaces it. It logs the mismatch at start; apps that get secrets stay stopped and jobs that get secrets are blocked, with the reason "Secrets could not be read", until the right key is mounted. Other apps and jobs run as usual.

## Rotate the data key

```sh
kubectl -n javo-system exec deploy/javo -- /javo secrets rotate --key-file /etc/javo-secrets-key/kek
```

With Postgres, run it in the controller:

```sh
kubectl -n javo-system exec deploy/javo-controller -- /javo secrets rotate --key-file /etc/javo-secrets-key/kek \
  --database-engine=postgres --database-dsn-file=/etc/javo-db/dsn
```

It adds a new data key, makes new values use it, and encrypts every stored value again under it. No value changes and no app restarts. Values encrypted before keep working while it runs; running it again is safe.

## Removing a person

`javo user remove` also deletes the person's secrets.
