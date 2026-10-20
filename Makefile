SHELL := /bin/bash
MODULE := github.com/aero/aero-id
BIN    := bin/aero-id
PG_DSN ?= postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable
PNPM  ?= pnpm
WEB_DIR := web

.PHONY: tidy build build-audit-replay build-preflight run test test-integration test-vault-contract lint clean proto web-install web-dev web-test web-lint web-build web-check load-test-http load-test-grpc load-test-event load-test-callback load-test-db load-test-db-seed check-integration-compile check-load-scenarios check-k8s-manifest render-k8s render-migration-k8s
tidy:        ; go mod tidy
build:       ; go build -o $(BIN) ./cmd/server
	@$(MAKE) --no-print-directory build-audit-replay
	@$(MAKE) --no-print-directory build-preflight
# Operator replay tool for dead-lettered audit publications/deliveries (B2-5);
# built alongside the server so bin/ ships both binaries (no repo-root binaries).
build-audit-replay: ; go build -o bin/audit-replay ./cmd/audit-replay
build-preflight: ; go build -o bin/preflight ./cmd/preflight
run:         ; go run ./cmd/server -config configs/config.yaml
test:        ; go test ./... -count=1
# Integration tests use Docker-owned fixtures. Keep both the per-package Go
# timeout and an outer process timeout: a stuck Docker/Testcontainers wait
# must fail the CI job rather than consume the runner indefinitely.
INTEGRATION_TEST_TIMEOUT ?= 15m
INTEGRATION_PROCESS_TIMEOUT ?= 20m
test-integration:
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: Docker is required for integration tests" >&2; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "ERROR: Docker daemon is unavailable for integration tests" >&2; exit 1; }
	timeout --foreground --kill-after=30s $(INTEGRATION_PROCESS_TIMEOUT) \
	  go test -tags=integration -p 1 -timeout=$(INTEGRATION_TEST_TIMEOUT) ./... -count=1
# Diagnostic full-suite alternative: two isolated Go package processes, not
# shared fixtures. Keep the original serial gate above unchanged. No filtering,
# retry, cache, longer timeout or inherited GOFLAGS may turn this into a subset.
.PHONY: test-integration-bounded check-integration-runner
test-integration-bounded:
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: Docker is required for integration tests" >&2; exit 1; }
	@timeout --foreground --kill-after=5s 30s docker info >/dev/null 2>&1 || { echo "ERROR: Docker daemon is unavailable for integration tests" >&2; exit 1; }
	GOTOOLCHAIN=local GOWORK=off GOFLAGS= timeout --foreground --kill-after=30s $(INTEGRATION_PROCESS_TIMEOUT) \
	  go test -mod=readonly -json -tags=integration -p 2 -timeout=$(INTEGRATION_TEST_TIMEOUT) ./... -count=1

# Docker-free execution-contract tests probe the real Make recipes using only
# private recording commands; they are not fixture or full-suite acceptance.
check-integration-runner:
	GOTOOLCHAIN=local GOWORK=off GOFLAGS= timeout --foreground --kill-after=5s 2m \
	  go test -mod=readonly ./internal/config -run '^TestIntegrationRunner' -count=1 -timeout=60s

# The real Vault contract suite is external-deployment backed and is kept
# separate from the PostgreSQL/MinIO integration fixture. It skips when no
# deployment is configured, or fails closed when *_REQUIRED=true is set.
VAULT_CONTRACT_TEST_TIMEOUT ?= 10m
test-vault-contract:
	go test -tags=integration ./internal/connector/aerovault ./internal/app \
	  -run '^(TestVaultContract|TestBuildSourceConnectorsAgainstConfiguredVault)' \
	  -count=1 -timeout=$(VAULT_CONTRACT_TEST_TIMEOUT)
lint:        ; go vet ./...
clean:       ; rm -rf bin/ web/dist/

web-install: ; cd $(WEB_DIR) && $(PNPM) install --frozen-lockfile
web-dev:     ; cd $(WEB_DIR) && $(PNPM) dev
web-test:    ; cd $(WEB_DIR) && $(PNPM) test
web-lint:    ; cd $(WEB_DIR) && $(PNPM) lint
web-build:   ; cd $(WEB_DIR) && $(PNPM) build
web-check:   ; cd $(WEB_DIR) && $(PNPM) check

