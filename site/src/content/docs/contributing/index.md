---
title: Contributing
description: Build and test Javo, run it locally, and the conventions for changes.
sidebar:
  order: 0
---

Issues and pull requests are welcome at [github.com/nebari-dev/javo](https://github.com/nebari-dev/javo). For a large change, open an issue first so the approach can be agreed before you write it. The full guide is [CONTRIBUTING.md](https://github.com/nebari-dev/javo/blob/main/CONTRIBUTING.md).

## Build and test

Requires Go (the version in `go.mod`), Node 24 with pnpm, Docker with Compose v2, kubectl and Helm.

```sh
make test        # Go unit and integration tests, on SQLite and on a throwaway Postgres in Docker
pnpm -C ui install --frozen-lockfile --ignore-scripts
pnpm -C ui test               # UI tests
pnpm -C ui build              # type check and production build
```

## Run it locally

```sh
make up          # local single-node cluster with this checkout; prints the URL
make reload      # rebuild and roll out after a change
make e2e-quick   # core end-to-end tests
make down        # delete the cluster
```

`ui/` also has `pnpm mock`, which runs the UI against canned API answers without a cluster. [Development](/contributing/development/) covers end-to-end tests and the other settings.

## This site

The documentation lives in `site/` and is built with [Astro Starlight](https://starlight.astro.build) and the Nebari Starlight theme.

```sh
make docs         # build into site/dist
make docs-serve   # live preview at http://localhost:4321
```

The CLI reference is generated: run `site/scripts/cli-reference.sh` after changing a command. The Helm values and REST API pages read `deploy/helm/javo/values.yaml` and `ui/openapi.json` at build time.

## Cutting a release

A release is the image `quay.io/nebari/javo:X.Y.Z` (linux/amd64 and linux/arm64) and the chart `oci://quay.io/nebari/charts/javo` version X.Y.Z. Pushing a tag `vX.Y.Z` builds and publishes both (`.github/workflows/release.yaml`):

```sh
git tag -a v0.1.3 -m "v0.1.3"
git push origin v0.1.3
```

The workflow builds the default target of the `Dockerfile`, pushes the image, then sets the chart's `version` and `appVersion` to X.Y.Z and its image defaults to `quay.io/nebari/javo`, tag X.Y.Z and the pushed digest (`scripts/release/set-chart-release.sh`), so the published chart pulls exactly the image built from the tag. The chart in the repository keeps its development defaults; only the published copy names the release image. The workflow uses only the repository's `GITHUB_TOKEN`. A package (the image or the chart) that was first pushed by hand and is not linked to this repository needs this repository added under the package's settings, Manage Actions access, with the Write role; otherwise the push is refused.

Install a release with:

```sh
helm install javo oci://quay.io/nebari/charts/javo --version 0.1.3 -n javo --create-namespace
```

## Conventions

- **Tests first.** Write a test that fails for the reason you expect, then the code that makes it pass. A test checks behaviour someone can observe, not a value copied from the code.
- **No new dependency without review.** Pin it to an exact version (no ranges or `latest`), check it for known advisories and install scripts, and record what you checked in `DEPENDENCIES.md`. The same applies to version bumps, container images (pinned by digest) and GitHub Actions (pinned by commit SHA).
- **Commit messages** say in plain English what changed and why, with a short type prefix as in the existing history (`fix(ui): ...`, `feat: ...`, `docs: ...`).
- **Third-party files** (logos, fonts) are copied from a pinned upstream commit or release, with the source and license recorded next to them. Nothing is fetched from a third-party host at runtime.
- **API changes**: regenerate `ui/openapi.json` with `make openapi` and the UI client with `cd ui && pnpm gen:api`.

## License

Javo is licensed under the [Apache License 2.0](https://github.com/nebari-dev/javo/blob/main/LICENSE). By opening a pull request you agree that your contribution is licensed under it. A Developer Certificate of Origin sign-off is not required.
