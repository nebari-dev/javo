LOCALBIN := $(CURDIR)/bin

CONTROLLER_GEN := $(LOCALBIN)/controller-gen
SETUP_ENVTEST := $(LOCALBIN)/setup-envtest
SQLC := $(LOCALBIN)/sqlc
# restic for the backup tests: the release binary the image carries (scripts/build/fetch-restic.sh).
RESTIC := $(LOCALBIN)/restic
export JAVO_TEST_RESTIC := $(RESTIC)

CONTROLLER_TOOLS_VERSION := v0.23.0
SETUP_ENVTEST_VERSION := v0.25.2
# Keep in step with the version and checksums in scripts/build/fetch-sqlc.sh.
SQLC_VERSION := 1.31.1
ENVTEST_K8S_VERSION := 1.37.0

.PHONY: generate generate-sql rbac test test-db test-postgres envtest-path db-packages openapi docs docs-serve up dev-up reload down dev-down e2e e2e-quick e2e-full e2e-ai e2e-github compose-up compose-down compose-e2e

# Deep copies for internal/model.
generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile=scripts/build/boilerplate.go.txt paths="./internal/model/..."

# RBAC rules from the +kubebuilder:rbac markers, one ClusterRole per
# component. The chart's Roles are written by hand (they are split across
# two namespaces); test/helm fails when they grant different rules.
rbac: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) rbac:roleName=javo-controller paths="./internal/controller/...;./internal/jobs/notify/...;./internal/netprobe/..." output:rbac:artifacts:config=config/rbac/controller
	$(CONTROLLER_GEN) rbac:roleName=javo-gateway paths=./internal/cli/servercmd/... output:rbac:artifacts:config=config/rbac/gateway

# Generated query code for internal/store (committed).
generate-sql: $(SQLC)
	cd internal/store && $(SQLC) generate

# The tests that run through storetest, on both engines: SQLite, then Postgres in a throwaway
# container (scripts/test/postgres-test.sh). A missing Postgres fails the tests.
DB_PACKAGES = ./internal/store/... ./internal/ai/... ./internal/backup/... ./internal/secrets/... ./internal/storage/... ./internal/auth/... ./internal/gateway/... ./internal/templates/... ./internal/controller/dbsource/... ./internal/controller/workspace/... ./internal/controller/storage/... ./internal/controller/namespace/... ./internal/controller/jobs/... ./internal/controller/idle/... ./internal/controller/preview/... ./internal/jobs/notify/... ./internal/netprobe/... ./internal/identity/... ./internal/cli/...

# The packages whose code differs between SQLite and Postgres (queries, the
# change feed, backups); CI and `make test` run only these on Postgres too.
PG_PACKAGES = ./internal/store/... ./internal/backup/...

# Some of these packages (internal/controller) have envtest tests, so both lines get the envtest binaries.
test-db: $(SETUP_ENVTEST) $(RESTIC)
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path)" JAVO_TEST_DB=sqlite go test -count=1 $(DB_PACKAGES)
	$(MAKE) test-postgres

test-postgres: $(SETUP_ENVTEST) $(RESTIC)
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path)" scripts/test/postgres-test.sh env JAVO_TEST_DB=postgres go test -count=1 $(PG_PACKAGES)

# Everything on SQLite (the default engine), then the engine-specific packages on Postgres.
test: $(SETUP_ENVTEST) $(RESTIC) test-postgres
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path)" go test ./...

# The KUBEBUILDER_ASSETS path for the envtest binaries, downloading them if needed (CI).
envtest-path: $(SETUP_ENVTEST)
	@$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path

# The packages the Postgres pass runs (CI).
db-packages:
	@echo $(PG_PACKAGES)

# The UI client is generated from this file (cd ui && pnpm gen:api).
openapi:
	go run ./cmd/javo openapi > ui/openapi.json

# The documentation site (site/): build into site/dist, or serve it with live
# reload at http://localhost:4321. Installs exactly site/pnpm-lock.yaml with
# lifecycle scripts off (site/.npmrc).
docs:
	cd site && pnpm install --frozen-lockfile && pnpm exec astro build

docs-serve:
	cd site && pnpm install --frozen-lockfile && pnpm exec astro dev

# The dev loop. Each checkout has its own cluster and host ports
# (scripts/dev/env.sh): by default single-node k3s in Docker Compose, project
# JAVO_KIND_NAME (javo-dev in the main checkout); JAVO_CLUSTER=kind uses a kind
# cluster instead. Images go through a local registry every checkout shares
# and are rebuilt only when their inputs change (scripts/build/images.sh).
#   make up       create or update the cluster and the release; prints the URL
#   make reload   rebuild the javo image only and roll the release to it
#   make down     delete the cluster and its volumes
# JAVO_KEYCLOAK=1 adds the dev Keycloak. Only the javo image is built: apps and
# jobs run upstream images in pixi environments (pod/files/envs).
up dev-up:
	scripts/dev/up.sh

reload:
	scripts/dev/reload.sh

down dev-down:
	scripts/dev/down.sh

