# Contributing to Javo

Issues and pull requests are welcome. For a large change, open an issue first so the approach can be agreed
before you write it.

## Build and test

Requires Go (version in `go.mod`), Node 24 with pnpm, Docker with Compose v2, kubectl and Helm.

    make test                     # Go unit and integration tests on SQLite, then the store and backup packages on a throwaway Postgres in Docker
    make test-local               # the build of `javo local` (Go tag `local`): build, vet, its tests, and that it leaves out what keeps it small
    pnpm -C ui install --frozen-lockfile --ignore-scripts
    pnpm -C ui test               # UI tests
    pnpm -C ui build              # type check and production build

On a pull request, CI runs `go build`, `go vet`, the Go tests (all on SQLite, the store and backup packages also on Postgres) and
`make test-local` when the Go side changes, the three `pnpm` steps above when `ui/` changes, and the docs site build when `site/` changes.

## Run it locally

    make up        # local single-node cluster with this checkout; prints the URL
    make reload    # rebuild and roll out after a change
    make down      # delete the cluster

To run it on another Docker host on your LAN instead (UI changes show in seconds through Vite, Go changes after a reload):

    JAVO_REMOTE=user@192.0.2.10 scripts/dev/remote-up.sh   # prints the Vite URL to open
    scripts/dev/remote-reload.sh                           # after a Go change

The UI and Vite bind to the host's address (`JAVO_BIND_ADDR`, default the host in `JAVO_REMOTE`); the Kubernetes API stays on its
loopback and is reached through an ssh tunnel. `ui/` is copied to the host as you edit (log in `.dev/ui-sync.log`).

`ui/` also has `pnpm mock`, which runs the UI against canned API answers without a cluster.
[Development](site/src/content/docs/contributing/development.md) covers end-to-end tests and the other settings.

## Conventions

- Tests first. Write a test that fails for the reason you expect, then the code that makes it pass. A
  test checks behaviour someone can observe, not a value copied from the code.
- No new dependency without review. Pin it to an exact version (no ranges or `latest`), check it for
  known advisories and install scripts, and add a row to [DEPENDENCIES.md](DEPENDENCIES.md) saying what you
  checked. The same applies to version bumps, container images (pinned by digest) and GitHub Actions
  (pinned by commit SHA).
- Commit messages say in plain English what changed and why, with a short type prefix as in the
  existing history (`fix(ui): ...`, `feat: ...`, `docs: ...`).
- Logos, fonts and other third-party files are copied from a pinned upstream commit or release, with
  the source and license recorded next to them (see [ui/LOGOS.md](ui/LOGOS.md)). Nothing is fetched
  from a third-party host at runtime.
- When the HTTP API changes, regenerate `ui/openapi.json` with `make openapi` and the UI client with
  `cd ui && pnpm gen:api`.

A Developer Certificate of Origin sign-off is not required. By opening a pull request you agree that
your contribution is licensed under the [Apache License 2.0](LICENSE).

## Security issues

Do not open a public issue for a vulnerability; see [SECURITY.md](SECURITY.md).
