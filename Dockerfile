# syntax=docker/dockerfile:1
# Base images are pinned by index digest; provenance in DEPENDENCIES.md.
# This is the only image the project builds: apps and jobs run upstream
# images unmodified (the pixi image, plus the few listed in DEPENDENCIES.md),
# and get the pod files below from this image (internal/podfiles).

ARG BUILDARCH

FROM --platform=$BUILDPLATFORM node:22.23.4-bookworm-slim@sha256:4b150b48b4116e2a22f2c39cc27025fbf4f153e2078fd3b14d159fdb5c7279da AS ui
WORKDIR /src/ui
COPY ui/package.json ui/pnpm-lock.yaml ./
# pnpm through Node's corepack, at the version and sha512 that
# ui/package.json's packageManager names (10.34.7: 0 OSV advisories, no
# install scripts; DEPENDENCIES.md).
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable pnpm
# Cache mounts (pnpm store, Go module and build caches) persist across builds
# and checkouts on one builder, so a rebuild compiles only what changed.
RUN --mount=type=cache,id=javo-pnpm-store,target=/pnpm-store \
    pnpm install --frozen-lockfile --ignore-scripts --store-dir /pnpm-store
COPY ui/ ./
RUN pnpm build

# The terminal page of the agent templates (pod/terminal): ttyd's own
# page, read from the ttyd release binary (the version the agents
# environment installs from conda-forge), plus Fira Code and xterm.js's
# clipboard addon. Built on the build machine; the page is the same for
# every architecture. javo-api (pod/javo-api) is tested here too.
FROM scratch AS ttyd-amd64
ADD --chmod=755 --checksum=sha256:8a217c968aba172e0dbf3f34447218dc015bc4d5e59bf51db2f2cd12b7be4f55 \
    https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.x86_64 /ttyd
FROM scratch AS ttyd-arm64
ADD --chmod=755 --checksum=sha256:b38acadd89d1d396a0f5649aa52c539edbad07f4bc7348b27b4f4b7219dd4165 \
    https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.aarch64 /ttyd
FROM ttyd-${BUILDARCH} AS ttyd

FROM --platform=$BUILDPLATFORM node:22.23.4-bookworm-slim@sha256:4b150b48b4116e2a22f2c39cc27025fbf4f153e2078fd3b14d159fdb5c7279da AS pod-files
COPY --from=ttyd /ttyd /usr/local/bin/ttyd
ADD --checksum=sha256:6cbc7814658cd5e57763c98dd3e3ce79d2adefe2a9e4f96ff97fd40a45ba39e4 \
    https://registry.npmjs.org/@xterm/addon-clipboard/-/addon-clipboard-0.1.0.tgz /tmp/addon-clipboard.tgz
COPY pod/terminal/ /src/terminal/
COPY pod/files/fonts/FiraCode-VF.woff2 /src/terminal/
COPY pod/javo-api/ /src/javo-api/
RUN cd /src/terminal \
 && tar -xzf /tmp/addon-clipboard.tgz -C /tmp package/lib/addon-clipboard.js \
 && node --test page.test.mjs \
 && mkdir -p /out \
 && node page.mjs /tmp/package/lib/addon-clipboard.js /out/terminal.html \
 && cd /src/javo-api && node --test javo-api.test.mjs

FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=javo-go-mod,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,id=javo-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=javo-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/javo ./cmd/javo

# restic 0.19.1 (backups to the S3-compatible bucket): restic's own image,
# pinned by index digest; the image for each architecture carries that
# architecture's release binary (DEPENDENCIES.md).
FROM restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510 AS restic

# The nebi 0.15 CLI (copied into pods of apps with Nebi access): the upstream
# release tarballs, checked against the sha256 in the release's checksums.txt
# (DEPENDENCIES.md). Both are fetched on the build machine; the one for the
# image's architecture is unpacked.
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS nebi
ARG TARGETARCH
ADD --checksum=sha256:d866977f03e50e1d56d01ae83adcd84120600996a0ff9133f2be0e981de8dd59 \
    https://github.com/nebari-dev/nebi/releases/download/v0.15/nebi_0.15_linux_x86_64.tar.gz /dl/nebi-amd64.tar.gz
