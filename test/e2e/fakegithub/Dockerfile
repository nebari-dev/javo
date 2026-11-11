# Test-only (scripts/dev/up.sh with JAVO_E2E_GIT=1); base images are the ones the
# main Dockerfile pins (provenance in DEPENDENCIES.md).
FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS build
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/fakegithub main.go

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/fakegithub /fakegithub
ENTRYPOINT ["/fakegithub"]
