SHELL := /bin/bash
MODULE := github.com/aero/aero-id
BIN    := bin/aero-id
PG_DSN ?= postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable

.PHONY: tidy build build-audit-replay run test test-integration lint clean proto load-test-http load-test-grpc load-test-event load-test-callback load-test-db check-integration-compile
tidy:        ; go mod tidy
build:       ; go build -o $(BIN) ./cmd/server
	@$(MAKE) --no-print-directory build-audit-replay
# Operator replay tool for dead-lettered audit publications/deliveries (B2-5);
# built alongside the server so bin/ ships both binaries (no repo-root binaries).
build-audit-replay: ; go build -o bin/audit-replay ./cmd/audit-replay
run:         ; go run ./cmd/server -config configs/config.yaml
test:        ; go test ./... -count=1
test-integration: ; go test -tags=integration -p 1 ./... -count=1
lint:        ; go vet ./...
clean:       ; rm -rf bin/

# ── Strict lint gate (HARNESS.md) ───────────────────────────────────────
# Enforces: file ≤500 lines, function ≤50 lines, complexity ≤10.
# Requires golangci-lint (install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
GOLANGCI_LINT ?= $(shell command -v golangci-lint 2>/dev/null || printf '%s/bin/golangci-lint' "$$(go env GOPATH)")

.PHONY: lint-strict check-file-sizes check-composition-root check-proto-sync
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

# Fail if any non-generated, non-test .go file exceeds 500 lines.
MAX_FILE_LINES ?= 500
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
	@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd

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

# Composition-root seam gate (Mutation-A pin): the buildAuditStack call and
# the drain Start must stay in internal/app/new.go with their exact shapes
# (C-1/C-2), the drain must not move into buildAuditStack (C-2b), the stack
# precedes NewFileRepository (C-3), and cmd/server/main.go hands the Loaded
# config to app.New unmutated (C-4). Docker-free companion to the boot e2e
# (internal/app/composition_root_integration_test.go).
.PHONY: check-composition-root
check-composition-root:
	@bash scripts/check-composition-root.sh

# Deterministic proto-stub sync gate: regenerates every committed *.pb.go from
# api/proto/ with a PINNED toolchain (protoc-gen-go derived from go.mod,
# protoc-gen-go-grpc v1.5.1, protoc 3.21.12) into a temp tree and diffs against
# the committed stubs. Self-testing (green+red fixtures); never mutates the repo.
.PHONY: check-proto-sync
check-proto-sync:
	@bash scripts/check-proto-sync.sh

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
.PHONY: migrate-up migrate-down migrate-version
migrate-up:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) up
migrate-down:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) down $(N)
migrate-version:
	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) version

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

# DB partition/index benchmark: EXPLAIN ANALYZE over the hot control-plane and
# worker queries. Requires psql and a seeded regional database.
PG_DSN_BENCH ?= $(PG_DSN)

load-test-db:
	@command -v psql >/dev/null 2>&1 || { echo "ERROR: psql not found (postgresql-client) — install it or set PG_DSN_BENCH"; exit 1; }
	psql "$(PG_DSN_BENCH)" -v ON_ERROR_STOP=1 -f scripts/load/db/partition-index-bench.sql
