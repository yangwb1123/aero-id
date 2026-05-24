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
	  api/proto/sso/v1/sso.proto

MIGRATIONS_DIR := migrations
MIGRATE_DSN    := postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable

.PHONY: migrate-up migrate-down migrate-create
migrate-up:
	migrate -path $(MIGRATIONS_DIR) -database "$(MIGRATE_DSN)" up
migrate-down:
	migrate -path $(MIGRATIONS_DIR) -database "$(MIGRATE_DSN)" down 1
migrate-create:
	@if [ -z "$(name)" ]; then echo "name=... required"; exit 1; fi
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)