# ── Strict lint gate (HARNESS.md) ───────────────────────────────────────
# Enforces: file ≤500 lines, function ≤50 lines, complexity ≤10.
# Requires golangci-lint (install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
GOLANGCI_LINT ?= $(shell command -v golangci-lint 2>/dev/null || printf '%s/bin/golangci-lint' "$$(go env GOPATH)")

.PHONY: lint-strict check-file-sizes check-composition-root check-proto-sync check-k8s-manifest render-k8s render-migration-k8s
lint-strict:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { echo "ERROR: golangci-lint not found. Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; exit 1; }
	$(GOLANGCI_LINT) run ./...

# Compile guard for integration-tagged _test.go files (FM-11): plain
# `go build -tags integration` does not compile _test.go, so an interface
# change that breaks an integration-only fake is invisible to the default
# gate. `go test -tags integration -run '^$'` compiles every test file and
# runs nothing; `go vet -tags integration` type-checks them too. Docker-free
# (no testcontainers started).
.PHONY: check-integration-compile
check-integration-compile:
	go test -tags integration -run '^$$' ./...
	go vet -tags integration ./...

# Enforce one-way dependency: no cross-feature imports.
.PHONY: check-architecture
check-architecture:
	@bash scripts/check-architecture.sh

# Load-test tooling drift gate (P6-08): canonical ghz scenario source (calls
# resolve in the proto, runner == scenarios.yaml), bench SQL relations exist
# in migrations, Makefile-referenced load assets are live. Pure bash + grep;
# self-testing fixtures; CI-safe (no Go/Docker/network). The script header is
# the canonical rule definition.
.PHONY: check-load-scenarios
check-load-scenarios:
	@bash scripts/check-load-scenarios.sh

# Migration-set integrity gate (2026-08-16): up/down pair invariant (G1), no
# duplicate versions (G2), no malformed migration-like filenames (G3) — the
# AGENTS.md "Every zero-padded NNNNNN_name.up.sql has a matching .down.sql"
# contract enforced by strict Discover() (internal/pkg/migrate) and this bash
# gate. Pure bash + grep; self-testing fixtures; CI-safe (no Go/Docker/network).
# The script header is the canonical rule definition.
.PHONY: check-migration-set
check-migration-set:
	@bash scripts/check-migration-set.sh

# Fail if any non-generated, non-test .go file exceeds 500 lines.
MAX_FILE_LINES ?= 500
# Composition-root budget: internal/app/new.go must stay <= this many lines
# (C-7 pin inside scripts/check-composition-root.sh; 400 passes, 401 trips).
MAX_COMPOSITION_ROOT_LINES ?= 400
check-file-sizes:
	@violations=0; \
	while IFS= read -r f; do \
	  n=$$(wc -l < "$$f"); \
	  if [ "$$n" -gt $(MAX_FILE_LINES) ]; then \
	    echo "VIOLATION: $$f ($$n lines > $(MAX_FILE_LINES))"; \
	    violations=$$((violations + 1)); \
	  fi; \
	done < <(find internal/ -name '*.go' \
	  ! -name '*.pb.go' ! -name '*_test.go' \
	  ! -path '*/testpg/*' ! -path '*/mocks/*'); \
	if [ "$$violations" -gt 0 ]; then \
	  echo ""; echo "$$violations file(s) exceed $(MAX_FILE_LINES)-line limit. Refactor before continuing."; \
	  exit 1; \
	fi; \
	echo "✓ All files ≤ $(MAX_FILE_LINES) lines"

# Ban swallowed/bare-call audit writes in the live service layer (single
# delivery + swallow ban). Self-tests against scripts/testdata/audit-swallow/
# before scanning; ROOT parameterized for the self-test fixture. The
# internal/comment root is a quarantine TRIPWIRE, not a live scan: the tree
# was deleted in the 2026-08-08 dead-tree quarantine (its service.go:431
# `_ = s.audit.Log(` was the last swallowed fail-open out-of-transaction
# audit write), the FM-12 guard in the script is the total tripwire, and the
# comment_positive self-test pins both the root and the P1/P2 patterns. This
# root also supersedes the live-root claims of the mutation-coverage campaign
# (2026-08-08-internal-comment-mutation-audit-coverage) and its R0 predecessor
# (2026-08-08-internal-comment-resolve-report-with-audit): Option A
# (quarantine) was chosen over Option B (port), so the token means tripwire.
.PHONY: check-audit-swallow
check-audit-swallow:
	@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit internal/connector internal/outbox internal/event internal/hub internal/region internal/sso internal/idempotency internal/middleware internal/config internal/handler

# R-1/R-2 regression gate for the deterministic event-ID derivation contract:
# the only random EventID: idgen.NewID() left in non-test Go is the C2
# callback replay site; derivation sources stay confined to idgen +
# projection_replication.go; AnchorNamespace is defined exactly once.
.PHONY: check-anchored-event-ids
check-anchored-event-ids:
	@bash scripts/check-anchored-event-ids.sh internal cmd

# Enumerated bare-site manifest gate (sweep-and-lock REQ-1): every bare
# s.audit.Record/Log site in internal/service must be enumerated in
# scripts/check-audit-manifest.sh with its audit-site marker; a NEW mutation
# path cannot regress to a non-tx Record without landing a manifest row and
# its integration proof.
.PHONY: check-audit-manifest
check-audit-manifest:
	@bash scripts/check-audit-manifest.sh

# Database startup/pool hardening gate: keep the bounded DSN wiring and the
# deliberate removal of dead contextless db.Ping mechanically pinned.
.PHONY: check-db-startup-hardening
check-db-startup-hardening:
	@bash scripts/check-db-startup-hardening.sh

# Composition-root seam gate (Mutation-A pin): the buildAuditStack call and
# the drain Start must stay in internal/app/new.go with their exact shapes
# (C-1/C-2), the drain must not move into buildAuditStack (C-2b), the stack
# precedes NewFileRepository (C-3), cmd/server/main.go hands the Loaded
# config to app.New unmutated (C-4), and Vault construction remains behind
# internal/connector/aerovault (C-8). Docker-free companion to the boot e2e
# (internal/app/composition_root_integration_test.go).
.PHONY: check-composition-root
check-composition-root:
	@MAX_COMPOSITION_ROOT_LINES=$(MAX_COMPOSITION_ROOT_LINES) bash scripts/check-composition-root.sh

# Deterministic proto-stub sync gate: regenerates every committed *.pb.go from
# api/proto/ with a PINNED toolchain (protoc-gen-go derived from go.mod,
# protoc-gen-go-grpc v1.5.1, protoc 3.21.12) into a temp tree and diffs against
# the committed stubs. Self-testing (green+red fixtures); never mutates the repo.
.PHONY: check-proto-sync
check-proto-sync:
	@bash scripts/check-proto-sync.sh

# Kubernetes release-template gate: source-disabled base, optional feature
# secrets, immutable images, config checksum annotations, and init preflight.
# Mandatory offline strict schema validation in the existing deployments/k8s
# test package, including all five renders and rejection/integrity/budget tests.
# Schema bytes/GVKs are pinned; no kubectl, kubeconfig or missing-schema skip.
# Go module provisioning is separate from cluster-free schema validation.
check-k8s-manifest:
	@bash scripts/check-k8s-manifest.sh

# Render a release-specific cell manifest. REGION and
# AERO_ID_IMAGE_DIGEST=sha256:<64 lowercase hex> are required; output may be
# redirected or selected with OUTPUT=/path/to/rendered.yaml.
render-k8s:
	@REGION="$${REGION:?set REGION}" AERO_ID_IMAGE_DIGEST="$${AERO_ID_IMAGE_DIGEST:?set AERO_ID_IMAGE_DIGEST}" \
	  bash scripts/render-regional-cell.sh deployments/k8s/regional-cell.yaml "$${OUTPUT:--}"

# Render the one-shot migration Job separately so a steady-state apply never
# creates a second migration. RELEASE must be unique per attempt.
render-migration-k8s:
	@RELEASE="$${RELEASE:?set RELEASE}" AERO_ID_IMAGE_DIGEST="$${AERO_ID_IMAGE_DIGEST:?set AERO_ID_IMAGE_DIGEST}" \
	  MIGRATION_ACTION="$${MIGRATION_ACTION:-up}" MIGRATION_COUNT="$${MIGRATION_COUNT:-1}" \
	  bash scripts/render-migration-job.sh deployments/k8s/migration-job.yaml "$${OUTPUT:--}"

proto:
	mkdir -p internal/sso/ssopb internal/handler/grpc/aeropb
	protoc \
	  --proto_path=api/proto \
	  --go_out=. --go_opt=module=github.com/aero/aero-id \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/aero/aero-id \
	api/proto/sso/v1/sso.proto \
	api/proto/aero_id/v1/aero_id.proto \
	api/proto/snaplink/authz/v1/authz.proto \
	api/proto/snaplink/audit/v1/audit.proto \
	api/proto/aero_id/v1/event.proto

MIGRATIONS_DIR := migrations
CONFIG         ?= configs/config.yaml
# N is the number of steps for migrate-down (default 1).
N              ?= 1

# Migration tooling — runs the in-repo cmd/migrate against the configured DB
# (reads the DSN from $(CONFIG); no external `migrate` binary required).
.PHONY: migrate-up migrate-down migrate-version migrate-verify migrate-validate
migrate-up:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) up
migrate-down:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) down $(N)
migrate-version:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) version
migrate-verify:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) verify
migrate-validate:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) validate