ADD --checksum=sha256:ff0b0aa61cf29db013efd71659167476f4401fcaa163beb89474b4aa2480facf \
    https://github.com/nebari-dev/nebi/releases/download/v0.15/nebi_0.15_linux_arm64.tar.gz /dl/nebi-arm64.tar.gz
RUN mkdir -p /out && tar -xzf /dl/nebi-${TARGETARCH}.tar.gz -C /out nebi

# Compose install (deploy/compose): the single-node k3s it runs and the
# one-shot installer. Built only by deploy/compose/compose.yaml; the default
# target (the last stage) does not use them.
FROM rancher/k3s:v1.37.1-k3s1@sha256:ca7f37d993d82ef0dcdcfecb2e0e2618ea541dbaffc620c8cedebe01a82acd0d AS k3s
# Cluster DNS through Docker's own resolver, then k3s (see the script).
COPY deploy/compose/k3s-entrypoint.sh /bin/javo-k3s-entrypoint
ENTRYPOINT ["/bin/javo-k3s-entrypoint"]

FROM docker:29.9.0-cli@sha256:1a4c7cb63513f349bdad01fcc6e0f3f2f67d37b9da86f14dc0d4a0942eecda00 AS docker-cli

# Helm 4.3.1 release binary, checked against the sha256 published with it.
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS helm
ARG TARGETARCH
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) sum=0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b ;; \
      arm64) sum=8cf8f66ecbc252ac8f7f06cca68aba88508ddfde3a6a34feb1678ad1694b4f88 ;; \
      *) echo "no helm checksum for $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/helm.tgz "https://get.helm.sh/helm-v4.3.1-linux-$TARGETARCH.tar.gz"; \
    echo "$sum  /tmp/helm.tgz" | sha256sum -c -; \
    tar -xzf /tmp/helm.tgz -C /tmp; \
    mkdir -p /out; \
    install -m 0755 "/tmp/linux-$TARGETARCH/helm" /out/helm

# k3s's busybox sh, kubectl and ctr, plus the docker CLI (to copy locally
# built images into k3s), helm, the javo CLI (javo user add), the chart and the
# shared install scripts.
FROM k3s AS compose-installer
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=helm /out/helm /usr/local/bin/helm
COPY --from=build /out/javo /usr/local/bin/javo
COPY deploy/helm/javo/ /opt/javo/deploy/helm/javo/
COPY scripts/install/install-chart.sh scripts/install/ensure-local-user.sh scripts/install/wait-healthz.sh /opt/javo/scripts/install/
COPY deploy/compose/install.sh deploy/compose/values.yaml /opt/javo/deploy/compose/
WORKDIR /opt/javo
ENTRYPOINT ["/opt/javo/deploy/compose/install.sh"]

# The javo binary for the image's architecture, at /linux_<arch>/javo. The
# release workflow compiles it on the runner, with Go's build cache kept
# between runs, and passes that folder in place of this stage (build context
# javo-bin).
FROM scratch AS javo-bin
ARG TARGETARCH
COPY --from=build /out/javo /linux_${TARGETARCH}/javo

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ARG TARGETARCH
COPY --from=javo-bin /linux_${TARGETARCH}/javo /javo
COPY --from=restic /usr/bin/restic /usr/local/bin/restic
# Not a pod file: only pods of apps with Nebi access copy it.
COPY --from=nebi /out/nebi /opt/javo-nebi-files/nebi
COPY --from=ui /src/ui/dist /ui
# The pod files (internal/podfiles copies them into each pod that needs
# them): pod-user.sh, the shell defaults, the samples, the bundled pixi
# environments, the terminal page and javo-api.
COPY pod/files/ /opt/javo-files/
COPY --from=pod-files /out/terminal.html /opt/javo-files/agents/terminal.html
COPY --chmod=755 pod/javo-api/javo-api.mjs /opt/javo-files/bin/javo-api
COPY --chmod=755 pod/bin/ /opt/javo-files/bin/
USER 65532:65532
ENTRYPOINT ["/javo"]
