SHELL := /bin/bash
MODULE := github.com/aero/aero-id
BIN    := bin/aero-id
PG_DSN ?= postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable

.PHONY: tidy build run test test-integration lint clean proto
tidy:        ; go mod tidy
build:       ; go build -o $(BIN) ./cmd/server
run:         ; go run ./cmd/server -config configs/config.yaml
test:        ; go test ./... -count=1
test-integration: ; go test -tags=integration ./... -count=1
lint:        ; go vet ./...
clean:       ; rm -rf bin/

proto:
	mkdir -p internal/sso/ssopb internal/handler/grpc/aeropb
	protoc \
	  --proto_path=api/proto \
	  --go_out=. --go_opt=module=github.com/aero/aero-id \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/aero/aero-id \
	  api/proto/sso/v1/sso.proto \
	  api/proto/aero_id/v1/aero_id.proto

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