# ── Load tests (P6-08) —───────────────────────────────────────────────────
# HTTP via k6; gRPC via ghz. See scripts/load/README.md.
BASE_URL      ?= http://localhost:8080
TOKEN         ?=
VUS           ?= 50
DURATION      ?= 3m

load-test-http:
	@command -v k6 >/dev/null 2>&1 || { echo "ERROR: k6 not found (https://grafana.com/docs/k6/latest/set-up/install-k6/)"; exit 1; }
	@test -n "$(TOKEN)" || { echo "ERROR: TOKEN=<snaplink-bearer-token> is required"; exit 1; }
	mkdir -p scripts/load/results
	k6 run -e BASE_URL=$(BASE_URL) -e TOKEN=$(TOKEN) -e VUS=$(VUS) -e DURATION=$(DURATION) \
	  --out json=scripts/load/results/k6-$$(date +%Y%m%d-%H%M%S).json \
	  scripts/load/k6/account.js

load-test-grpc:
	@command -v ghz >/dev/null 2>&1 || { echo "ERROR: ghz not found (https://ghz.sh/docs/install)"; exit 1; }
	@test -n "$(TOKEN)" || { echo "ERROR: TOKEN=<snaplink-bearer-token> is required"; exit 1; }
	mkdir -p scripts/load/results
	bash scripts/load/run-ghz.sh "$(TOKEN)" "$(BASE_URL_GRPC)"