# End-to-end tests through the gateway, on this checkout's cluster. Tests on
# accounts of their own run side by side (E2E_PARALLEL at a time); the ones
# that restart or reconfigure the server run alone first.
#   e2e-quick  the core suites, without Keycloak: a few minutes
#   e2e-full   every suite, with the dev Keycloak and the hermetic Git side
#              (JAVO_E2E_GIT=1: Gitea and a fake GitHub pull request list, for
#              the Git and preview suites; also: make e2e); then the
#              namespace-modes test, which reconfigures the release
# JAVO_E2E_GITHUB=1 adds one live smoke test against github.com, for a cluster
# without JAVO_E2E_GIT (whose DNS rule sends github.com to Gitea).
# JAVO_DB=postgres runs them on Postgres (see scripts/dev/up.sh).
JAVO_CLUSTER ?= compose
export JAVO_CLUSTER
E2E_PARALLEL ?= 8
E2E_QUICK := ^(TestEndToEnd|TestTypedCommand|TestContainerImage|TestImageRunsAsRoot|TestSampleJob|TestJobRunsAtItsSize|TestCustomCommandJob|TestAPITokens|TestAPIDocsPage|TestAppWithReadAPIAccess|TestAppGetsASecret|TestJobGetsASecret|TestPasswordReset|TestSizeBiggerThanTheNode)$$
# JAVO_E2E_HEAVY=1 adds the templates slow to start the first time (RStudio's
# large image, Pluto's precompile).
ifeq ($(JAVO_CLUSTER),kind)
E2E_ENV = KUBECONFIG=$(CURDIR)/.dev/kubeconfig JAVO_E2E_TARGET=kind
else
E2E_ENV = KUBECONFIG=$(CURDIR)/.dev/kubeconfig JAVO_E2E_TARGET=dev
endif
E2E_TEST = go test -tags e2e -count=1 -v -parallel $(E2E_PARALLEL)

e2e-quick: up
	$(E2E_ENV) $(E2E_TEST) -timeout 15m -run '$(E2E_QUICK)' ./test/e2e/

# The AI suites: the rules diagnosis, plans and MCP with AI off, then AI on
# against a fake OpenAI-compatible model (test/e2e/testdata/fakemodel.py);
# no real model is called.
e2e-ai: up
	$(E2E_ENV) JAVO_E2E_AI=1 $(E2E_TEST) -timeout 20m -run '^(TestDiagnosisPlansAndMCP|TestAIWithAFakeModel)$$' ./test/e2e/

e2e e2e-full:
	JAVO_KEYCLOAK=1 JAVO_E2E_GIT=1 scripts/dev/up.sh
	$(E2E_ENV) JAVO_E2E_KEYCLOAK=1 $(E2E_TEST) -timeout 40m ./test/e2e/
	$(E2E_ENV) JAVO_E2E_KEYCLOAK=1 JAVO_E2E_NAMESPACE_MODES=1 $(E2E_TEST) -timeout 15m -run TestNamespaceModes ./test/e2e/

# The live smoke test against github.com: previews on, no hermetic Git (its DNS
# rule would catch github.com).
e2e-github:
	JAVO_E2E_GITHUB=1 scripts/dev/up.sh
	$(E2E_ENV) JAVO_E2E_GITHUB=1 $(E2E_TEST) -timeout 20m -run '^TestLiveGitHub$$' ./test/e2e/

# Docker Compose install (deploy/compose), the way people install it: build
# the images from this checkout, install, follow the installer and fail when
# it fails. Project <JAVO_KIND_NAME>-compose on this checkout's ports
# (scripts/dev/env.sh), so run it or make up, not both. JAVO_PORT, JAVO_PUBLIC_URL,
# JAVO_API_PORT and JAVO_IMAGE_TAG are read from the environment.
DEV_ENV = $(shell bash scripts/dev/env.sh)
dev_env = $(patsubst $(1)=%,%,$(filter $(1)=%,$(DEV_ENV)))
COMPOSE = docker compose -p $(call dev_env,JAVO_KIND_NAME)-compose -f deploy/compose/compose.yaml --profile build
COMPOSE_PORTS = JAVO_PORT=$(or $(JAVO_PORT),$(call dev_env,JAVO_HTTP_PORT)) JAVO_API_PORT=$(or $(JAVO_API_PORT),$(call dev_env,JAVO_API_PORT))
COMPOSE_URL = $(or $(JAVO_PUBLIC_URL),http://localhost:$(or $(JAVO_PORT),$(call dev_env,JAVO_HTTP_PORT)))

compose-up:
	$(COMPOSE_PORTS) $(COMPOSE) up -d
	$(COMPOSE_PORTS) $(COMPOSE) logs -f --no-log-prefix installer
	@test "$$($(COMPOSE_PORTS) $(COMPOSE) ps -a --format '{{.ExitCode}}' installer)" = 0

compose-down:
	$(COMPOSE_PORTS) $(COMPOSE) down

# compose-up, then every end-to-end suite through the gateway as local user
# alice (created for the tests). Keycloak suites are skipped.
compose-e2e: compose-up
	mkdir -p .dev
	$(COMPOSE_PORTS) $(COMPOSE) exec -T k3s cat /var/lib/javo-kubeconfig/kubeconfig.yaml | \
	  sed 's#127.0.0.1:6443#127.0.0.1:$(or $(JAVO_API_PORT),$(call dev_env,JAVO_API_PORT))#' > .dev/compose.kubeconfig
	chmod 600 .dev/compose.kubeconfig
	KUBECONFIG=$(CURDIR)/.dev/compose.kubeconfig scripts/install/ensure-local-user.sh javo-system alice .dev/alice.compose.password
	JAVO_E2E_TARGET=compose JAVO_URL=$(COMPOSE_URL) JAVO_PASSWORD_FILE=$(CURDIR)/.dev/alice.compose.password \
	  KUBECONFIG=$(CURDIR)/.dev/compose.kubeconfig $(E2E_TEST) -timeout 40m ./test/e2e/

$(CONTROLLER_GEN):
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

$(SQLC): scripts/build/fetch-sqlc.sh
	scripts/build/fetch-sqlc.sh $(SQLC)
	@test "$$($(SQLC) version)" = "v$(SQLC_VERSION)"

$(RESTIC): scripts/build/fetch-restic.sh
	scripts/build/fetch-restic.sh $(RESTIC)

$(SETUP_ENVTEST):
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)
