SHELL := /bin/bash
MODULE := github.com/aero/aero-id
BIN    := bin/aero-id
PG_DSN ?= postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable

.PHONY: tidy build run test test-integration lint clean
tidy:        ; go mod tidy
build:       ; go build -o $(BIN) ./cmd/server
run:         ; go run ./cmd/server -config configs/config.yaml
test:        ; go test ./... -count=1
test-integration: ; go test -tags=integration ./... -count=1
lint:        ; go vet ./...
clean:       ; rm -rf bin/