# Event ingest soak: canonical event batches with per-source HMAC signatures
# plus duplicate redelivery idempotency checks. The load account needs the
# source_event_ingest permission and a binding for SOURCE.
SOURCE      ?= aero-im
SOURCE_KEY  ?=
BATCH       ?= 10

load-test-event:
	@command -v k6 >/dev/null 2>&1 || { echo "ERROR: k6 not found (https://grafana.com/docs/k6/latest/set-up/install-k6/)"; exit 1; }
	@test -n "$(TOKEN)" || { echo "ERROR: TOKEN=<snaplink-bearer-token> is required"; exit 1; }
	@test -n "$(SOURCE_KEY)" || { echo "ERROR: SOURCE_KEY=<event_ingest.source_keys[SOURCE]> is required"; exit 1; }
	mkdir -p scripts/load/results
	k6 run -e BASE_URL=$(BASE_URL) -e TOKEN=$(TOKEN) -e SOURCE=$(SOURCE) -e SOURCE_KEY=$(SOURCE_KEY) \
	  -e BATCH=$(BATCH) -e VUS=$(VUS) -e DURATION=$(DURATION) \
	  --out json=scripts/load/results/k6-event-$$(date +%Y%m%d-%H%M%S).json \
	  scripts/load/k6/event-soak.js

# Callback soak: register subscriptions against a local sink, trigger audited
# profile operations, and assert signed at-least-once deliveries arrive.
# Requires scripts/load/sink running (go run ./scripts/load/sink -addr :9091)
# and a RECEIVER_URL reachable from the cell.
RECEIVER_URL ?= http://127.0.0.1:9091

load-test-callback:
	@command -v k6 >/dev/null 2>&1 || { echo "ERROR: k6 not found (https://grafana.com/docs/k6/latest/set-up/install-k6/)"; exit 1; }
	@test -n "$(TOKEN)" || { echo "ERROR: TOKEN=<snaplink-bearer-token> is required"; exit 1; }
	mkdir -p scripts/load/results
	k6 run -e BASE_URL=$(BASE_URL) -e TOKEN=$(TOKEN) -e RECEIVER_URL=$(RECEIVER_URL) \
	  -e VUS=$(VUS) -e DURATION=$(DURATION) \
	  --out json=scripts/load/results/k6-callback-$$(date +%Y%m%d-%H%M%S).json \
	  scripts/load/k6/callback-soak.js

