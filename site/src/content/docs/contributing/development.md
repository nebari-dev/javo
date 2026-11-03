---
title: Development
description: Local clusters, end-to-end tests, single-host deploys and the Docker Compose install from a checkout.
sidebar:
  order: 1
---

Details for working on Javo. See [Contributing](/contributing/) for the short version.

## Local cluster

Run the tests (envtest binaries are downloaded into `bin/`):

    make test

Run it locally (kubeconfig `.dev/kubeconfig`; needs docker with Compose v2, kubectl, helm and Go):

    make up          # cluster, images and release; prints the URL (http://javo.localhost:8080 in the main checkout)
    make reload      # after a Go or UI change: rebuild the Javo image and roll the release
    make e2e-quick   # up, then the core end-to-end suites (no Keycloak)
    make e2e-full    # every suite, with the dev Keycloak (also: make e2e)
    make down        # delete the cluster and its volumes

The cluster is single-node k3s in Docker Compose (`deploy/compose/compose.yaml`
with `deploy/compose/dev.yaml`). Images go through a local registry that
every checkout shares (container `javo-registry` on 127.0.0.1:5001, kept
across `make down`). Each image is tagged with a hash of its build inputs,
so it is built and pushed only when they change, once for all checkouts, and
k3s pulls only new layers. `make up` is the same command for a first install
and every update after it. Sign in as local user alice (password in
`.dev/alice.password`). `JAVO_KEYCLOAK=1 make up` adds a dev Keycloak (realm
`javo`, users alice and bob) at http://keycloak.localhost:8081/auth, and
it stays until `make down`. Only the `javo` image is built: apps and jobs
run upstream images (the pixi image, rocker/rstudio, nginx), which k3s pulls,
in pixi environments installed into each person's home on a first start
(`pod/files/envs`); `JAVO_E2E_HEAVY=1` runs the tests of the templates slow
to start the first time (RStudio, Pluto). Values: `deploy/dev/values.yaml`. Passwords are generated into
`.dev/` (mode 0600): `alice.keycloak.password`, `bob.keycloak.password`,
`keycloak-admin.password`.

End-to-end tests run in parallel: most sign in as a local account of their
own, `E2E_PARALLEL` (default 8) at a time; the ones that restart the server,
change a shared account or measure timings run alone first. A single test:
`make up`, then `KUBECONFIG=.dev/kubeconfig JAVO_E2E_TARGET=dev go test -tags e2e -run TestSampleJob ./test/e2e/`
(add `JAVO_E2E_IMAGE=$(sed -n 's/^# ref streamlit //p' .dev/images.yaml)`
for tests that seed files). `make e2e-full` also sets `JAVO_E2E_GIT=1`: dev-up
starts Gitea and a fake GitHub pull request list in namespace `javo-e2e` and
points the cluster's DNS for `github.com` at Gitea, so the Git and preview
suites run on ordinary `https://github.com/<owner>/<repo>` addresses without
leaving the cluster (`scripts/test/e2e-git-up.sh`); without it they are skipped.
`JAVO_E2E_GITHUB=1` adds one live test against the real github.com, for a
cluster without `JAVO_E2E_GIT`. `JAVO_CLUSTER=kind` runs every target on a kind
cluster instead (images loaded with `kind load`, Keycloak always on).

Each checkout gets its own cluster and host ports, so git worktrees run side
by side (`scripts/dev/env.sh`). The main checkout uses project (or kind cluster)
`javo-dev` on ports 8080 (UI, apps), 8081 (Keycloak) and 16080 (Kubernetes
API). A linked worktree uses `javo-<worktree directory>` and a free port pair
in 8100-8999 (API port: the UI port plus 8000), picked once and saved in
`.dev/kind.env`; `make up` prints the URLs and the e2e tests read the ports
from that file. `JAVO_KIND_NAME`, `JAVO_HTTP_PORT`, `JAVO_KEYCLOAK_PORT` and
`JAVO_API_PORT` override them (and are saved). `bash scripts/dev/env.sh` prints
them. A checkout runs one cluster at a time: `make up`, `JAVO_CLUSTER=kind
make up` and `make compose-up` use the same ports.

Deploy to a single-node k3s host without a registry (images are streamed over SSH):

    KUBECONFIG=/path/to/kubeconfig JAVO_SSH_HOST=user@<host> JAVO_PUBLIC_URL=https://<host> scripts/install/deploy-single-node.sh

These three are required; that deploys one host with local sign-in. Add
`JAVO_APPS_URL=https://<apps-host>` to serve apps on their own host (needed
for sharing) and `JAVO_OIDC_URL=https://<auth-host>/auth` to deploy the dev
Keycloak and sign in with it. The proxy in front terminates TLS and forwards
the UI and apps hosts to NodePort 30080, the Keycloak host to NodePort 30081.
The files it writes under `.dev/` are named after `JAVO_SITE` (default
`single-node`), so one checkout can deploy to several hosts. Run the e2e tests
there with `JAVO_E2E_TARGET=single-node JAVO_URL=https://<host>
JAVO_PASSWORD_FILE=$PWD/.dev/alice.single-node.password` (plus
`JAVO_KEYCLOAK_URL=<JAVO_OIDC_URL> JAVO_SECRETS_SUFFIX=.single-node` with
Keycloak); tests a deployment cannot run are skipped. Site-specific Helm values
(for example `gateway.trustedProxies` if your proxy reaches the gateway from
another address) go in `.dev/single-node.values.yaml` (`.dev/<JAVO_SITE>.values.yaml`),
which is untracked and applied after `deploy/single-node/values.yaml`.

## Docker Compose

The Compose install is described in [Docker Compose](/getting-started/compose/).
It is tested on Colima (Docker 29, arm64, cgroup v2), where k3s runs in the
container's own cgroup namespace; cgroup v1 hosts are untested. No images are
published yet, so `--profile build` builds them from the checkout.

From the repository root, `make compose-up` and `make compose-down` do the
same as `docker compose --profile build up -d` and
`docker compose --profile build down` in `deploy/compose/`, as project
`<JAVO_KIND_NAME>-compose` on the checkout's ports (`scripts/dev/env.sh`), and
`make compose-e2e` runs compose-up and then the end-to-end tests through the
gateway as local user alice, whom it creates (password in
`.dev/alice.compose.password`).
