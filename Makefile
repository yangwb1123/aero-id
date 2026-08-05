SHELL := /bin/bash
MODULE := github.com/aero/aero-id
BIN    := bin/aero-id
PG_DSN ?= postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable

.PHONY: tidy build run test test-integration lint clean proto load-test-http load-test-grpc
tidy:        ; go mod tidy
build:       ; go build -o $(BIN) ./cmd/server
run:         ; go run ./cmd/server -config configs/config.yaml
test:        ; go test ./... -count=1
test-integration: ; go test -tags=integration -p 1 ./... -count=1
lint:        ; go vet ./...
clean:       ; rm -rf bin/

# ── Strict lint gate (HARNESS.md) ───────────────────────────────────────
# Enforces: file ≤500 lines, function ≤50 lines, complexity ≤10.
# Requires golangci-lint (install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
GOLANGCI_LINT ?= $(shell command -v golangci-lint 2>/dev/null || printf '%s/bin/golangci-lint' "$$(go env GOPATH)")

.PHONY: lint-strict check-file-sizes
lint-strict:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { echo "ERROR: golangci-lint not found. Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; exit 1; }
	$(GOLANGCI_LINT) run ./...

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