# DB partition/index benchmark: EXPLAIN ANALYZE over the hot control-plane
# and worker queries. Requires psql and a SEEDED bench database.
# PG_DSN_BENCH must be set explicitly — there is deliberately NO fallback to
# the app DSN (PG_DSN): running EXPLAIN ANALYZE against the live app DB is
# the hazard this guard chain exists to prevent. Guards:
#   1. explicit DSN (no default);
#   2. app-DSN refusal when PG_DSN_BENCH equals PG_DSN, unless
#      BENCH_ALLOW_APP_DSN=1 (dangerous — deliberate operator action only;
#      string equality is imperfect, which is why guard 3 is the substantive
#      protection);
#   3. seed-sanity probe on the aero_load_bench_seed marker (created only by
#      make load-test-db-seed; the app DB never contains it), unless
#      BENCH_SKIP_SEED_CHECK=1 (explicit escape hatch).
# Seed the bench cell first:
#   make load-test-db-seed PG_DSN_BENCH=<bench-dsn>
load-test-db:
	@test -n "$(PG_DSN_BENCH)" || { echo "ERROR: PG_DSN_BENCH must be set explicitly (bench DB; never the app DB). See make load-test-db-seed / scripts/load/README.md"; exit 1; }
	@command -v psql >/dev/null 2>&1 || { echo "ERROR: psql not found (postgresql-client) — install it or set PG_DSN_BENCH"; exit 1; }
	@test "$(PG_DSN_BENCH)" != "$(PG_DSN)" || test "$(BENCH_ALLOW_APP_DSN)" = "1" || { echo "ERROR: PG_DSN_BENCH equals the app DSN (PG_DSN) — refusing. Set BENCH_ALLOW_APP_DSN=1 only for a deliberately isolated app-schema bench cell."; exit 1; }
	@test "$(BENCH_SKIP_SEED_CHECK)" = "1" || { marker=$$(psql "$(PG_DSN_BENCH)" -v ON_ERROR_STOP=1 -tA -c "SELECT schema_version FROM aero_load_bench_seed LIMIT 1" 2>/dev/null || true); test -n "$$marker" || { echo "ERROR: $(PG_DSN_BENCH) is not a seeded bench database — run: make load-test-db-seed PG_DSN_BENCH=$(PG_DSN_BENCH)"; exit 1; }; test "$$marker" -ge 1 || echo "WARNING: aero_load_bench_seed schema_version=$$marker (< 1) — stale seed; consider make load-test-db-seed"; }
	psql "$(PG_DSN_BENCH)" -v ON_ERROR_STOP=1 -f scripts/load/db/partition-index-bench.sql

# Idempotent seed of the bench database: creates the aero_load_bench_seed
# marker (schema_version, seeded_at, per-table row counts) and populates the
# hot tables at the volumes documented in scripts/load/db/seed-bench.sql
# (override per-cell with psql variables, e.g. AUDIT_LEDGER_ROWS=2000000).
# Same explicit-DSN + app-DSN-refusal guards as load-test-db (no seed check —
# this target creates the marker).
load-test-db-seed:
	@test -n "$(PG_DSN_BENCH)" || { echo "ERROR: PG_DSN_BENCH must be set explicitly (bench DB; never the app DB). See scripts/load/README.md"; exit 1; }
	@command -v psql >/dev/null 2>&1 || { echo "ERROR: psql not found (postgresql-client) — install it or set PG_DSN_BENCH"; exit 1; }
	@test "$(PG_DSN_BENCH)" != "$(PG_DSN)" || test "$(BENCH_ALLOW_APP_DSN)" = "1" || { echo "ERROR: PG_DSN_BENCH equals the app DSN (PG_DSN) — refusing. Set BENCH_ALLOW_APP_DSN=1 only for a deliberately isolated app-schema bench cell."; exit 1; }
	psql "$(PG_DSN_BENCH)" -v ON_ERROR_STOP=1 -f scripts/load/db/seed-bench.sql
