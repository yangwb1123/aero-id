# aero-id Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build aero-id, a Go service that manages files/messages/comments for accounts authenticated via an external SSO over gRPC, exposing both HTTP REST and gRPC.

**Architecture:** Horizontally layered (handler → service → repository → DB). HTTP and gRPC handlers share one service layer. Repository uses GORM on PostgreSQL. FileStorage is an interface with `local` and `s3` implementations. SSO client wraps gRPC + token cache (60s) + batch user cache (300s).

**Tech Stack:** Go 1.26, Gin, gRPC, GORM, PostgreSQL 16, viper, zap, `google/uuid` v7, `golang-migrate`, `testcontainers-go`, `aws-sdk-go-v2` for S3.

---

## File Structure

```
aero-id/
├── go.mod / go.sum
├── Makefile
├── .gitignore
├── README.md
├── configs/config.yaml
├── cmd/server/main.go
├── internal/
│   ├── app/app.go
│   ├── config/{config.go,config_test.go}
│   ├── domain/{shared.go,file.go,message.go,comment.go,comment_test.go}
│   ├── repository/{db.go,file.go,message.go,comment.go,testpg.go,*_test.go}
│   ├── service/
│   │   ├── service.go
│   │   ├── {file,message,comment}.go + _test.go
│   │   └── mocks/{repo_mocks.go,sso_mocks.go,storage_mocks.go}
│   ├── handler/
│   │   ├── http/{router.go,health.go,file.go,message.go,comment.go,*_test.go}
│   │   └── grpc/{server.go,file.go,message.go,comment.go,server_test.go}
│   ├── middleware/{request_id.go,recovery.go,access_log.go,cors.go,auth.go,auth_test.go}
│   ├── sso/{client.go,token_cache.go,user_cache.go,*_test.go}
│   ├── storage/{storage.go,local.go,s3.go,*_test.go}
│   └── pkg/
│       ├── logger/logger.go
│       ├── db/db.go
│       ├── errors/{errors.go,errors_test.go}
│       ├── httpx/httpx.go
│       └── idgen/idgen.go
├── api/proto/
│   ├── sso/v1/sso.proto
│   └── aero_id/v1/{file.proto,message.proto,comment.proto,common.proto}
├── migrations/000{1,2,3}_*.{up,down}.sql
├── deployments/{Dockerfile,docker-compose.yml}
└── docs/superpowers/{specs,plans}/
```

## Task Order

| # | Task | Phase |
|---|---|---|
| 1 | Project skeleton (`go.mod`, Makefile, gitignore, placeholder main) | 0. Bootstrap |
| 2 | `pkg/logger` (zap wrapper) | 0 |
| 3 | `pkg/errors` (AppError + mappers) | 0 |
| 4 | `pkg/idgen` (UUID v7) | 0 |
| 5 | `config` (viper loader) | 0 |
| 6 | `pkg/db` + `repository/testpg` helper | 0 |
| 7 | Migrations + Makefile migrate targets | 1. DB |
| 8 | `domain/shared.go` (enums + Page) | 2. Domain |
| 9 | `domain/{file,message,comment}.go` + comment nesting tests | 2 |
| 10 | `repository/file.go` + PG integration test | 3. Repo |
| 11 | `repository/message.go` + PG integration test | 3 |
| 12 | `repository/comment.go` + PG integration test | 3 |
| 13 | `storage/storage.go` (interface + ObjectInfo) | 4. Storage |
| 14 | `storage/local.go` + tests (HMAC presign) | 4 |
| 15 | `storage/s3.go` + integration test (MinIO via testcontainers) | 4 |
| 16 | SSO proto + protoc generation | 5. SSO |
| 17 | `sso/token_cache.go` + tests | 5 |
| 18 | `sso/user_cache.go` + tests | 5 |
| 19 | `sso/client.go` + bufconn integration test | 5 |
| 20 | `service/service.go` (interfaces + ctx helpers) + mocks generation | 6. Service |
| 21 | `service/file.go` + tests | 6 |
| 22 | `service/message.go` + tests | 6 |
| 23 | `service/comment.go` + tests (two-level enforcement) | 6 |
| 24 | Middleware (request_id, recovery, access_log, cors, auth) + tests | 7. HTTP |
| 25 | HTTP health + router skeleton | 7 |
| 26 | HTTP file handler + tests | 7 |
| 27 | HTTP message handler + tests | 7 |
| 28 | HTTP comment handler + tests | 7 |
| 29 | aero_id proto + generation | 8. gRPC |
| 30 | gRPC server + auth interceptor | 8 |
| 31 | gRPC file/message/comment handlers + bufconn tests | 8 |
| 32 | `app/app.go` + `cmd/server/main.go` (wiring + graceful shutdown) | 9. Wire-up |
| 33 | Dockerfile + docker-compose | 9 |
| 34 | README + final `make test` + smoke run | 9 |

---

## Conventions Used Throughout

- **Commit message style:** Conventional Commits (`feat:`, `test:`, `refactor:`, `chore:`, `docs:`)
- **Branch:** all work on `main` (single-developer MVP)
- **Test framework:** `testing` stdlib + `testify/require` (add as needed in tasks where first used)
- **Module path:** `github.com/aero/aero-id`
- **All ctx variables passed first parameter** (Go convention)
- **All errors wrapped with `fmt.Errorf("...: %w", err)` at boundaries**
- **AppError convention:** Repository/Storage return raw errors; Service translates them into `*errors.AppError`; Handler renders AppError as response.

---

## Phase 0: Bootstrap

### Task 1: Project skeleton

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `Makefile`
- Create: `configs/config.yaml`
- Create: `cmd/server/main.go`

- [ ] **Step 1: Create `go.mod`**

```
module github.com/aero/aero-id

go 1.26
```

- [ ] **Step 2: Create `.gitignore`**

```
/bin/
/data/
/.env
*.test
*.out
*.log
vendor/
.idea/
.vscode/
```

- [ ] **Step 3: Create `Makefile` (initial targets)**

```makefile
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
```

- [ ] **Step 4: Create `configs/config.yaml` (placeholder)**

```yaml
app:    { name: aero-id, env: dev, version: 0.1.0 }
server:
  http: { host: 0.0.0.0, port: 8080, read_timeout: 15s, write_timeout: 15s, shutdown_timeout: 10s }
  grpc: { host: 0.0.0.0, port: 9090 }
database:
  host: 127.0.0.1
  port: 5432
  user: aero
  password: aero
  dbname: aero_id
  sslmode: disable
  max_open_conns: 50
  max_idle_conns: 10
  conn_max_lifetime: 1h
  log_level: warn
sso:
  endpoint: 127.0.0.1:9000
  timeout: 5s
  insecure: true
  token_cache_ttl: 60s
  user_cache_ttl: 300s
storage:
  type: local
  local:
    root: ./data/files
    public_base_url: http://127.0.0.1:8080
signing_key: change-me-please-very-long-random
log:    { level: info, encoding: console }
```

- [ ] **Step 5: Create placeholder `cmd/server/main.go`**

```go
package main

import "fmt"

func main() {
    fmt.Println("aero-id starting (skeleton)")
}
```

- [ ] **Step 6: Verify build**

Run: `go build ./cmd/server && ./aero-id-server-placeholder 2>/dev/null; ./bin/aero-id 2>/dev/null; go run ./cmd/server`
Expected output: `aero-id starting (skeleton)`

- [ ] **Step 7: Commit**

```bash
git add go.mod .gitignore Makefile configs/ cmd/
git commit -m "chore: scaffold aero-id module skeleton"
```

---

### Task 2: `pkg/logger` (zap wrapper)

**Files:**
- Create: `internal/pkg/logger/logger.go`
- Test: `internal/pkg/logger/logger_test.go`

- [ ] **Step 1: Add zap dependency**

Run: `go get go.uber.org/zap@v1.27.0`

- [ ] **Step 2: Write failing test**

Create `internal/pkg/logger/logger_test.go`:

```go
package logger

import (
	"testing"
)

func TestNew_invalidLevel(t *testing.T) {
	_, err := New("nonsense", "console")
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestNew_validLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		t.Run(lvl, func(t *testing.T) {
			l, err := New(lvl, "json")
			if err != nil {
				t.Fatalf("New(%q): %v", lvl, err)
			}
			if l == nil {
				t.Fatal("nil logger")
			}
		})
	}
}
```

- [ ] **Step 3: Verify test fails**

Run: `go test ./internal/pkg/logger/...`
Expected: FAIL (package `logger` undefined: `New`)

- [ ] **Step 4: Implement `logger.go`**

Create `internal/pkg/logger/logger.go`:

```go
package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New builds a zap logger from a level string ("debug"|"info"|"warn"|"error")
// and an encoding ("console"|"json").
func New(level, encoding string) (*zap.Logger, error) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("parse log level %q: %w", level, err)
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	cfg.Encoding = encoding
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.EncodeDuration = zapcore.StringDurationEncoder
	if encoding == "console" {
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}
	return cfg.Build()
}
```

- [ ] **Step 5: Verify test passes**

Run: `go test ./internal/pkg/logger/... -v`
Expected: PASS (3 subtests)

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/pkg/logger/
git commit -m "feat(logger): add zap-based logger wrapper"
```

---

### Task 3: `pkg/errors` (AppError type)

**Files:**
- Create: `internal/pkg/errors/errors.go`
- Test: `internal/pkg/errors/errors_test.go`

- [ ] **Step 1: Add gRPC codes dependency**

Run: `go get google.golang.org/grpc@v1.66.0`

- [ ] **Step 2: Write failing test**

Create `internal/pkg/errors/errors_test.go`:

```go
package errors

import (
	stderrors "errors"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
)

func TestNotFound(t *testing.T) {
	e := NotFound("file.not_found", "file not found")
	if e.HTTP != http.StatusNotFound {
		t.Errorf("HTTP = %d, want 404", e.HTTP)
	}
	if e.GRPC != codes.NotFound {
		t.Errorf("GRPC = %v, want NotFound", e.GRPC)
	}
	if e.Code != "file.not_found" {
		t.Errorf("Code = %q", e.Code)
	}
}

func TestUnauthorized(t *testing.T) {
	e := Unauthorized("auth.unauthorized", "token invalid")
	if e.HTTP != http.StatusUnauthorized || e.GRPC != codes.Unauthenticated {
		t.Errorf("got HTTP=%d GRPC=%v", e.HTTP, e.GRPC)
	}
}

func TestWrap_preservesCause(t *testing.T) {
	root := stderrors.New("root cause")
	e := Internal("internal.unexpected", "boom").Wrap(root)
	if !stderrors.Is(e, root) {
		t.Fatal("errors.Is should unwrap to root cause")
	}
}

func TestAs(t *testing.T) {
	var ae *AppError
	if !stderrors.As(NotFound("x.y", "z"), &ae) {
		t.Fatal("errors.As should match *AppError")
	}
}
```

- [ ] **Step 3: Verify test fails**

Run: `go test ./internal/pkg/errors/...`
Expected: FAIL (undefined: NotFound, Unauthorized, etc.)

- [ ] **Step 4: Implement `errors.go`**

Create `internal/pkg/errors/errors.go`:

```go
package errors

import (
	"net/http"

	"google.golang.org/grpc/codes"
)

// AppError is the unified error shape for service/handler boundaries.
// Repositories return raw errors; service code converts them via constructors here.
type AppError struct {
	Code    string
	Message string
	HTTP    int
	GRPC    codes.Code
	Cause   error
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Message + " (cause: " + e.Cause.Error() + ")"
	}
	return e.Code + ": " + e.Message
}

func (e *AppError) Unwrap() error { return e.Cause }

func (e *AppError) Wrap(cause error) *AppError {
	cp := *e
	cp.Cause = cause
	return &cp
}

func BadRequest(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusBadRequest, GRPC: codes.InvalidArgument}
}
func Unauthorized(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusUnauthorized, GRPC: codes.Unauthenticated}
}
func Forbidden(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusForbidden, GRPC: codes.PermissionDenied}
}
func NotFound(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusNotFound, GRPC: codes.NotFound}
}
func Conflict(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusConflict, GRPC: codes.AlreadyExists}
}
func Internal(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusInternalServerError, GRPC: codes.Internal}
}
```

- [ ] **Step 5: Verify test passes**

Run: `go test ./internal/pkg/errors/... -v`
Expected: PASS (4 tests)

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/pkg/errors/
git commit -m "feat(errors): add AppError with HTTP/gRPC mapping"
```

---

### Task 4: `pkg/idgen` (UUID v7)

**Files:**
- Create: `internal/pkg/idgen/idgen.go`
- Test: `internal/pkg/idgen/idgen_test.go`

- [ ] **Step 1: Add uuid dependency**

Run: `go get github.com/google/uuid@v1.6.0`

- [ ] **Step 2: Write failing test**

Create `internal/pkg/idgen/idgen_test.go`:

```go
package idgen

import (
	"testing"
	"time"
)

func TestNewID_uniqueAndTimeOrdered(t *testing.T) {
	prev := NewID()
	for i := 0; i < 100; i++ {
		time.Sleep(time.Millisecond)
		curr := NewID()
		if curr == prev {
			t.Fatal("two consecutive IDs collided")
		}
		if curr.String() < prev.String() {
			t.Fatalf("expected time-ordered, got %s < %s", curr, prev)
		}
		prev = curr
	}
}

func TestParse_roundtrip(t *testing.T) {
	id := NewID()
	parsed, err := Parse(id.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != id {
		t.Fatalf("parsed=%s want=%s", parsed, id)
	}
}
```

- [ ] **Step 3: Verify test fails**

Run: `go test ./internal/pkg/idgen/...`
Expected: FAIL (undefined: NewID, Parse)

- [ ] **Step 4: Implement `idgen.go`**

Create `internal/pkg/idgen/idgen.go`:

```go
package idgen

import "github.com/google/uuid"

type ID = uuid.UUID

func NewID() ID {
	id, err := uuid.NewV7()
	if err != nil {
		// V7 only fails if the system clock is broken; panicking here is acceptable.
		panic("uuid v7: " + err.Error())
	}
	return id
}

func Parse(s string) (ID, error) {
	return uuid.Parse(s)
}
```

- [ ] **Step 5: Verify test passes**

Run: `go test ./internal/pkg/idgen/... -v`
Expected: PASS (2 tests)

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/pkg/idgen/
git commit -m "feat(idgen): add UUID v7 generator"
```

---

### Task 5: `config` (viper loader)

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Add viper dependency**

Run: `go get github.com/spf13/viper@v1.19.0`

- [ ] **Step 2: Write failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `
app: {name: aero-id, env: test, version: 0.0.1}
server:
  http: {host: 0.0.0.0, port: 8080, read_timeout: 15s, write_timeout: 15s, shutdown_timeout: 10s}
  grpc: {host: 0.0.0.0, port: 9090}
database:
  host: 127.0.0.1
  port: 5432
  user: aero
  password: aero
  dbname: aero_id
  sslmode: disable
  max_open_conns: 10
  max_idle_conns: 2
  conn_max_lifetime: 30m
  log_level: warn
sso: {endpoint: "sso:9000", timeout: 5s, insecure: true, token_cache_ttl: 60s, user_cache_ttl: 300s}
storage:
  type: local
  local: {root: /tmp/files, public_base_url: "http://x"}
signing_key: testkey
log: {level: info, encoding: json}
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_fromFile(t *testing.T) {
	cfg, err := Load(writeTemp(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.DBName != "aero_id" {
		t.Errorf("DBName=%q", cfg.Database.DBName)
	}
	if cfg.SSO.TokenCacheTTL.String() != "1m0s" {
		t.Errorf("TokenCacheTTL=%v", cfg.SSO.TokenCacheTTL)
	}
}

func TestLoad_envOverride(t *testing.T) {
	t.Setenv("AERO_ID_DATABASE_PASSWORD", "envpass")
	t.Setenv("AERO_ID_SIGNING_KEY", "envkey")
	cfg, err := Load(writeTemp(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Password != "envpass" {
		t.Errorf("Password=%q", cfg.Database.Password)
	}
	if cfg.SigningKey != "envkey" {
		t.Errorf("SigningKey=%q", cfg.SigningKey)
	}
}

func TestLoad_missingSigningKey(t *testing.T) {
	bad := sample[:len(sample)-len("\nsigning_key: testkey\nlog: {level: info, encoding: json}\n")]
	bad += "\nlog: {level: info, encoding: json}\n"
	_, err := Load(writeTemp(t, bad))
	if err == nil {
		t.Fatal("expected validation error for missing signing_key")
	}
}

func TestDatabaseDSN(t *testing.T) {
	cfg, _ := Load(writeTemp(t, sample))
	dsn := cfg.Database.DSN()
	want := "host=127.0.0.1 port=5432 user=aero password=aero dbname=aero_id sslmode=disable TimeZone=UTC"
	if dsn != want {
		t.Errorf("\n got %s\nwant %s", dsn, want)
	}
}
```

- [ ] **Step 3: Verify test fails**

Run: `go test ./internal/config/...`
Expected: FAIL (undefined: Load, Config)

- [ ] **Step 4: Implement `config.go`**

Create `internal/config/config.go`:

```go
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	App        AppConfig      `mapstructure:"app"`
	Server     ServerConfig   `mapstructure:"server"`
	Database   DatabaseConfig `mapstructure:"database"`
	SSO        SSOConfig      `mapstructure:"sso"`
	Storage    StorageConfig  `mapstructure:"storage"`
	SigningKey string         `mapstructure:"signing_key"`
	Log        LogConfig      `mapstructure:"log"`
}

type AppConfig struct {
	Name    string `mapstructure:"name"`
	Env     string `mapstructure:"env"`
	Version string `mapstructure:"version"`
}

type ServerConfig struct {
	HTTP HTTPConfig `mapstructure:"http"`
	GRPC GRPCConfig `mapstructure:"grpc"`
}

type HTTPConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
}

type GRPCConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

type DatabaseConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	DBName          string        `mapstructure:"dbname"`
	SSLMode         string        `mapstructure:"sslmode"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	LogLevel        string        `mapstructure:"log_level"`
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=UTC",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode,
	)
}

type SSOConfig struct {
	Endpoint      string        `mapstructure:"endpoint"`
	Timeout       time.Duration `mapstructure:"timeout"`
	Insecure      bool          `mapstructure:"insecure"`
	TokenCacheTTL time.Duration `mapstructure:"token_cache_ttl"`
	UserCacheTTL  time.Duration `mapstructure:"user_cache_ttl"`
}

type StorageConfig struct {
	Type  string             `mapstructure:"type"`
	Local LocalStorageConfig `mapstructure:"local"`
	S3    S3StorageConfig    `mapstructure:"s3"`
}

type LocalStorageConfig struct {
	Root          string `mapstructure:"root"`
	PublicBaseURL string `mapstructure:"public_base_url"`
}

type S3StorageConfig struct {
	Region     string `mapstructure:"region"`
	Bucket     string `mapstructure:"bucket"`
	Endpoint   string `mapstructure:"endpoint"`
	AccessKey  string `mapstructure:"access_key"`
	SecretKey  string `mapstructure:"secret_key"`
	UsePathStyle bool `mapstructure:"use_path_style"`
}

type LogConfig struct {
	Level    string `mapstructure:"level"`
	Encoding string `mapstructure:"encoding"`
}

func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("AERO_ID")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.SigningKey == "" {
		return fmt.Errorf("signing_key is required")
	}
	if c.Database.DBName == "" {
		return fmt.Errorf("database.dbname is required")
	}
	if c.SSO.Endpoint == "" {
		return fmt.Errorf("sso.endpoint is required")
	}
	switch c.Storage.Type {
	case "local", "s3":
	default:
		return fmt.Errorf("storage.type must be 'local' or 's3'")
	}
	return nil
}
```

- [ ] **Step 5: Verify test passes**

Run: `go test ./internal/config/... -v`
Expected: PASS (4 tests)

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config/
git commit -m "feat(config): add viper-based config loader with env overrides"
```

---

### Task 6: `pkg/db` + test PG helper

**Files:**
- Create: `internal/pkg/db/db.go`
- Create: `internal/repository/testpg/testpg.go` (test helper, build-tagged)
- Test: `internal/pkg/db/db_test.go` (build tag `integration`)

- [ ] **Step 1: Add gorm + testcontainers dependencies**

Run:
```
go get gorm.io/gorm@v1.25.11
go get gorm.io/driver/postgres@v1.5.9
go get github.com/testcontainers/testcontainers-go@v0.32.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.32.0
```

- [ ] **Step 2: Implement `db.go`**

Create `internal/pkg/db/db.go`:

```go
package db

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Options struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	LogLevel        string
}

func Open(opts Options) (*gorm.DB, error) {
	lvl := logger.Warn
	switch opts.LogLevel {
	case "silent":
		lvl = logger.Silent
	case "error":
		lvl = logger.Error
	case "info":
		lvl = logger.Info
	}
	gormCfg := &gorm.Config{Logger: logger.Default.LogMode(lvl)}
	db, err := gorm.Open(postgres.Open(opts.DSN), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("gorm open: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	if opts.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(opts.MaxIdleConns)
	}
	if opts.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}
	return db, nil
}

func Ping(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}
```

- [ ] **Step 3: Implement `testpg` helper**

Create `internal/repository/testpg/testpg.go`:

```go
//go:build integration

package testpg

import (
	"context"
	"testing"

	pg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/gorm"

	dbpkg "github.com/aero/aero-id/internal/pkg/db"
)

// Start launches a throwaway PostgreSQL container and returns a connected *gorm.DB.
// Cleanup is registered with t.Cleanup.
func Start(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	c, err := pg.Run(ctx,
		"postgres:16-alpine",
		pg.WithDatabase("aero_id_test"),
		pg.WithUsername("aero"),
		pg.WithPassword("aero"),
		// BasicWaitStrategies waits for "ready to accept connections" + port. The bare
		// testcontainers.WithWaitStrategyAndDeadline(duration) form errors at runtime
		// ("no wait strategy supplied") because it passes a deadline but no strategy.
		pg.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start pg container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	db, err := dbpkg.Open(dbpkg.Options{
		DSN:          dsn,
		MaxOpenConns: 5,
		LogLevel:     "silent",
	})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return db
}
```

- [ ] **Step 4: Write integration test**

Create `internal/pkg/db/db_test.go`:

```go
//go:build integration

package db_test

import (
	"testing"

	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestPing(t *testing.T) {
	g := testpg.Start(t)
	if err := db.Ping(g); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
```

- [ ] **Step 5: Verify integration test passes**

Run: `make test-integration`
Expected: PASS (takes ~10-20s, pulls postgres:16-alpine image first time)

- [ ] **Step 6: Verify unit tests still pass**

Run: `make test`
Expected: PASS (integration build tag skips testpg)

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/pkg/db/ internal/repository/testpg/
git commit -m "feat(db): add gorm connection helper and testcontainers PG helper"
```

---

## Phase 1: Database Migrations

### Task 7: Migrations + Makefile targets

**Files:**
- Create: `migrations/000001_create_files.up.sql` / `.down.sql`
- Create: `migrations/000002_create_messages.up.sql` / `.down.sql`
- Create: `migrations/000003_create_comments.up.sql` / `.down.sql`
- Modify: `Makefile`

- [ ] **Step 1: Create files migration UP**

Create `migrations/000001_create_files.up.sql`:

```sql
CREATE TABLE files (
    id            UUID PRIMARY KEY,
    owner_uid     TEXT NOT NULL,
    name          TEXT NOT NULL,
    size          BIGINT NOT NULL,
    mime_type     TEXT NOT NULL,
    storage_key   TEXT NOT NULL,
    storage_type  TEXT NOT NULL,
    checksum      TEXT,
    visibility    SMALLINT NOT NULL DEFAULT 0,
    meta          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_files_owner    ON files(owner_uid, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_files_checksum ON files(checksum);
```

- [ ] **Step 2: Create files migration DOWN**

Create `migrations/000001_create_files.down.sql`:

```sql
DROP TABLE IF EXISTS files;
```

- [ ] **Step 3: Create messages migration UP**

Create `migrations/000002_create_messages.up.sql`:

```sql
CREATE TABLE messages (
    id          UUID PRIMARY KEY,
    from_uid    TEXT NOT NULL,
    to_uid      TEXT NOT NULL,
    kind        SMALLINT NOT NULL DEFAULT 1,
    content     TEXT NOT NULL,
    file_id     UUID REFERENCES files(id),
    read_at     TIMESTAMPTZ,
    meta        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL,
    deleted_at  TIMESTAMPTZ
);
CREATE INDEX idx_messages_inbox  ON messages(to_uid,   created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_messages_outbox ON messages(from_uid, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_messages_unread ON messages(to_uid)                    WHERE read_at IS NULL AND deleted_at IS NULL;
```

- [ ] **Step 4: Create messages migration DOWN**

Create `migrations/000002_create_messages.down.sql`:

```sql
DROP TABLE IF EXISTS messages;
```

- [ ] **Step 5: Create comments migration UP**

Create `migrations/000003_create_comments.up.sql`:

```sql
CREATE TABLE comments (
    id           UUID PRIMARY KEY,
    owner_uid    TEXT NOT NULL,
    target_type  TEXT NOT NULL,
    target_id    UUID NOT NULL,
    parent_id    UUID REFERENCES comments(id),
    root_id      UUID,
    content      TEXT NOT NULL,
    like_count   INT NOT NULL DEFAULT 0,
    reply_count  INT NOT NULL DEFAULT 0,
    status       SMALLINT NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    deleted_at   TIMESTAMPTZ
);
CREATE INDEX idx_comments_target ON comments(target_type, target_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_owner  ON comments(owner_uid, created_at DESC)              WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_root   ON comments(root_id, created_at)                     WHERE deleted_at IS NULL;
```

- [ ] **Step 6: Create comments migration DOWN**

Create `migrations/000003_create_comments.down.sql`:

```sql
DROP TABLE IF EXISTS comments;
```

- [ ] **Step 7: Add migrate targets to Makefile**

Replace `Makefile`'s `.PHONY` line and append targets — add to end of Makefile:

```makefile
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
```

- [ ] **Step 8: Write a smoke integration test for migrations**

Create `migrations/migrations_test.go`:

```go
//go:build integration

package migrations_test

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestMigrationsUpAndDown(t *testing.T) {
	g := testpg.Start(t)
	sqlDB, err := g.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)

	dsn := getDSN(t, sqlDB)
	m, err := migrate.New("file://"+dir, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("up: %v", err)
	}
	for _, table := range []string{"files", "messages", "comments"} {
		var ok bool
		if err := sqlDB.QueryRow(
			`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_name=$1)`,
			table,
		).Scan(&ok); err != nil || !ok {
			t.Fatalf("table %q missing", table)
		}
	}
	if err := m.Down(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("down: %v", err)
	}
}

func getDSN(t *testing.T, db *sql.DB) string {
	t.Helper()
	// testcontainers DSN is reachable from inside the gorm pool; reuse its underlying
	// connection settings via the connection string baked into config.
	t.Skip("intentionally skipped: testpg DSN is private; replace this stub if you " +
		"refactor testpg to expose DSN. Migrations are also exercised in repository tests.")
	return ""
}
```

> **Note for the engineer:** the migrations are also exercised end-to-end by the repository tests in Tasks 10–12 via `gorm.AutoMigrate`-equivalent or direct SQL execution; this `migrations_test.go` is a placeholder you can flesh out if you decide to refactor `testpg.Start` to also return the DSN. For MVP it is OK to delete it before commit.

- [ ] **Step 9: Add migrate CLI dep (for the eventual migrate test, runtime use is via separate `migrate` binary)**

Run: `go get github.com/golang-migrate/migrate/v4@v4.17.1`

- [ ] **Step 10: Verify migration SQL is syntactically valid**

Apply manually against the test container to confirm:

```bash
# Start a throwaway pg container in one terminal:
docker run --rm -e POSTGRES_PASSWORD=aero -e POSTGRES_USER=aero -e POSTGRES_DB=aero_id -p 5432:5432 postgres:16-alpine
# In another terminal:
migrate -path migrations -database "postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable" up
psql "postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable" -c "\dt"
# Expected: files, messages, comments, schema_migrations
migrate -path migrations -database "postgres://aero:aero@127.0.0.1:5432/aero_id?sslmode=disable" down -all
```

- [ ] **Step 11: Commit**

```bash
git add migrations/ Makefile go.mod go.sum
git commit -m "feat(migrations): add files/messages/comments DDL and migrate Makefile targets"
```

---

## Phase 2: Domain Models

### Task 8: `domain/shared.go` (enums and shared types)

**Files:**
- Create: `internal/domain/shared.go`

- [ ] **Step 1: Implement shared types**

Create `internal/domain/shared.go`:

```go
package domain

import "github.com/aero/aero-id/internal/pkg/idgen"

// ID is the canonical identifier type for all aero-id entities.
type ID = idgen.ID

// Visibility for files.
type Visibility int16

const (
	VisibilityPrivate Visibility = 0
	VisibilityPublic  Visibility = 1
)

// MessageKind enumerates message payload types.
type MessageKind int16

const (
	MessageKindText   MessageKind = 1
	MessageKindImage  MessageKind = 2
	MessageKindFile   MessageKind = 3
	MessageKindSystem MessageKind = 4
)

// CommentStatus tracks moderation state.
type CommentStatus int16

const (
	CommentStatusNormal  CommentStatus = 1
	CommentStatusDeleted CommentStatus = 2
	CommentStatusHidden  CommentStatus = 3
)

// TargetType is the polymorphic discriminator for what a comment is attached to.
type TargetType string

const (
	TargetTypeFile    TargetType = "file"
	TargetTypeMessage TargetType = "message"
)

// Page is the standard pagination request.
type Page struct {
	Limit  int    // default 20, max 100
	Cursor string // opaque, repository-defined
}

const (
	DefaultPageLimit = 20
	MaxPageLimit     = 100
)

// Normalize clamps Limit to [1, MaxPageLimit] and applies the default if zero.
func (p *Page) Normalize() {
	if p.Limit <= 0 {
		p.Limit = DefaultPageLimit
	}
	if p.Limit > MaxPageLimit {
		p.Limit = MaxPageLimit
	}
}

// SystemSenderUID is the synthetic from_uid for system notifications.
const SystemSenderUID = "system"
```

- [ ] **Step 2: Smoke-build the package**

Run: `go build ./internal/domain/`
Expected: success (no test yet — types only).

- [ ] **Step 3: Commit**

```bash
git add internal/domain/shared.go
git commit -m "feat(domain): add shared enums and pagination type"
```

---

### Task 9: Domain entities + comment nesting rule

**Files:**
- Create: `internal/domain/file.go`
- Create: `internal/domain/message.go`
- Create: `internal/domain/comment.go`
- Test: `internal/domain/comment_test.go`

- [ ] **Step 1: Write `file.go`**

Create `internal/domain/file.go`:

```go
package domain

import "time"

type File struct {
	ID          ID
	OwnerUID    string
	Name        string
	Size        int64
	MimeType    string
	StorageKey  string
	StorageType string
	Checksum    string
	Visibility  Visibility
	Meta        map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
```

- [ ] **Step 2: Write `message.go`**

Create `internal/domain/message.go`:

```go
package domain

import "time"

type Message struct {
	ID        ID
	FromUID   string
	ToUID     string
	Kind      MessageKind
	Content   string
	FileID    *ID
	ReadAt    *time.Time
	Meta      map[string]any
	CreatedAt time.Time
}
```

- [ ] **Step 3: Write failing comment nesting test**

Create `internal/domain/comment_test.go`:

```go
package domain

import (
	"testing"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestValidateNewComment_topLevel(t *testing.T) {
	err := ValidateNewComment(nil)
	if err != nil {
		t.Fatalf("top-level comment should be allowed: %v", err)
	}
}

func TestValidateNewComment_replyToTopLevel(t *testing.T) {
	root := idgen.NewID()
	parent := &Comment{ID: root, ParentID: nil, RootID: root}
	err := ValidateNewComment(parent)
	if err != nil {
		t.Fatalf("reply to top-level should be allowed: %v", err)
	}
}

func TestValidateNewComment_replyToReplyForbidden(t *testing.T) {
	root := idgen.NewID()
	parent := &Comment{ID: idgen.NewID(), ParentID: &root, RootID: root}
	err := ValidateNewComment(parent)
	if err == nil {
		t.Fatal("expected error for depth >= 3")
	}
}

func TestNewTopLevelComment_setsRootToSelf(t *testing.T) {
	c := NewComment(idgen.NewID(), "uid-x", TargetTypeFile, idgen.NewID(), nil, "hi")
	if c.ParentID != nil {
		t.Fatal("ParentID should be nil")
	}
	if c.RootID != c.ID {
		t.Fatal("RootID should equal ID for top-level")
	}
}

func TestNewReply_inheritsRootID(t *testing.T) {
	root := idgen.NewID()
	parent := &Comment{ID: root, ParentID: nil, RootID: root}
	c := NewComment(idgen.NewID(), "uid-y", TargetTypeFile, idgen.NewID(), parent, "re: hi")
	if c.RootID != root {
		t.Fatalf("RootID = %s, want %s", c.RootID, root)
	}
	if c.ParentID == nil || *c.ParentID != parent.ID {
		t.Fatal("ParentID should equal parent.ID")
	}
}
```

- [ ] **Step 4: Verify test fails**

Run: `go test ./internal/domain/...`
Expected: FAIL (undefined: Comment, ValidateNewComment, NewComment)

- [ ] **Step 5: Implement `comment.go`**

Create `internal/domain/comment.go`:

```go
package domain

import (
	"errors"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

type Comment struct {
	ID         ID
	OwnerUID   string
	TargetType TargetType
	TargetID   ID
	ParentID   *ID
	RootID     ID
	Content    string
	LikeCount  int
	ReplyCount int
	Status     CommentStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ErrNestingTooDeep is returned by ValidateNewComment when a reply would create
// a depth >= 3 chain (root → reply → reply-of-reply).
var ErrNestingTooDeep = errors.New("comment nesting must not exceed two levels")

// ValidateNewComment enforces the two-level nesting rule.
//   - nil parent (top-level): always allowed.
//   - parent whose own ParentID is nil (top-level): allowed (becomes 2nd level).
//   - parent whose ParentID is non-nil (already a reply): rejected.
func ValidateNewComment(parent *Comment) error {
	if parent == nil {
		return nil
	}
	if parent.ParentID != nil {
		return ErrNestingTooDeep
	}
	return nil
}

// NewComment is a factory that wires ID/RootID/ParentID per the nesting rule.
// Caller must have already validated the parent with ValidateNewComment.
func NewComment(id ID, ownerUID string, targetType TargetType, targetID ID, parent *Comment, content string) *Comment {
	c := &Comment{
		ID:         id,
		OwnerUID:   ownerUID,
		TargetType: targetType,
		TargetID:   targetID,
		Content:    content,
		Status:     CommentStatusNormal,
	}
	if parent == nil {
		c.RootID = id
		return c
	}
	pid := parent.ID
	c.ParentID = &pid
	c.RootID = parent.RootID
	return c
}

// Make sure idgen is referenced so go vet does not strip it when only the type alias is used in tests.
var _ = idgen.NewID
```

- [ ] **Step 6: Verify test passes**

Run: `go test ./internal/domain/... -v`
Expected: PASS (5 tests)

- [ ] **Step 7: Commit**

```bash
git add internal/domain/
git commit -m "feat(domain): add File/Message/Comment entities with two-level nesting rule"
```

---

## Phase 3: Repository Layer

### Task 10: `repository/file.go` + PG integration test

**Files:**
- Create: `internal/repository/db.go` (shared GORM models + helpers)
- Create: `internal/repository/file.go`
- Modify: `internal/repository/testpg/testpg.go` (add `Migrate` helper)
- Test: `internal/repository/file_test.go` (build tag `integration`)

- [ ] **Step 1: Add gorm datatypes dependency**

Run: `go get gorm.io/datatypes@v1.2.1`

- [ ] **Step 2: Extend testpg with migration helper**

Modify `internal/repository/testpg/testpg.go` — add at the bottom (still under `//go:build integration`):

```go
import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Migrate executes every *.up.sql file under migrationsDir in lexical order.
// Path is typically returned by RepoRoot helpers; we just take it raw.
func Migrate(t *testing.T, db *gorm.DB, migrationsDir string) {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := db.Exec(string(b)).Error; err != nil {
			t.Fatalf("exec %s: %v", f, err)
		}
	}
}

// RepoRoot finds the module root by walking up from the calling test file
// looking for a go.mod.
func RepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := wd; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("go.mod not found")
	return ""
}
```

> The new `import` block must be merged into the existing import block at the top of the file.

- [ ] **Step 3: Implement `repository/db.go`**

Create `internal/repository/db.go`:

```go
package repository

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// fileRow is the GORM-mapped representation of the files table.
type fileRow struct {
	ID          uuid.UUID         `gorm:"column:id;primaryKey"`
	OwnerUID    string            `gorm:"column:owner_uid"`
	Name        string            `gorm:"column:name"`
	Size        int64             `gorm:"column:size"`
	MimeType    string            `gorm:"column:mime_type"`
	StorageKey  string            `gorm:"column:storage_key"`
	StorageType string            `gorm:"column:storage_type"`
	Checksum    *string           `gorm:"column:checksum"`
	Visibility  int16             `gorm:"column:visibility"`
	Meta        datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt   time.Time         `gorm:"column:created_at"`
	UpdatedAt   time.Time         `gorm:"column:updated_at"`
	DeletedAt   gorm.DeletedAt    `gorm:"column:deleted_at;index"`
}

func (fileRow) TableName() string { return "files" }

func fileRowToDomain(r *fileRow) *domain.File {
	f := &domain.File{
		ID:          r.ID,
		OwnerUID:    r.OwnerUID,
		Name:        r.Name,
		Size:        r.Size,
		MimeType:    r.MimeType,
		StorageKey:  r.StorageKey,
		StorageType: r.StorageType,
		Visibility:  domain.Visibility(r.Visibility),
		Meta:        map[string]any(r.Meta),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.Checksum != nil {
		f.Checksum = *r.Checksum
	}
	return f
}

func fileDomainToRow(f *domain.File) *fileRow {
	r := &fileRow{
		ID:          f.ID,
		OwnerUID:    f.OwnerUID,
		Name:        f.Name,
		Size:        f.Size,
		MimeType:    f.MimeType,
		StorageKey:  f.StorageKey,
		StorageType: f.StorageType,
		Visibility:  int16(f.Visibility),
		Meta:        datatypes.JSONMap(f.Meta),
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
	}
	if f.Checksum != "" {
		c := f.Checksum
		r.Checksum = &c
	}
	return r
}
```

- [ ] **Step 4: Implement `repository/file.go`**

Create `internal/repository/file.go`:

```go
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// ErrNotFound is returned when no row matches.
var ErrNotFound = errors.New("repository: not found")

type FileRepository interface {
	Create(ctx context.Context, f *domain.File) error
	Get(ctx context.Context, id domain.ID) (*domain.File, error)
	ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error)
	Delete(ctx context.Context, id domain.ID) error
}

type fileRepo struct {
	db *gorm.DB
}

func NewFileRepository(db *gorm.DB) FileRepository {
	return &fileRepo{db: db}
}

func (r *fileRepo) Create(ctx context.Context, f *domain.File) error {
	row := fileDomainToRow(f)
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	return nil
}

func (r *fileRepo) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	var row fileRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	return fileRowToDomain(&row), nil
}

func (r *fileRepo) ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("owner_uid = ?", ownerUID).
		Order("created_at DESC, id DESC").
		Limit(page.Limit + 1) // fetch one extra to detect "has more"
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor) // simple id-based cursor; works because UUID v7 is time-ordered
	}
	var rows []fileRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list files: %w", err)
	}
	nextCursor := ""
	if len(rows) > page.Limit {
		nextCursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.File, len(rows))
	for i := range rows {
		out[i] = fileRowToDomain(&rows[i])
	}
	return out, nextCursor, nil
}

func (r *fileRepo) Delete(ctx context.Context, id domain.ID) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&fileRow{})
	if res.Error != nil {
		return fmt.Errorf("delete file: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 5: Write failing integration test**

Create `internal/repository/file_test.go`:

```go
//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupFileRepo(t *testing.T) repository.FileRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewFileRepository(db)
}

func TestFileRepo_CreateGetListDelete(t *testing.T) {
	ctx := context.Background()
	repo := setupFileRepo(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	f := &domain.File{
		ID: idgen.NewID(), OwnerUID: "u1", Name: "x.txt", Size: 7,
		MimeType: "text/plain", StorageKey: "u1/x", StorageType: "local",
		Checksum: "abc", Visibility: domain.VisibilityPrivate,
		Meta: map[string]any{"k": "v"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "x.txt" || got.Size != 7 || got.Checksum != "abc" {
		t.Fatalf("got %+v", got)
	}
	if got.Meta["k"] != "v" {
		t.Errorf("meta lost: %+v", got.Meta)
	}

	list, _, err := repo.ListByOwner(ctx, "u1", domain.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1, got %d", len(list))
	}

	if err := repo.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, f.ID); err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFileRepo_ListPagination(t *testing.T) {
	ctx := context.Background()
	repo := setupFileRepo(t)

	for i := 0; i < 3; i++ {
		now := time.Now().UTC()
		_ = repo.Create(ctx, &domain.File{
			ID: idgen.NewID(), OwnerUID: "u2", Name: "f", Size: 1,
			MimeType: "x", StorageKey: "k", StorageType: "local",
			CreatedAt: now, UpdatedAt: now,
		})
		time.Sleep(2 * time.Millisecond)
	}
	page1, cursor1, err := repo.ListByOwner(ctx, "u2", domain.Page{Limit: 2})
	if err != nil || len(page1) != 2 || cursor1 == "" {
		t.Fatalf("page1=%d cursor=%q err=%v", len(page1), cursor1, err)
	}
	page2, cursor2, err := repo.ListByOwner(ctx, "u2", domain.Page{Limit: 2, Cursor: cursor1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("page2=%d cursor=%q", len(page2), cursor2)
	}
}
```

- [ ] **Step 6: Run integration tests**

Run: `make test-integration`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/repository/ 
git commit -m "feat(repository): add file repository with GORM and PG integration tests"
```

---

### Task 11: `repository/message.go` + integration test

**Files:**
- Modify: `internal/repository/db.go` (add messageRow)
- Create: `internal/repository/message.go`
- Test: `internal/repository/message_test.go` (build tag `integration`)

- [ ] **Step 1: Append `messageRow` to `db.go`**

Append to `internal/repository/db.go`:

```go
type messageRow struct {
	ID        uuid.UUID         `gorm:"column:id;primaryKey"`
	FromUID   string            `gorm:"column:from_uid"`
	ToUID     string            `gorm:"column:to_uid"`
	Kind      int16             `gorm:"column:kind"`
	Content   string            `gorm:"column:content"`
	FileID    *uuid.UUID        `gorm:"column:file_id"`
	ReadAt    *time.Time        `gorm:"column:read_at"`
	Meta      datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt time.Time         `gorm:"column:created_at"`
	DeletedAt gorm.DeletedAt    `gorm:"column:deleted_at;index"`
}

func (messageRow) TableName() string { return "messages" }

func msgRowToDomain(r *messageRow) *domain.Message {
	m := &domain.Message{
		ID:        r.ID,
		FromUID:   r.FromUID,
		ToUID:     r.ToUID,
		Kind:      domain.MessageKind(r.Kind),
		Content:   r.Content,
		FileID:    r.FileID,
		ReadAt:    r.ReadAt,
		Meta:      map[string]any(r.Meta),
		CreatedAt: r.CreatedAt,
	}
	return m
}

func msgDomainToRow(m *domain.Message) *messageRow {
	return &messageRow{
		ID:        m.ID,
		FromUID:   m.FromUID,
		ToUID:     m.ToUID,
		Kind:      int16(m.Kind),
		Content:   m.Content,
		FileID:    m.FileID,
		ReadAt:    m.ReadAt,
		Meta:      datatypes.JSONMap(m.Meta),
		CreatedAt: m.CreatedAt,
	}
}
```

- [ ] **Step 2: Implement `message.go`**

Create `internal/repository/message.go`:

```go
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type MessageRepository interface {
	Create(ctx context.Context, m *domain.Message) error
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	MarkRead(ctx context.Context, id domain.ID, at time.Time) error
}

type messageRepo struct{ db *gorm.DB }

func NewMessageRepository(db *gorm.DB) MessageRepository { return &messageRepo{db: db} }

func (r *messageRepo) Create(ctx context.Context, m *domain.Message) error {
	if err := r.db.WithContext(ctx).Create(msgDomainToRow(m)).Error; err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	return nil
}

func (r *messageRepo) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	var row messageRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return msgRowToDomain(&row), nil
}

func (r *messageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where("to_uid = ?", toUID)
	if unreadOnly {
		q = q.Where("read_at IS NULL")
	}
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []messageRow
	if err := q.Order("created_at DESC, id DESC").Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list inbox: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Message, len(rows))
	for i := range rows {
		out[i] = msgRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *messageRepo) ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where(
		"(from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)",
		uidA, uidB, uidB, uidA,
	)
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []messageRow
	if err := q.Order("created_at DESC, id DESC").Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list conversation: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Message, len(rows))
	for i := range rows {
		out[i] = msgRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *messageRepo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("id = ? AND read_at IS NULL", id).
		Update("read_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark read: %w", res.Error)
	}
	return nil
}
```

- [ ] **Step 3: Write integration test**

Create `internal/repository/message_test.go`:

```go
//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupMsgRepo(t *testing.T) repository.MessageRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewMessageRepository(db)
}

func TestMessageRepo_CreateAndRead(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	m := &domain.Message{
		ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
		Kind: domain.MessageKindText, Content: "hi", CreatedAt: now,
	}
	if err := repo.Create(ctx, m); err != nil {
		t.Fatal(err)
	}

	inbox, _, err := repo.ListInbox(ctx, "bob", domain.Page{}, true)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox len=%d err=%v", len(inbox), err)
	}

	if err := repo.MarkRead(ctx, m.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	unread, _, _ := repo.ListInbox(ctx, "bob", domain.Page{}, true)
	if len(unread) != 0 {
		t.Fatalf("expected 0 unread, got %d", len(unread))
	}
}

func TestMessageRepo_Conversation(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	now := time.Now().UTC()
	pairs := []struct{ from, to string }{
		{"a", "b"}, {"b", "a"}, {"a", "c"},
	}
	for _, p := range pairs {
		_ = repo.Create(ctx, &domain.Message{
			ID: idgen.NewID(), FromUID: p.from, ToUID: p.to,
			Kind: domain.MessageKindText, Content: "x", CreatedAt: now,
		})
		time.Sleep(2 * time.Millisecond)
	}
	conv, _, err := repo.ListConversation(ctx, "a", "b", domain.Page{Limit: 10})
	if err != nil || len(conv) != 2 {
		t.Fatalf("conv len=%d err=%v", len(conv), err)
	}
}
```

- [ ] **Step 4: Run integration tests**

Run: `make test-integration`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/repository/message.go internal/repository/message_test.go internal/repository/db.go
git commit -m "feat(repository): add message repository"
```

---

### Task 12: `repository/comment.go` + integration test

**Files:**
- Modify: `internal/repository/db.go` (add commentRow)
- Create: `internal/repository/comment.go`
- Test: `internal/repository/comment_test.go` (build tag `integration`)

- [ ] **Step 1: Append `commentRow` to `db.go`**

Append to `internal/repository/db.go`:

```go
type commentRow struct {
	ID         uuid.UUID      `gorm:"column:id;primaryKey"`
	OwnerUID   string         `gorm:"column:owner_uid"`
	TargetType string         `gorm:"column:target_type"`
	TargetID   uuid.UUID      `gorm:"column:target_id"`
	ParentID   *uuid.UUID     `gorm:"column:parent_id"`
	RootID     uuid.UUID      `gorm:"column:root_id"`
	Content    string         `gorm:"column:content"`
	LikeCount  int            `gorm:"column:like_count"`
	ReplyCount int            `gorm:"column:reply_count"`
	Status     int16          `gorm:"column:status"`
	CreatedAt  time.Time      `gorm:"column:created_at"`
	UpdatedAt  time.Time      `gorm:"column:updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (commentRow) TableName() string { return "comments" }

func commentRowToDomain(r *commentRow) *domain.Comment {
	return &domain.Comment{
		ID:         r.ID,
		OwnerUID:   r.OwnerUID,
		TargetType: domain.TargetType(r.TargetType),
		TargetID:   r.TargetID,
		ParentID:   r.ParentID,
		RootID:     r.RootID,
		Content:    r.Content,
		LikeCount:  r.LikeCount,
		ReplyCount: r.ReplyCount,
		Status:     domain.CommentStatus(r.Status),
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
}

func commentDomainToRow(c *domain.Comment) *commentRow {
	return &commentRow{
		ID:         c.ID,
		OwnerUID:   c.OwnerUID,
		TargetType: string(c.TargetType),
		TargetID:   c.TargetID,
		ParentID:   c.ParentID,
		RootID:     c.RootID,
		Content:    c.Content,
		LikeCount:  c.LikeCount,
		ReplyCount: c.ReplyCount,
		Status:     int16(c.Status),
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
}
```

- [ ] **Step 2: Implement `comment.go`**

Create `internal/repository/comment.go`:

```go
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type CommentRepository interface {
	Create(ctx context.Context, c *domain.Comment) error
	Get(ctx context.Context, id domain.ID) (*domain.Comment, error)
	ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	IncReplyCount(ctx context.Context, parentID domain.ID) error
	SoftDelete(ctx context.Context, id domain.ID) error
}

type commentRepo struct{ db *gorm.DB }

func NewCommentRepository(db *gorm.DB) CommentRepository { return &commentRepo{db: db} }

func (r *commentRepo) Create(ctx context.Context, c *domain.Comment) error {
	if err := r.db.WithContext(ctx).Create(commentDomainToRow(c)).Error; err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	return nil
}

func (r *commentRepo) Get(ctx context.Context, id domain.ID) (*domain.Comment, error) {
	var row commentRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get comment: %w", err)
	}
	return commentRowToDomain(&row), nil
}

func (r *commentRepo) ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("target_type = ? AND target_id = ? AND parent_id IS NULL", string(t), target).
		Order("created_at DESC, id DESC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []commentRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list by target: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *commentRepo) ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("root_id = ? AND id <> root_id", rootID).
		Order("created_at ASC, id ASC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("id > ?", page.Cursor)
	}
	var rows []commentRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list replies: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Comment, len(rows))
	for i := range rows {
		out[i] = commentRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *commentRepo) IncReplyCount(ctx context.Context, parentID domain.ID) error {
	res := r.db.WithContext(ctx).Model(&commentRow{}).
		Where("id = ?", parentID).
		UpdateColumn("reply_count", gorm.Expr("reply_count + 1"))
	if res.Error != nil {
		return fmt.Errorf("inc reply count: %w", res.Error)
	}
	return nil
}

func (r *commentRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&commentRow{})
	if res.Error != nil {
		return fmt.Errorf("soft delete: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 3: Write integration test**

Create `internal/repository/comment_test.go`:

```go
//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupCommentRepo(t *testing.T) repository.CommentRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewCommentRepository(db)
}

func TestCommentRepo_TopLevelAndReply(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)

	target := idgen.NewID()
	now := time.Now().UTC()

	root := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u1", TargetType: domain.TargetTypeFile,
		TargetID: target, Content: "top", Status: domain.CommentStatusNormal,
		CreatedAt: now, UpdatedAt: now,
	}
	root.RootID = root.ID
	if err := repo.Create(ctx, root); err != nil {
		t.Fatal(err)
	}

	reply := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u2", TargetType: domain.TargetTypeFile,
		TargetID: target, ParentID: &root.ID, RootID: root.ID,
		Content: "child", Status: domain.CommentStatusNormal,
		CreatedAt: now.Add(time.Millisecond), UpdatedAt: now.Add(time.Millisecond),
	}
	if err := repo.Create(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if err := repo.IncReplyCount(ctx, root.ID); err != nil {
		t.Fatal(err)
	}

	tops, _, err := repo.ListByTarget(ctx, domain.TargetTypeFile, target, domain.Page{})
	if err != nil || len(tops) != 1 {
		t.Fatalf("tops len=%d err=%v", len(tops), err)
	}
	if tops[0].ReplyCount != 1 {
		t.Errorf("reply_count = %d, want 1", tops[0].ReplyCount)
	}

	replies, _, err := repo.ListReplies(ctx, root.ID, domain.Page{})
	if err != nil || len(replies) != 1 {
		t.Fatalf("replies len=%d err=%v", len(replies), err)
	}
}

func TestCommentRepo_SoftDelete(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)

	c := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u", TargetType: domain.TargetTypeFile,
		TargetID: idgen.NewID(), Content: "x", Status: domain.CommentStatusNormal,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	c.RootID = c.ID
	_ = repo.Create(ctx, c)
	if err := repo.SoftDelete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Get(ctx, c.ID)
	if err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 4: Run integration tests**

Run: `make test-integration`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/repository/comment.go internal/repository/comment_test.go internal/repository/db.go
git commit -m "feat(repository): add comment repository with reply count maintenance"
```

---

## Phase 4: File Storage

### Task 13: `storage/storage.go` (interface and ObjectInfo)

**Files:**
- Create: `internal/storage/storage.go`

- [ ] **Step 1: Implement interface**

Create `internal/storage/storage.go`:

```go
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned when the requested object does not exist.
var ErrNotFound = errors.New("storage: not found")

// ErrNotSupported is returned by PresignGet on backends that cannot pre-sign.
var ErrNotSupported = errors.New("storage: operation not supported by backend")

// ObjectInfo is the common metadata returned by Stat.
type ObjectInfo struct {
	Key      string
	Size     int64
	ETag     string
	MimeType string
	Modified time.Time
}

// FileStorage is the backend-agnostic file store used by the file service.
//
//   - Put returns a backend-generated storage key plus the number of bytes written.
//     The reader is consumed once; implementations may buffer to disk for size/checksum.
//   - Get returns a stream the caller MUST close.
//   - Delete returns ErrNotFound when the key doesn't exist.
//   - PresignGet returns a time-limited URL the client can fetch directly.
//     Backends that cannot pre-sign return ErrNotSupported.
type FileStorage interface {
	Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Stat(ctx context.Context, key string) (*ObjectInfo, error)
}
```

- [ ] **Step 2: Smoke build**

Run: `go build ./internal/storage/`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/storage/storage.go
git commit -m "feat(storage): add FileStorage interface and ObjectInfo"
```

---

### Task 14: `storage/local.go` (local FS + HMAC presign)

**Files:**
- Create: `internal/storage/local.go`
- Test: `internal/storage/local_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/storage/local_test.go`:

```go
package storage_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/storage"
)

func newLocal(t *testing.T) storage.FileStorage {
	t.Helper()
	return storage.NewLocal(storage.LocalConfig{
		Root:          t.TempDir(),
		SigningKey:    "test-signing-key",
		PublicBaseURL: "http://example.com",
	})
}

func TestLocal_PutGetStatDelete(t *testing.T) {
	ctx := context.Background()
	s := newLocal(t)
	key, n, err := s.Put(ctx, "uid-1", "hello.txt", strings.NewReader("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 11 {
		t.Errorf("size = %d", n)
	}
	if !strings.Contains(key, "uid-1") {
		t.Errorf("key doesn't include owner: %q", key)
	}

	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "hello world" {
		t.Errorf("body = %q", body)
	}

	info, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 11 {
		t.Errorf("stat size = %d", info.Size)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); err != storage.ErrNotFound {
		t.Errorf("after delete, Get returned %v", err)
	}
}

func TestLocal_PresignVerifies(t *testing.T) {
	ctx := context.Background()
	s := newLocal(t)
	key, _, _ := s.Put(ctx, "uid-2", "f.bin", strings.NewReader("data"))

	url, err := s.PresignGet(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "http://example.com/files/dl?") {
		t.Errorf("url = %q", url)
	}

	local := s.(*storage.Local)
	if err := local.VerifyPresignedURL(url, time.Now()); err != nil {
		t.Errorf("verify fresh url: %v", err)
	}
	// Tamper: change query
	tampered := strings.Replace(url, "key=", "key=xx", 1)
	if err := local.VerifyPresignedURL(tampered, time.Now()); err == nil {
		t.Error("verify tampered url should fail")
	}
	// Expired
	if err := local.VerifyPresignedURL(url, time.Now().Add(time.Hour)); err == nil {
		t.Error("verify expired url should fail")
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/storage/...`
Expected: FAIL (undefined: NewLocal, Local, LocalConfig)

- [ ] **Step 3: Implement `local.go`**

Create `internal/storage/local.go`:

```go
package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

type LocalConfig struct {
	Root          string
	SigningKey    string
	PublicBaseURL string
}

type Local struct {
	cfg LocalConfig
}

func NewLocal(cfg LocalConfig) *Local { return &Local{cfg: cfg} }

func (l *Local) keyToPath(key string) string {
	return filepath.Join(l.cfg.Root, key)
}

func (l *Local) Put(ctx context.Context, ownerUID, _ string, r io.Reader) (string, int64, error) {
	now := time.Now().UTC()
	id := idgen.NewID().String()
	key := filepath.ToSlash(filepath.Join(ownerUID, now.Format("2006"), now.Format("01"), id))
	path := l.keyToPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return "", 0, fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return "", 0, fmt.Errorf("write: %w", err)
	}
	return key, n, nil
}

func (l *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(l.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return f, nil
}

func (l *Local) Delete(ctx context.Context, key string) error {
	err := os.Remove(l.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	return nil
}

func (l *Local) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	info, err := os.Stat(l.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	return &ObjectInfo{
		Key:      key,
		Size:     info.Size(),
		Modified: info.ModTime().UTC(),
	}, nil
}

func (l *Local) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	expires := time.Now().Add(ttl).Unix()
	sig := l.sign(key, expires)
	v := url.Values{}
	v.Set("key", key)
	v.Set("exp", strconv.FormatInt(expires, 10))
	v.Set("sig", sig)
	return l.cfg.PublicBaseURL + "/files/dl?" + v.Encode(), nil
}

func (l *Local) sign(key string, expires int64) string {
	mac := hmac.New(sha256.New, []byte(l.cfg.SigningKey))
	fmt.Fprintf(mac, "%s|%d", key, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyPresignedURL is used by the download handler to authenticate a presigned URL.
// It validates the signature and rejects expired URLs based on `now`.
func (l *Local) VerifyPresignedURL(rawURL string, now time.Time) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	q := u.Query()
	key := q.Get("key")
	expStr := q.Get("exp")
	gotSig := q.Get("sig")
	if key == "" || expStr == "" || gotSig == "" {
		return errors.New("presign: missing params")
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return fmt.Errorf("presign: bad exp: %w", err)
	}
	if now.Unix() > exp {
		return errors.New("presign: expired")
	}
	wantSig := l.sign(key, exp)
	if !hmac.Equal([]byte(gotSig), []byte(wantSig)) {
		return errors.New("presign: bad signature")
	}
	return nil
}

// Ensure Local satisfies the interface.
var _ FileStorage = (*Local)(nil)
```

- [ ] **Step 4: Verify test passes**

Run: `go test ./internal/storage/... -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/storage/local.go internal/storage/local_test.go
git commit -m "feat(storage): add local filesystem backend with HMAC presigned URLs"
```

---

### Task 15: `storage/s3.go` + MinIO integration test

**Files:**
- Create: `internal/storage/s3.go`
- Test: `internal/storage/s3_test.go` (build tag `integration`)

- [ ] **Step 1: Add AWS SDK dependencies**

Run:
```
go get github.com/aws/aws-sdk-go-v2@v1.30.3
go get github.com/aws/aws-sdk-go-v2/config@v1.27.27
go get github.com/aws/aws-sdk-go-v2/credentials@v1.17.27
go get github.com/aws/aws-sdk-go-v2/service/s3@v1.58.2
```

- [ ] **Step 2: Implement `s3.go`**

Create `internal/storage/s3.go`:

```go
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

type S3Config struct {
	Region       string
	Bucket       string
	Endpoint     string // optional; for MinIO/aliyun OSS
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
}

type S3 struct {
	cfg    S3Config
	client *s3.Client
}

func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})
	return &S3{cfg: cfg, client: client}, nil
}

func (s *S3) buildKey(ownerUID string) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s/%s/%s/%s", ownerUID, now.Format("2006"), now.Format("01"), idgen.NewID())
}

func (s *S3) Put(ctx context.Context, ownerUID, _ string, r io.Reader) (string, int64, error) {
	// io.Reader → []byte via io.ReadAll for size accounting; service should pre-bound size.
	body, err := io.ReadAll(r)
	if err != nil {
		return "", 0, fmt.Errorf("read body: %w", err)
	}
	key := s.buildKey(ownerUID)
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body), // PutObject needs a seekable reader (SDK rewinds on retry)
	})
	if err != nil {
		return "", 0, fmt.Errorf("put object: %w", err)
	}
	return key, int64(len(body)), nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get object: %w", err)
	}
	return out.Body, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (s *S3) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("head object: %w", err)
	}
	info := &ObjectInfo{Key: key}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ETag != nil {
		info.ETag = *out.ETag
	}
	if out.LastModified != nil {
		info.Modified = *out.LastModified
	}
	return info, nil
}

func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	presigner := s3.NewPresignClient(s.client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign: %w", err)
	}
	return req.URL, nil
}

var _ FileStorage = (*S3)(nil)
```

- [ ] **Step 3: Add MinIO testcontainers module**

Run: `go get github.com/testcontainers/testcontainers-go/modules/minio@v0.32.0`

- [ ] **Step 4: Write integration test**

Create `internal/storage/s3_test.go`:

```go
//go:build integration

package storage_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/aero/aero-id/internal/storage"
)

func TestS3_RoundTrip(t *testing.T) {
	ctx := context.Background()
	m, err := minio.Run(ctx, "minio/minio:RELEASE.2025-01-20T14-49-07Z",
		minio.WithUsername("minio"), minio.WithPassword("minio123"))
	if err != nil {
		t.Fatalf("minio start: %v", err)
	}
	t.Cleanup(func() { _ = m.Terminate(ctx) })

	endpoint, _ := m.ConnectionString(ctx)
	s, err := storage.NewS3(ctx, storage.S3Config{
		Region:       "us-east-1",
		Bucket:       "test-bucket",
		Endpoint:     "http://" + endpoint,
		AccessKey:    "minio",
		SecretKey:    "minio123",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Bucket creation is a one-time bootstrap; MinIO does not auto-create.
	// For MVP we assume infra creates buckets; here we create it via raw client.
	if err := createBucket(ctx, s, "test-bucket"); err != nil {
		t.Fatal(err)
	}

	key, n, err := s.Put(ctx, "uid", "f.bin", strings.NewReader("hello s3"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("size = %d", n)
	}

	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "hello s3" {
		t.Errorf("body = %q", body)
	}

	info, err := s.Stat(ctx, key)
	if err != nil || info.Size != 8 {
		t.Fatalf("stat %+v err=%v", info, err)
	}

	url, err := s.PresignGet(ctx, key, 5*time.Minute)
	if err != nil || !strings.Contains(url, "X-Amz-Signature") {
		t.Errorf("presign url = %q err=%v", url, err)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); err != storage.ErrNotFound {
		t.Errorf("after delete: %v", err)
	}
}

// createBucket is a small helper because the public API doesn't expose CreateBucket;
// in production buckets are pre-provisioned by infra.
func createBucket(ctx context.Context, s *storage.S3, _ string) error {
	return s.EnsureBucket(ctx)
}
```

- [ ] **Step 5: Add `EnsureBucket` test helper to `s3.go`**

Append to `internal/storage/s3.go`:

```go
// EnsureBucket idempotently creates the configured bucket. Useful for tests.
func (s *S3) EnsureBucket(ctx context.Context) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.cfg.Bucket),
	})
	if err == nil {
		return nil
	}
	var bne *s3types.BucketAlreadyOwnedByYou
	var bex *s3types.BucketAlreadyExists
	if errors.As(err, &bne) || errors.As(err, &bex) {
		return nil
	}
	return fmt.Errorf("create bucket: %w", err)
}
```

- [ ] **Step 6: Run integration test**

Run: `make test-integration`
Expected: PASS (pulls minio image first time, ~30s)

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/storage/s3.go internal/storage/s3_test.go
git commit -m "feat(storage): add S3 backend with MinIO-based integration test"
```

---

## Phase 5: SSO Integration

### Task 16: SSO proto + code generation

**Files:**
- Create: `api/proto/sso/v1/sso.proto`
- Modify: `Makefile`
- Generated: `internal/sso/ssopb/*.go`

- [ ] **Step 1: Write SSO proto**

Create `api/proto/sso/v1/sso.proto`:

```proto
syntax = "proto3";

package sso.v1;

option go_package = "github.com/aero/aero-id/internal/sso/ssopb";

service SSO {
  rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);
  rpc GetUsers(GetUsersRequest) returns (GetUsersResponse);
}

message ValidateTokenRequest {
  string token = 1;
}

message ValidateTokenResponse {
  string uid = 1;
  int64  expires_at_unix = 2; // 0 if no expiry
}

message GetUsersRequest {
  repeated string uids = 1;
}

message User {
  string uid      = 1;
  string nickname = 2;
  string avatar   = 3;
}

message GetUsersResponse {
  repeated User users = 1;
}
```

- [ ] **Step 2: Install protoc-gen-go tooling**

Run:
```
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

> Ensure `$(go env GOPATH)/bin` is on `$PATH`.

- [ ] **Step 3: Add `proto` Makefile target**

Append to `Makefile`:

```makefile
.PHONY: proto
proto:
	mkdir -p internal/sso/ssopb internal/handler/grpc/aeropb
	protoc \
	  --proto_path=api/proto \
	  --go_out=. --go_opt=module=github.com/aero/aero-id \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/aero/aero-id \
	  api/proto/sso/v1/sso.proto
```

> The `aero_id/v1/*.proto` lines will be added in Task 29; for now this target only generates SSO.

- [ ] **Step 4: Generate**

Run: `make proto`
Expected: creates `internal/sso/ssopb/sso.pb.go` and `internal/sso/ssopb/sso_grpc.pb.go`.

- [ ] **Step 5: Verify generated code compiles**

Run: `go build ./internal/sso/...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add api/proto/ Makefile internal/sso/ssopb/
git commit -m "feat(sso): define SSO gRPC proto and code generation"
```

---

### Task 17: `sso/token_cache.go` + tests

**Files:**
- Create: `internal/sso/token_cache.go`
- Test: `internal/sso/token_cache_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/sso/token_cache_test.go`:

```go
package sso

import (
	"testing"
	"time"
)

func TestTokenCache_HitMissExpire(t *testing.T) {
	c := NewTokenCache(60 * time.Second)
	if _, ok := c.Get("tok1"); ok {
		t.Fatal("empty cache should miss")
	}
	c.Set("tok1", "uid-1", time.Now().Add(30*time.Second))
	if uid, ok := c.Get("tok1"); !ok || uid != "uid-1" {
		t.Fatalf("expected hit uid-1, got uid=%q ok=%v", uid, ok)
	}
	// Manually set an expired entry to verify Get filters it.
	c.Set("tok2", "uid-2", time.Now().Add(-1*time.Second))
	if _, ok := c.Get("tok2"); ok {
		t.Fatal("expired entry should be filtered")
	}
}

func TestTokenCache_TTLClampsToConfigured(t *testing.T) {
	c := NewTokenCache(60 * time.Second)
	exp := time.Now().Add(24 * time.Hour) // far future
	c.Set("tok", "uid", exp)
	got, _ := c.GetExpiry("tok")
	if time.Until(got) > 61*time.Second {
		t.Fatalf("expiry clamp failed: %v", time.Until(got))
	}
}

func TestTokenCache_Cleanup(t *testing.T) {
	c := NewTokenCache(60 * time.Second)
	c.Set("a", "u1", time.Now().Add(-1*time.Second))
	c.Set("b", "u2", time.Now().Add(time.Hour))
	c.SweepExpired(time.Now())
	if _, ok := c.Get("a"); ok {
		t.Error("a should be swept")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("b should remain")
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/sso/... -run TokenCache`
Expected: FAIL (undefined: TokenCache, NewTokenCache)

- [ ] **Step 3: Implement `token_cache.go`**

Create `internal/sso/token_cache.go`:

```go
package sso

import (
	"sync"
	"time"
)

// TokenCache is an in-memory token→uid cache with a maximum TTL ceiling.
// SSO-provided expirations longer than the ceiling are clamped.
type TokenCache struct {
	maxTTL time.Duration
	mu     sync.RWMutex
	items  map[string]tokenEntry
}

type tokenEntry struct {
	uid    string
	expiry time.Time
}

func NewTokenCache(maxTTL time.Duration) *TokenCache {
	return &TokenCache{
		maxTTL: maxTTL,
		items:  make(map[string]tokenEntry),
	}
}

func (c *TokenCache) Get(token string) (string, bool) {
	c.mu.RLock()
	e, ok := c.items[token]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiry) {
		return "", false
	}
	return e.uid, true
}

func (c *TokenCache) GetExpiry(token string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[token]
	return e.expiry, ok
}

// Set stores token→uid with the lesser of (provided expiry, now+maxTTL).
func (c *TokenCache) Set(token, uid string, expiry time.Time) {
	ceiling := time.Now().Add(c.maxTTL)
	if expiry.IsZero() || expiry.After(ceiling) {
		expiry = ceiling
	}
	c.mu.Lock()
	c.items[token] = tokenEntry{uid: uid, expiry: expiry}
	c.mu.Unlock()
}

func (c *TokenCache) SweepExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.items {
		if now.After(e.expiry) {
			delete(c.items, k)
		}
	}
}

// RunCleaner starts a background goroutine that sweeps expired entries.
// It returns a stop func; call it to terminate the goroutine.
func (c *TokenCache) RunCleaner(interval time.Duration) (stop func()) {
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-t.C:
				c.SweepExpired(time.Now())
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/sso/... -v -run TokenCache`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/sso/token_cache.go internal/sso/token_cache_test.go
git commit -m "feat(sso): add TokenCache with TTL ceiling and background sweeper"
```

---

### Task 18: `sso/user_cache.go` + tests

**Files:**
- Create: `internal/sso/user_cache.go`
- Test: `internal/sso/user_cache_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/sso/user_cache_test.go`:

```go
package sso

import (
	"context"
	"testing"
	"time"
)

type fakeFetcher struct {
	calls [][]string
}

func (f *fakeFetcher) Fetch(ctx context.Context, uids []string) (map[string]User, error) {
	f.calls = append(f.calls, append([]string(nil), uids...))
	out := make(map[string]User, len(uids))
	for _, u := range uids {
		out[u] = User{UID: u, Nickname: "n-" + u, Avatar: "a-" + u}
	}
	return out, nil
}

func TestUserCache_BatchMiss(t *testing.T) {
	f := &fakeFetcher{}
	c := NewUserCache(60*time.Second, f.Fetch)
	got, err := c.GetMany(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].Nickname != "n-a" || got["b"].Nickname != "n-b" {
		t.Fatalf("got %+v", got)
	}
	if len(f.calls) != 1 || len(f.calls[0]) != 2 {
		t.Fatalf("expected 1 fetch of 2 uids, got %+v", f.calls)
	}
}

func TestUserCache_HitsSkipFetch(t *testing.T) {
	f := &fakeFetcher{}
	c := NewUserCache(60*time.Second, f.Fetch)
	_, _ = c.GetMany(context.Background(), []string{"a"})
	_, _ = c.GetMany(context.Background(), []string{"a", "b"})
	// Second call should fetch only "b".
	last := f.calls[len(f.calls)-1]
	if len(last) != 1 || last[0] != "b" {
		t.Fatalf("expected fetch [b], got %v", last)
	}
}

func TestUserCache_DedupesInputUIDs(t *testing.T) {
	f := &fakeFetcher{}
	c := NewUserCache(60*time.Second, f.Fetch)
	_, _ = c.GetMany(context.Background(), []string{"a", "a", "a"})
	if len(f.calls[0]) != 1 {
		t.Fatalf("expected deduped to [a], got %v", f.calls[0])
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/sso/... -run UserCache`
Expected: FAIL (undefined: UserCache, User, NewUserCache)

- [ ] **Step 3: Implement `user_cache.go`**

Create `internal/sso/user_cache.go`:

```go
package sso

import (
	"context"
	"sync"
	"time"
)

// User is the public shape held in cache (mirrors SSO `User` proto).
type User struct {
	UID      string
	Nickname string
	Avatar   string
}

// Fetcher is the function the cache delegates to on miss.
type Fetcher func(ctx context.Context, uids []string) (map[string]User, error)

type userEntry struct {
	user   User
	expiry time.Time
}

type UserCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	items   map[string]userEntry
	fetcher Fetcher
}

func NewUserCache(ttl time.Duration, fetcher Fetcher) *UserCache {
	return &UserCache{
		ttl:     ttl,
		items:   make(map[string]userEntry),
		fetcher: fetcher,
	}
}

// GetMany returns a map keyed by uid. UIDs not found in cache trigger a single
// batch call to the configured Fetcher. Duplicate input uids are deduplicated.
func (c *UserCache) GetMany(ctx context.Context, uids []string) (map[string]User, error) {
	out := make(map[string]User, len(uids))
	seen := make(map[string]struct{}, len(uids))
	var missing []string
	now := time.Now()

	c.mu.RLock()
	for _, u := range uids {
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		if e, ok := c.items[u]; ok && now.Before(e.expiry) {
			out[u] = e.user
		} else {
			missing = append(missing, u)
		}
	}
	c.mu.RUnlock()

	if len(missing) == 0 {
		return out, nil
	}
	fetched, err := c.fetcher(ctx, missing)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	exp := now.Add(c.ttl)
	for u, info := range fetched {
		c.items[u] = userEntry{user: info, expiry: exp}
		out[u] = info
	}
	c.mu.Unlock()
	return out, nil
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/sso/... -v -run UserCache`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/sso/user_cache.go internal/sso/user_cache_test.go
git commit -m "feat(sso): add batched UserCache with single-fetch dedupe"
```

---

### Task 19: `sso/client.go` + bufconn integration test

**Files:**
- Create: `internal/sso/client.go`
- Test: `internal/sso/client_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/sso/client_test.go`:

```go
package sso_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/sso/ssopb"
)

type fakeSSOServer struct {
	ssopb.UnimplementedSSOServer
	validateCalls int
	getUsersCalls int
}

func (f *fakeSSOServer) ValidateToken(_ context.Context, req *ssopb.ValidateTokenRequest) (*ssopb.ValidateTokenResponse, error) {
	f.validateCalls++
	if req.Token == "bad" {
		return nil, grpc.Errorf(7, "invalid")
	}
	return &ssopb.ValidateTokenResponse{Uid: "uid-of-" + req.Token, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()}, nil
}

func (f *fakeSSOServer) GetUsers(_ context.Context, req *ssopb.GetUsersRequest) (*ssopb.GetUsersResponse, error) {
	f.getUsersCalls++
	users := make([]*ssopb.User, 0, len(req.Uids))
	for _, uid := range req.Uids {
		users = append(users, &ssopb.User{Uid: uid, Nickname: "n-" + uid, Avatar: "a"})
	}
	return &ssopb.GetUsersResponse{Users: users}, nil
}

func startFakeSSO(t *testing.T) (*sso.Client, *fakeSSOServer) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &fakeSSOServer{}
	ssopb.RegisterSSOServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	c := sso.NewClientFromConn(conn, sso.ClientConfig{
		TokenCacheTTL: 60 * time.Second,
		UserCacheTTL:  60 * time.Second,
	})
	return c, fake
}

func TestClient_ValidateTokenCaches(t *testing.T) {
	c, fake := startFakeSSO(t)
	ctx := context.Background()

	uid, err := c.ValidateToken(ctx, "abc")
	if err != nil || uid != "uid-of-abc" {
		t.Fatalf("first call uid=%q err=%v", uid, err)
	}
	uid, _ = c.ValidateToken(ctx, "abc")
	if uid != "uid-of-abc" {
		t.Fatalf("second call uid=%q", uid)
	}
	if fake.validateCalls != 1 {
		t.Errorf("validate calls = %d, want 1 (cached)", fake.validateCalls)
	}
}

func TestClient_ValidateTokenBadReturnsError(t *testing.T) {
	c, _ := startFakeSSO(t)
	_, err := c.ValidateToken(context.Background(), "bad")
	if err == nil {
		t.Fatal("expected error for bad token")
	}
}

func TestClient_GetUsersDedupesAcrossCalls(t *testing.T) {
	c, fake := startFakeSSO(t)
	ctx := context.Background()

	_, _ = c.GetUsers(ctx, []string{"u1", "u2"})
	_, _ = c.GetUsers(ctx, []string{"u1", "u3"})

	if fake.getUsersCalls != 2 {
		t.Errorf("calls = %d, want 2 (one per non-cached batch)", fake.getUsersCalls)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/sso/...`
Expected: FAIL (undefined: Client, NewClientFromConn, ClientConfig, ValidateToken, GetUsers)

- [ ] **Step 3: Implement `client.go`**

Create `internal/sso/client.go`:

```go
package sso

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aero/aero-id/internal/sso/ssopb"
)

type ClientConfig struct {
	TokenCacheTTL time.Duration
	UserCacheTTL  time.Duration
	Timeout       time.Duration // per-RPC deadline for SSO calls; 0 = no deadline
}

type Client struct {
	conn       *grpc.ClientConn
	rpc        ssopb.SSOClient
	tokenCache *TokenCache
	userCache  *UserCache
	stopFn     func()
	timeout    time.Duration
}

// NewClient dials the SSO endpoint and returns a Client with caches running.
func NewClient(ctx context.Context, endpoint string, insecureConn bool, cfg ClientConfig) (*Client, error) {
	var opts []grpc.DialOption
	if insecureConn {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial sso: %w", err)
	}
	return NewClientFromConn(conn, cfg), nil
}

// NewClientFromConn lets tests inject a bufconn-backed *grpc.ClientConn.
func NewClientFromConn(conn *grpc.ClientConn, cfg ClientConfig) *Client {
	c := &Client{
		conn:       conn,
		rpc:        ssopb.NewSSOClient(conn),
		tokenCache: NewTokenCache(cfg.TokenCacheTTL),
		timeout:    cfg.Timeout,
	}
	c.userCache = NewUserCache(cfg.UserCacheTTL, c.fetchUsers)
	stop := c.tokenCache.RunCleaner(30 * time.Second)
	c.stopFn = stop
	return c
}

// Close stops the cache sweeper and closes the underlying gRPC connection.
func (c *Client) Close() {
	if c.stopFn != nil {
		c.stopFn()
		c.stopFn = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// ValidateToken returns the uid for a Bearer token, consulting the cache first.
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
	if uid, ok := c.tokenCache.Get(token); ok {
		return uid, nil
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp, err := c.rpc.ValidateToken(ctx, &ssopb.ValidateTokenRequest{Token: token})
	if err != nil {
		return "", fmt.Errorf("sso validate: %w", err)
	}
	var exp time.Time
	if resp.ExpiresAtUnix > 0 {
		exp = time.Unix(resp.ExpiresAtUnix, 0)
	}
	c.tokenCache.Set(token, resp.Uid, exp)
	return resp.Uid, nil
}

// GetUsers returns user info for the given uids, batching uncached entries.
func (c *Client) GetUsers(ctx context.Context, uids []string) (map[string]User, error) {
	return c.userCache.GetMany(ctx, uids)
}

func (c *Client) fetchUsers(ctx context.Context, uids []string) (map[string]User, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp, err := c.rpc.GetUsers(ctx, &ssopb.GetUsersRequest{Uids: uids})
	if err != nil {
		return nil, fmt.Errorf("sso get users: %w", err)
	}
	out := make(map[string]User, len(resp.Users))
	for _, u := range resp.Users {
		out[u.Uid] = User{UID: u.Uid, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out, nil
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/sso/... -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/sso/client.go internal/sso/client_test.go
git commit -m "feat(sso): add SSO client with token and user caching"
```

---

## Phase 6: Service Layer

> The service layer is the single home of business logic shared by both the HTTP and
> gRPC transports. It depends only on the **interfaces** defined in Phases 3–5
> (`repository.*`, `storage.FileStorage`) plus a narrow `UserDirectory` (satisfied by
> `*sso.Client`). Tests use hand-written function-field doubles in `service/mocks`
> (no codegen toolchain needed). Repos/storage return raw errors; the service is the
> layer that translates them into `*errors.AppError`.

### Task 20: `service/service.go` (interfaces + ctx helpers) + mocks

**Files:**
- Create: `internal/service/service.go`
- Create: `internal/service/mocks/mocks.go`
- Test: `internal/service/service_test.go` (package `service`, internal — exercises unexported helpers)

- [ ] **Step 1: Write failing test**

Create `internal/service/service_test.go`:

```go
package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestActorRoundTrip(t *testing.T) {
	ctx := WithActor(context.Background(), "u1")
	uid, ok := ActorFrom(ctx)
	if !ok || uid != "u1" {
		t.Fatalf("uid=%q ok=%v", uid, ok)
	}
}

func TestActorFrom_missing(t *testing.T) {
	if _, ok := ActorFrom(context.Background()); ok {
		t.Fatal("expected no actor in empty context")
	}
}

func TestActorFrom_emptyStringIsNotPresent(t *testing.T) {
	ctx := WithActor(context.Background(), "")
	if _, ok := ActorFrom(ctx); ok {
		t.Fatal("empty actor must be treated as absent")
	}
}

func TestMustActor_error(t *testing.T) {
	if _, err := mustActor(context.Background()); err == nil {
		t.Fatal("expected unauthorized error when actor is missing")
	}
}

func TestCanReadFile(t *testing.T) {
	pub := &domain.File{OwnerUID: "owner", Visibility: domain.VisibilityPublic}
	priv := &domain.File{OwnerUID: "owner", Visibility: domain.VisibilityPrivate}
	if !canReadFile("stranger", pub) {
		t.Error("public file should be readable by anyone")
	}
	if canReadFile("stranger", priv) {
		t.Error("private file should not be readable by a stranger")
	}
	if !canReadFile("owner", priv) {
		t.Error("owner should read its own private file")
	}
}

func TestCanReadMessage(t *testing.T) {
	m := &domain.Message{FromUID: "a", ToUID: "b"}
	if !canReadMessage("a", m) || !canReadMessage("b", m) {
		t.Error("both participants should read the message")
	}
	if canReadMessage("c", m) {
		t.Error("non-participant should not read the message")
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/service/`
Expected: FAIL (undefined: WithActor, ActorFrom, mustActor, canReadFile, canReadMessage).

- [ ] **Step 3: Implement `service.go`**

Create `internal/service/service.go`:

```go
// Package service holds aero-id's business logic, shared by the HTTP and gRPC
// transports. It depends on repository/storage interfaces plus a narrow
// UserDirectory; it is the boundary that converts raw errors into *errors.AppError.
package service

import (
	"context"
	"io"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/sso"
)

// ctxKey is an unexported context key type so callers cannot collide with us.
type ctxKey int

const actorKey ctxKey = iota

// WithActor returns a child context carrying the authenticated uid. The HTTP auth
// middleware and the gRPC auth interceptor call this after validating the token
// with SSO. The actor is never written to business tables — it is only used for
// authorization decisions and logging.
func WithActor(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, actorKey, uid)
}

// ActorFrom extracts the authenticated uid. ok is false when absent or empty.
func ActorFrom(ctx context.Context) (string, bool) {
	uid, _ := ctx.Value(actorKey).(string)
	return uid, uid != ""
}

// UserDirectory is the subset of the SSO client the service uses to enrich list
// responses with author/sender display info. *sso.Client satisfies it.
type UserDirectory interface {
	GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error)
}

// ---- Input/output DTOs ----

type CreateFileInput struct {
	Name       string
	MimeType   string
	Visibility domain.Visibility
	Reader     io.Reader
	Meta       map[string]any
}

type FileList struct {
	Files      []*domain.File
	NextCursor string
}

type SendMessageInput struct {
	ToUID   string
	Kind    domain.MessageKind
	Content string
	FileID  *domain.ID
	Meta    map[string]any
}

type MessageList struct {
	Messages   []*domain.Message
	NextCursor string
	Users      map[string]sso.User
}

type CreateCommentInput struct {
	TargetType domain.TargetType
	TargetID   domain.ID
	ParentID   *domain.ID
	Content    string
}

type CommentList struct {
	Comments   []*domain.Comment
	NextCursor string
	Users      map[string]sso.User
}

// ---- Service interfaces (implemented in file.go / message.go / comment.go) ----

type FileService interface {
	Create(ctx context.Context, in CreateFileInput) (*domain.File, error)
	Get(ctx context.Context, id domain.ID) (*domain.File, error)
	List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error)
	Delete(ctx context.Context, id domain.ID) error
	DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error)
}

type MessageService interface {
	Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error)
	Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error)
	MarkRead(ctx context.Context, id domain.ID) error
}

type CommentService interface {
	Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error)
	List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error)
	Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error)
	Delete(ctx context.Context, id domain.ID) error
}

// ---- shared helpers (unexported; reused across the three service impls) ----

// mustActor returns the authenticated uid or an Unauthorized AppError.
func mustActor(ctx context.Context) (string, error) {
	uid, ok := ActorFrom(ctx)
	if !ok {
		return "", apperr.Unauthorized("auth.unauthorized", "missing authenticated actor")
	}
	return uid, nil
}

// canReadFile is the file read ACL: public files are world-readable; private ones
// only by their owner.
func canReadFile(actor string, f *domain.File) bool {
	return f.Visibility == domain.VisibilityPublic || f.OwnerUID == actor
}

// canReadMessage is the message read ACL: only the two participants.
func canReadMessage(actor string, m *domain.Message) bool {
	return m.FromUID == actor || m.ToUID == actor
}

// enrichUsers resolves display info for a set of uids via the directory. An empty
// input short-circuits without an RPC. The underlying cache deduplicates.
func enrichUsers(ctx context.Context, dir UserDirectory, uids []string) (map[string]sso.User, error) {
	if len(uids) == 0 {
		return map[string]sso.User{}, nil
	}
	return dir.GetUsers(ctx, uids)
}
```

- [ ] **Step 4: Implement `mocks/mocks.go`**

Create `internal/service/mocks/mocks.go`:

```go
// Package mocks provides function-field test doubles for the service layer's
// repository, storage, and SSO dependencies. Each method delegates to a field;
// a test sets only the fields the case under test actually exercises.
package mocks

import (
	"context"
	"io"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
)

type FileRepo struct {
	CreateFn      func(ctx context.Context, f *domain.File) error
	GetFn         func(ctx context.Context, id domain.ID) (*domain.File, error)
	ListByOwnerFn func(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error)
	DeleteFn      func(ctx context.Context, id domain.ID) error
}

func (m *FileRepo) Create(ctx context.Context, f *domain.File) error { return m.CreateFn(ctx, f) }
func (m *FileRepo) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return m.GetFn(ctx, id)
}
func (m *FileRepo) ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error) {
	return m.ListByOwnerFn(ctx, ownerUID, page)
}
func (m *FileRepo) Delete(ctx context.Context, id domain.ID) error { return m.DeleteFn(ctx, id) }

type MessageRepo struct {
	CreateFn           func(ctx context.Context, m *domain.Message) error
	GetFn              func(ctx context.Context, id domain.ID) (*domain.Message, error)
	ListInboxFn        func(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversationFn func(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	MarkReadFn         func(ctx context.Context, id domain.ID, at time.Time) error
}

func (m *MessageRepo) Create(ctx context.Context, msg *domain.Message) error {
	return m.CreateFn(ctx, msg)
}
func (m *MessageRepo) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	return m.GetFn(ctx, id)
}
func (m *MessageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
	return m.ListInboxFn(ctx, toUID, page, unreadOnly)
}
func (m *MessageRepo) ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error) {
	return m.ListConversationFn(ctx, uidA, uidB, page)
}
func (m *MessageRepo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
	return m.MarkReadFn(ctx, id, at)
}

type CommentRepo struct {
	CreateFn        func(ctx context.Context, c *domain.Comment) error
	GetFn           func(ctx context.Context, id domain.ID) (*domain.Comment, error)
	ListByTargetFn  func(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	ListRepliesFn   func(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	IncReplyCountFn func(ctx context.Context, parentID domain.ID) error
	SoftDeleteFn    func(ctx context.Context, id domain.ID) error
}

func (m *CommentRepo) Create(ctx context.Context, c *domain.Comment) error { return m.CreateFn(ctx, c) }
func (m *CommentRepo) Get(ctx context.Context, id domain.ID) (*domain.Comment, error) {
	return m.GetFn(ctx, id)
}
func (m *CommentRepo) ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	return m.ListByTargetFn(ctx, t, target, page)
}
func (m *CommentRepo) ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	return m.ListRepliesFn(ctx, rootID, page)
}
func (m *CommentRepo) IncReplyCount(ctx context.Context, parentID domain.ID) error {
	return m.IncReplyCountFn(ctx, parentID)
}
func (m *CommentRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	return m.SoftDeleteFn(ctx, id)
}

type Storage struct {
	PutFn        func(ctx context.Context, ownerUID, name string, r io.Reader) (string, int64, error)
	GetFn        func(ctx context.Context, key string) (io.ReadCloser, error)
	DeleteFn     func(ctx context.Context, key string) error
	PresignGetFn func(ctx context.Context, key string, ttl time.Duration) (string, error)
	StatFn       func(ctx context.Context, key string) (*storage.ObjectInfo, error)
}

func (m *Storage) Put(ctx context.Context, ownerUID, name string, r io.Reader) (string, int64, error) {
	return m.PutFn(ctx, ownerUID, name, r)
}
func (m *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) { return m.GetFn(ctx, key) }
func (m *Storage) Delete(ctx context.Context, key string) error               { return m.DeleteFn(ctx, key) }
func (m *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return m.PresignGetFn(ctx, key, ttl)
}
func (m *Storage) Stat(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	return m.StatFn(ctx, key)
}

type UserDir struct {
	GetUsersFn func(ctx context.Context, uids []string) (map[string]sso.User, error)
}

func (m *UserDir) GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error) {
	return m.GetUsersFn(ctx, uids)
}

// Compile-time guarantees the doubles satisfy the real interfaces.
var (
	_ repository.FileRepository    = (*FileRepo)(nil)
	_ repository.MessageRepository = (*MessageRepo)(nil)
	_ repository.CommentRepository = (*CommentRepo)(nil)
	_ storage.FileStorage          = (*Storage)(nil)
)
```

- [ ] **Step 5: Verify test passes**

Run: `go test ./internal/service/ ./internal/service/mocks/ -v`
Expected: PASS (6 tests in `service`; `mocks` has no tests but must compile).

- [ ] **Step 6: Commit**

```bash
git add internal/service/service.go internal/service/service_test.go internal/service/mocks/
git commit -m "feat(service): add service interfaces, ctx actor helpers, and test mocks"
```

---

### Task 21: `service/file.go` + tests

**Files:**
- Create: `internal/service/file.go`
- Test: `internal/service/file_test.go` (package `service_test`, external)

- [ ] **Step 1: Write failing test**

Create `internal/service/file_test.go`:

```go
package service_test

import (
	"context"
	stderrors "errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

// assertAppCode asserts err is an *AppError with the given HTTP status. Defined
// once here and reused by message_test.go and comment_test.go (same package).
func assertAppCode(t *testing.T, err error, wantHTTP int) {
	t.Helper()
	var ae *apperr.AppError
	if !stderrors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %v", err)
	}
	if ae.HTTP != wantHTTP {
		t.Fatalf("HTTP = %d, want %d (code=%s)", ae.HTTP, wantHTTP, ae.Code)
	}
}

func TestFileService_CreateStoresAndPersists(t *testing.T) {
	var created *domain.File
	repo := &mocks.FileRepo{
		CreateFn: func(_ context.Context, f *domain.File) error { created = f; return nil },
	}
	store := &mocks.Storage{
		PutFn: func(_ context.Context, owner, name string, _ io.Reader) (string, int64, error) {
			if owner != "owner" {
				t.Errorf("owner passed to storage = %q", owner)
			}
			return "owner/2026/05/key", 11, nil
		},
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")
	f, err := svc.Create(ctx, service.CreateFileInput{Name: "a.txt", Reader: strings.NewReader("hello world")})
	if err != nil {
		t.Fatal(err)
	}
	if f.OwnerUID != "owner" || f.Size != 11 || f.StorageType != "local" || f.StorageKey != "owner/2026/05/key" {
		t.Fatalf("unexpected file: %+v", f)
	}
	if created == nil {
		t.Fatal("repo.Create was not called")
	}
}

func TestFileService_CreateRequiresActor(t *testing.T) {
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local")
	_, err := svc.Create(context.Background(), service.CreateFileInput{Name: "a", Reader: strings.NewReader("x")})
	assertAppCode(t, err, 401)
}

func TestFileService_CreateRejectsEmptyName(t *testing.T) {
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, service.CreateFileInput{Reader: strings.NewReader("x")})
	assertAppCode(t, err, 400)
}

func TestFileService_GetPrivateForbidden(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "someone", Visibility: domain.VisibilityPrivate}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_GetPublicAllowed(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "someone", Visibility: domain.VisibilityPublic}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "stranger")
	if _, err := svc.Get(ctx, idgen.NewID()); err != nil {
		t.Fatalf("public file should be readable: %v", err)
	}
}

func TestFileService_GetNotFound(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) { return nil, repository.ErrNotFound },
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "x")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 404)
}

func TestFileService_ListOnlyOwnFiles(t *testing.T) {
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "me")
	_, err := svc.List(ctx, "someone-else", domain.Page{})
	assertAppCode(t, err, 403)
}

func TestFileService_ListDefaultsToActor(t *testing.T) {
	repo := &mocks.FileRepo{
		ListByOwnerFn: func(_ context.Context, owner string, _ domain.Page) ([]*domain.File, string, error) {
			if owner != "me" {
				t.Errorf("owner = %q, want me", owner)
			}
			return []*domain.File{{OwnerUID: "me"}}, "next", nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "me")
	list, err := svc.List(ctx, "", domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if list.NextCursor != "next" || len(list.Files) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestFileService_DeleteRequiresOwner(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "not-owner")
	err := svc.Delete(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_DeleteRemovesRowThenBlob(t *testing.T) {
	deletedRow, deletedBlob := false, false
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
		DeleteFn: func(_ context.Context, _ domain.ID) error { deletedRow = true; return nil },
	}
	store := &mocks.Storage{
		DeleteFn: func(_ context.Context, key string) error {
			if key != "k" {
				t.Errorf("blob key = %q", key)
			}
			deletedBlob = true
			return nil
		},
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if !deletedRow || !deletedBlob {
		t.Fatalf("deletedRow=%v deletedBlob=%v", deletedRow, deletedBlob)
	}
}

func TestFileService_DownloadURLAppliesACL(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate, StorageKey: "k"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	assertAppCode(t, err, 403)
}

func TestFileService_DownloadURLPresigns(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
	}
	store := &mocks.Storage{
		PresignGetFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
			return "https://signed/" + key, nil
		},
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")
	url, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	if err != nil || url != "https://signed/k" {
		t.Fatalf("url=%q err=%v", url, err)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/service/`
Expected: FAIL (undefined: service.NewFileService).

- [ ] **Step 3: Implement `file.go`**

Create `internal/service/file.go`:

```go
package service

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

type fileService struct {
	files       repository.FileRepository
	store       storage.FileStorage
	storageType string // "local" | "s3", recorded on each row for later routing
}

func NewFileService(files repository.FileRepository, store storage.FileStorage, storageType string) FileService {
	return &fileService{files: files, store: store, storageType: storageType}
}

func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, apperr.BadRequest("file.invalid", "name is required")
	}
	if in.Reader == nil {
		return nil, apperr.BadRequest("file.invalid", "file content is required")
	}

	key, size, err := s.store.Put(ctx, actor, in.Name, in.Reader)
	if err != nil {
		return nil, apperr.Internal("file.store_failed", "failed to store file").Wrap(err)
	}

	now := time.Now().UTC()
	f := &domain.File{
		ID:          idgen.NewID(),
		OwnerUID:    actor,
		Name:        in.Name,
		Size:        size,
		MimeType:    in.MimeType,
		StorageKey:  key,
		StorageType: s.storageType,
		Visibility:  in.Visibility,
		Meta:        in.Meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.files.Create(ctx, f); err != nil {
		_ = s.store.Delete(ctx, key) // best-effort cleanup of the orphaned blob
		return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
	}
	return f, nil
}

func (s *fileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return nil, apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if !canReadFile(actor, f) {
		return nil, apperr.Forbidden("file.forbidden", "not allowed to access this file")
	}
	return f, nil
}

func (s *fileService) List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if ownerUID == "" {
		ownerUID = actor
	}
	if ownerUID != actor {
		return nil, apperr.Forbidden("file.forbidden", "can only list your own files")
	}
	files, next, err := s.files.ListByOwner(ctx, ownerUID, page)
	if err != nil {
		return nil, apperr.Internal("file.list_failed", "failed to list files").Wrap(err)
	}
	return &FileList{Files: files, NextCursor: next}, nil
}

func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if f.OwnerUID != actor {
		return apperr.Forbidden("file.forbidden", "only the owner can delete this file")
	}
	if err := s.files.Delete(ctx, id); err != nil {
		return apperr.Internal("file.delete_failed", "failed to delete file").Wrap(err)
	}
	// The soft-deleted row is authoritative. Blob removal is best-effort: if it fails
	// the row is already gone, so we don't fail the request — the blob is left for a
	// future storage GC sweep rather than resurrecting the row.
	_ = s.store.Delete(ctx, f.StorageKey)
	return nil
}

func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
	f, err := s.Get(ctx, id) // reuses the read ACL above
	if err != nil {
		return "", err
	}
	url, err := s.store.PresignGet(ctx, f.StorageKey, ttl)
	if err != nil {
		return "", apperr.Internal("file.presign_failed", "failed to presign download URL").Wrap(err)
	}
	return url, nil
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/service/ -run TestFileService -v`
Expected: PASS (all `TestFileService_*`).

- [ ] **Step 5: Commit**

```bash
git add internal/service/file.go internal/service/file_test.go
git commit -m "feat(service): add file service with storage and read/owner ACL"
```

---

### Task 22: `service/message.go` + tests

**Files:**
- Create: `internal/service/message.go`
- Test: `internal/service/message_test.go` (package `service_test`)

- [ ] **Step 1: Write failing test**

Create `internal/service/message_test.go`:

```go
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
	"github.com/aero/aero-id/internal/sso"
)

func TestMessageService_SendRequiresToUID(t *testing.T) {
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{Content: "hi"})
	assertAppCode(t, err, 400)
}

func TestMessageService_SendDefaultsKindAndPersists(t *testing.T) {
	var saved *domain.Message
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, m *domain.Message) error { saved = m; return nil }}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	m, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if m.FromUID != "a" || m.ToUID != "b" || m.Kind != domain.MessageKindText {
		t.Fatalf("unexpected message: %+v", m)
	}
	if saved == nil {
		t.Fatal("repo.Create was not called")
	}
}

func TestMessageService_SendRejectsForeignFileAttachment(t *testing.T) {
	fid := idgen.NewID()
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "someone-else"}, nil
	}}
	svc := service.NewMessageService(&mocks.MessageRepo{}, files, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
	assertAppCode(t, err, 403)
}

func TestMessageService_SendRejectsMissingFile(t *testing.T) {
	fid := idgen.NewID()
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewMessageService(&mocks.MessageRepo{}, files, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
	assertAppCode(t, err, 400)
}

func TestMessageService_GetForbiddenForNonParticipant(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "c")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestMessageService_MarkReadOnlyRecipient(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a") // sender, not recipient
	err := svc.MarkRead(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestMessageService_MarkReadIdempotent(t *testing.T) {
	now := time.Now()
	called := false
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: id, FromUID: "a", ToUID: "b", ReadAt: &now}, nil
		},
		MarkReadFn: func(_ context.Context, _ domain.ID, _ time.Time) error { called = true; return nil },
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "b")
	if err := svc.MarkRead(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("MarkRead repo must not be called when already read")
	}
}

func TestMessageService_InboxDedupesAndEnrichesSenders(t *testing.T) {
	repo := &mocks.MessageRepo{ListInboxFn: func(_ context.Context, to string, _ domain.Page, _ bool) ([]*domain.Message, string, error) {
		if to != "b" {
			t.Errorf("inbox owner = %q, want b", to)
		}
		return []*domain.Message{{FromUID: "a"}, {FromUID: "a"}, {FromUID: "c"}}, "", nil
	}}
	var requested []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		out := map[string]sso.User{}
		for _, u := range uids {
			out[u] = sso.User{UID: u, Nickname: "n-" + u}
		}
		return out, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, dir)
	ctx := service.WithActor(context.Background(), "b")
	list, err := svc.Inbox(ctx, domain.Page{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 {
		t.Fatalf("expected 2 deduped sender uids, got %v", requested)
	}
	if list.Users["a"].Nickname != "n-a" {
		t.Fatalf("missing enrichment for a: %+v", list.Users)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/service/`
Expected: FAIL (undefined: service.NewMessageService).

- [ ] **Step 3: Implement `message.go`**

Create `internal/service/message.go`:

```go
package service

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type messageService struct {
	messages repository.MessageRepository
	files    repository.FileRepository // used only to validate file attachments
	dir      UserDirectory
}

func NewMessageService(messages repository.MessageRepository, files repository.FileRepository, dir UserDirectory) MessageService {
	return &messageService{messages: messages, files: files, dir: dir}
}

func (s *messageService) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.ToUID == "" {
		return nil, apperr.BadRequest("message.invalid", "to_uid is required")
	}
	if in.Kind == 0 {
		in.Kind = domain.MessageKindText
	}
	if in.Content == "" && in.FileID == nil {
		return nil, apperr.BadRequest("message.invalid", "content or file_id is required")
	}
	if in.FileID != nil {
		f, err := s.files.Get(ctx, *in.FileID)
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.BadRequest("message.invalid", "referenced file does not exist")
		}
		if err != nil {
			return nil, apperr.Internal("message.send_failed", "failed to verify file").Wrap(err)
		}
		if f.OwnerUID != actor {
			return nil, apperr.Forbidden("message.forbidden", "cannot attach a file you do not own")
		}
	}

	m := &domain.Message{
		ID:        idgen.NewID(),
		FromUID:   actor,
		ToUID:     in.ToUID,
		Kind:      in.Kind,
		Content:   in.Content,
		FileID:    in.FileID,
		Meta:      in.Meta,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.messages.Create(ctx, m); err != nil {
		return nil, apperr.Internal("message.send_failed", "failed to send message").Wrap(err)
	}
	return m, nil
}

func (s *messageService) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return nil, apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if !canReadMessage(actor, m) {
		return nil, apperr.Forbidden("message.forbidden", "not a participant in this message")
	}
	return m, nil
}

func (s *messageService) Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	msgs, next, err := s.messages.ListInbox(ctx, actor, page, unreadOnly)
	if err != nil {
		return nil, apperr.Internal("message.list_failed", "failed to list inbox").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, senderUIDs(msgs))
	if err != nil {
		return nil, apperr.Internal("message.enrich_failed", "failed to resolve senders").Wrap(err)
	}
	return &MessageList{Messages: msgs, NextCursor: next, Users: users}, nil
}

func (s *messageService) Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if withUID == "" {
		return nil, apperr.BadRequest("message.invalid", "peer uid is required")
	}
	msgs, next, err := s.messages.ListConversation(ctx, actor, withUID, page)
	if err != nil {
		return nil, apperr.Internal("message.list_failed", "failed to list conversation").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, []string{actor, withUID})
	if err != nil {
		return nil, apperr.Internal("message.enrich_failed", "failed to resolve participants").Wrap(err)
	}
	return &MessageList{Messages: msgs, NextCursor: next, Users: users}, nil
}

func (s *messageService) MarkRead(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if m.ToUID != actor {
		return apperr.Forbidden("message.forbidden", "only the recipient can mark a message read")
	}
	if m.ReadAt != nil {
		return nil // already read — idempotent no-op
	}
	if err := s.messages.MarkRead(ctx, id, time.Now().UTC()); err != nil {
		return apperr.Internal("message.mark_read_failed", "failed to mark message read").Wrap(err)
	}
	return nil
}

// senderUIDs returns the distinct from_uids preserving first-seen order.
func senderUIDs(msgs []*domain.Message) []string {
	seen := make(map[string]struct{}, len(msgs))
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if _, ok := seen[m.FromUID]; ok {
			continue
		}
		seen[m.FromUID] = struct{}{}
		out = append(out, m.FromUID)
	}
	return out
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/service/ -run TestMessageService -v`
Expected: PASS (all `TestMessageService_*`).

- [ ] **Step 5: Commit**

```bash
git add internal/service/message.go internal/service/message_test.go
git commit -m "feat(service): add message service with participant ACL and sender enrichment"
```

---

### Task 23: `service/comment.go` + tests (two-level enforcement)

**Files:**
- Create: `internal/service/comment.go`
- Test: `internal/service/comment_test.go` (package `service_test`)

- [ ] **Step 1: Write failing test**

Create `internal/service/comment_test.go`:

```go
package service_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
	"github.com/aero/aero-id/internal/sso"
)

// readableFileRepo is a FileRepo whose target is a public file (so target ACL passes).
func readableFileRepo(target domain.ID) *mocks.FileRepo {
	return &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: target, OwnerUID: "owner", Visibility: domain.VisibilityPublic}, nil
	}}
}

func TestCommentService_CreateTopLevel(t *testing.T) {
	target := idgen.NewID()
	var saved *domain.Comment
	comments := &mocks.CommentRepo{CreateFn: func(_ context.Context, c *domain.Comment) error { saved = c; return nil }}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	c, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, Content: "nice"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ParentID != nil || c.RootID != c.ID {
		t.Fatalf("top-level wiring wrong: %+v", c)
	}
	if saved == nil {
		t.Fatal("comment was not persisted")
	}
}

func TestCommentService_CreateReplyIncrementsParent(t *testing.T) {
	target := idgen.NewID()
	root := idgen.NewID()
	incremented := false
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: root, ParentID: nil, RootID: root, TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		CreateFn:        func(_ context.Context, _ *domain.Comment) error { return nil },
		IncReplyCountFn: func(_ context.Context, pid domain.ID) error { incremented = pid == root; return nil },
	}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	c, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &root, Content: "re"})
	if err != nil {
		t.Fatal(err)
	}
	if c.RootID != root || c.ParentID == nil || *c.ParentID != root {
		t.Fatalf("reply wiring wrong: %+v", c)
	}
	if !incremented {
		t.Fatal("parent reply count was not incremented")
	}
}

func TestCommentService_RejectsThirdLevel(t *testing.T) {
	target := idgen.NewID()
	root := idgen.NewID()
	midParent := idgen.NewID()
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.Comment, error) {
			// The requested parent is itself a reply (ParentID != nil) → depth 3.
			return &domain.Comment{ID: midParent, ParentID: &root, RootID: root, TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
	}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &midParent, Content: "too deep"})
	assertAppCode(t, err, 400)
	var ae *apperr.AppError
	stderrors.As(err, &ae)
	if ae.Code != "comment.nesting_too_deep" {
		t.Fatalf("code = %q, want comment.nesting_too_deep", ae.Code)
	}
}

func TestCommentService_TargetNotFound(t *testing.T) {
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewCommentService(&mocks.CommentRepo{}, files, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: idgen.NewID(), Content: "x"})
	assertAppCode(t, err, 404)
}

func TestCommentService_TargetForbidden(t *testing.T) {
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "someone", Visibility: domain.VisibilityPrivate}, nil
	}}
	svc := service.NewCommentService(&mocks.CommentRepo{}, files, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: idgen.NewID(), Content: "x"})
	assertAppCode(t, err, 403)
}

func TestCommentService_ParentBelongsToDifferentTarget(t *testing.T) {
	target := idgen.NewID()
	other := idgen.NewID()
	root := idgen.NewID()
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: root, ParentID: nil, RootID: root, TargetType: domain.TargetTypeFile, TargetID: other}, nil
	}}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &root, Content: "x"})
	assertAppCode(t, err, 400)
}

func TestCommentService_ListEnrichesAuthors(t *testing.T) {
	target := idgen.NewID()
	comments := &mocks.CommentRepo{ListByTargetFn: func(_ context.Context, _ domain.TargetType, _ domain.ID, _ domain.Page) ([]*domain.Comment, string, error) {
		return []*domain.Comment{{OwnerUID: "x"}, {OwnerUID: "y"}, {OwnerUID: "x"}}, "next", nil
	}}
	var requested []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		return map[string]sso.User{"x": {UID: "x"}, "y": {UID: "y"}}, nil
	}}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, dir)
	ctx := service.WithActor(context.Background(), "viewer")
	list, err := svc.List(ctx, domain.TargetTypeFile, target, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 {
		t.Fatalf("expected 2 deduped author uids, got %v", requested)
	}
	if list.NextCursor != "next" {
		t.Fatalf("cursor = %q", list.NextCursor)
	}
}

func TestCommentService_DeleteRequiresOwner(t *testing.T) {
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: id, OwnerUID: "author"}, nil
	}}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "not-author")
	err := svc.Delete(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/service/`
Expected: FAIL (undefined: service.NewCommentService).

- [ ] **Step 3: Implement `comment.go`**

Create `internal/service/comment.go`:

```go
package service

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type commentService struct {
	comments repository.CommentRepository
	files    repository.FileRepository
	messages repository.MessageRepository
	dir      UserDirectory
}

func NewCommentService(
	comments repository.CommentRepository,
	files repository.FileRepository,
	messages repository.MessageRepository,
	dir UserDirectory,
) CommentService {
	return &commentService{comments: comments, files: files, messages: messages, dir: dir}
}

// assertTargetReadable verifies the comment target exists and the actor may read
// it, reusing the same ACL as the file/message services.
func (s *commentService) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
	switch t {
	case domain.TargetTypeFile:
		f, err := s.files.Get(ctx, target)
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target file not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !canReadFile(actor, f) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	case domain.TargetTypeMessage:
		m, err := s.messages.Get(ctx, target)
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target message not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !canReadMessage(actor, m) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	default:
		return apperr.BadRequest("comment.invalid", "unsupported target type")
	}
}

func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Content == "" {
		return nil, apperr.BadRequest("comment.invalid", "content is required")
	}
	if err := s.assertTargetReadable(ctx, actor, in.TargetType, in.TargetID); err != nil {
		return nil, err
	}

	var parent *domain.Comment
	if in.ParentID != nil {
		p, err := s.comments.Get(ctx, *in.ParentID)
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.BadRequest("comment.invalid", "parent comment not found")
		}
		if err != nil {
			return nil, apperr.Internal("comment.get_failed", "failed to load parent comment").Wrap(err)
		}
		if p.TargetType != in.TargetType || p.TargetID != in.TargetID {
			return nil, apperr.BadRequest("comment.invalid", "parent belongs to a different target")
		}
		// Enforce the two-level nesting rule at the domain layer.
		if err := domain.ValidateNewComment(p); err != nil {
			return nil, apperr.BadRequest("comment.nesting_too_deep", "replies may not be nested more than two levels")
		}
		parent = p
	}

	c := domain.NewComment(idgen.NewID(), actor, in.TargetType, in.TargetID, parent, in.Content)
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	if err := s.comments.Create(ctx, c); err != nil {
		return nil, apperr.Internal("comment.create_failed", "failed to create comment").Wrap(err)
	}
	if parent != nil {
		// NOTE: Create + IncReplyCount are not wrapped in a transaction. If IncReplyCount
		// fails after the comment is persisted, the reply is saved but the parent's
		// reply_count is not incremented (a denormalized counter that can drift). A future
		// task should wrap both writes in a DB transaction.
		if err := s.comments.IncReplyCount(ctx, parent.ID); err != nil {
			return nil, apperr.Internal("comment.create_failed", "failed to update reply count").Wrap(err)
		}
	}
	return c, nil
}

func (s *commentService) List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertTargetReadable(ctx, actor, t, target); err != nil {
		return nil, err
	}
	cs, next, err := s.comments.ListByTarget(ctx, t, target, page)
	if err != nil {
		return nil, apperr.Internal("comment.list_failed", "failed to list comments").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, authorUIDs(cs))
	if err != nil {
		return nil, apperr.Internal("comment.enrich_failed", "failed to resolve authors").Wrap(err)
	}
	return &CommentList{Comments: cs, NextCursor: next, Users: users}, nil
}

func (s *commentService) Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	root, err := s.comments.Get(ctx, rootID)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("comment.not_found", "root comment not found")
	}
	if err != nil {
		return nil, apperr.Internal("comment.get_failed", "failed to load root comment").Wrap(err)
	}
	if err := s.assertTargetReadable(ctx, actor, root.TargetType, root.TargetID); err != nil {
		return nil, err
	}
	cs, next, err := s.comments.ListReplies(ctx, rootID, page)
	if err != nil {
		return nil, apperr.Internal("comment.list_failed", "failed to list replies").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, authorUIDs(cs))
	if err != nil {
		return nil, apperr.Internal("comment.enrich_failed", "failed to resolve authors").Wrap(err)
	}
	return &CommentList{Comments: cs, NextCursor: next, Users: users}, nil
}

func (s *commentService) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return apperr.Forbidden("comment.forbidden", "only the author can delete this comment")
	}
	if err := s.comments.SoftDelete(ctx, id); err != nil {
		return apperr.Internal("comment.delete_failed", "failed to delete comment").Wrap(err)
	}
	return nil
}

// authorUIDs returns the distinct owner_uids preserving first-seen order.
func authorUIDs(cs []*domain.Comment) []string {
	seen := make(map[string]struct{}, len(cs))
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if _, ok := seen[c.OwnerUID]; ok {
			continue
		}
		seen[c.OwnerUID] = struct{}{}
		out = append(out, c.OwnerUID)
	}
	return out
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/service/ -v`
Expected: PASS (all service tests across file/message/comment + internal helpers).

- [ ] **Step 5: Commit**

```bash
git add internal/service/comment.go internal/service/comment_test.go
git commit -m "feat(service): add comment service with target ACL and two-level nesting"
```

---

## Phase 7: HTTP Transport

> The HTTP layer is thin: middleware authenticates and injects the actor into the
> request context; handlers parse input, call the service, and render the result.
> All error rendering goes through `httpx.WriteError`, which understands `*AppError`.
> Handlers never touch repositories or storage directly.

### Task 24: `pkg/httpx` + middleware (request_id, recovery, access_log, cors, auth)

**Files:**
- Create: `internal/pkg/httpx/httpx.go`
- Create: `internal/middleware/request_id.go`
- Create: `internal/middleware/recovery.go`
- Create: `internal/middleware/access_log.go`
- Create: `internal/middleware/cors.go`
- Create: `internal/middleware/auth.go`
- Test: `internal/middleware/middleware_test.go` (package `middleware_test`)

- [ ] **Step 1: Add Gin dependency**

Run: `go get github.com/gin-gonic/gin@v1.10.0`

- [ ] **Step 2: Implement `httpx.go` (shared by middleware and handlers)**

Create `internal/pkg/httpx/httpx.go`:

```go
// Package httpx holds small HTTP rendering helpers shared by middleware and
// handlers: a uniform JSON envelope and AppError-aware error rendering.
package httpx

import (
	stderrors "errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteError renders err as JSON. An *AppError carries its own HTTP status and
// stable code; any other error becomes a 500 with a generic message so internal
// details are never leaked to clients.
func WriteError(c *gin.Context, err error) {
	rid := c.GetString("request_id")
	var ae *apperr.AppError
	if stderrors.As(err, &ae) {
		c.JSON(ae.HTTP, errorBody{Error: errorDetail{Code: ae.Code, Message: ae.Message, RequestID: rid}})
		return
	}
	c.JSON(http.StatusInternalServerError, errorBody{Error: errorDetail{
		Code: "internal.unexpected", Message: "internal server error", RequestID: rid,
	}})
}

// OK / Created / NoContent wrap a successful payload in a {"data": ...} envelope.
func OK(c *gin.Context, data any)      { c.JSON(http.StatusOK, gin.H{"data": data}) }
func Created(c *gin.Context, data any) { c.JSON(http.StatusCreated, gin.H{"data": data}) }
func NoContent(c *gin.Context)         { c.Status(http.StatusNoContent) }

// PageFromQuery reads ?limit= and ?cursor= into an (unnormalized) domain.Page;
// the repository clamps Limit via Page.Normalize.
func PageFromQuery(c *gin.Context) domain.Page {
	p := domain.Page{Cursor: c.Query("cursor")}
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			p.Limit = n
		}
	}
	return p
}
```

- [ ] **Step 3: Write failing middleware test**

Create `internal/middleware/middleware_test.go`:

```go
package middleware_test

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/service"
)

type fakeValidator struct {
	uid string
	err error
}

func (f fakeValidator) ValidateToken(context.Context, string) (string, error) {
	return f.uid, f.err
}

func newEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

func TestRequestID_setsHeader(t *testing.T) {
	e := newEngine()
	e.Use(middleware.RequestID())
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Header().Get(middleware.RequestIDHeader) == "" {
		t.Fatal("expected a request id response header")
	}
}

func TestRecovery_returns500(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Recovery(zap.NewNop()))
	e.GET("/boom", func(*gin.Context) { panic("kaboom") })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestAuth_missingToken401(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{uid: "u1"}))
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/p", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAuth_invalidToken401(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{err: stderrors.New("bad")}))
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer xyz")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAuth_injectsActor(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{uid: "u1"}))
	var got string
	e.GET("/p", func(c *gin.Context) {
		got, _ = service.ActorFrom(c.Request.Context())
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer xyz")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got != "u1" {
		t.Fatalf("status=%d actor=%q", w.Code, got)
	}
}
```

- [ ] **Step 4: Verify test fails**

Run: `go test ./internal/middleware/`
Expected: FAIL (undefined: middleware.RequestID, Recovery, Auth, RequestIDHeader).

- [ ] **Step 5: Implement the five middleware files**

Create `internal/middleware/request_id.go`:

```go
package middleware

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

// RequestIDHeader is the canonical header carrying the per-request correlation id.
const RequestIDHeader = "X-Request-ID"

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID reuses an inbound X-Request-ID or mints a new one, exposing it on the
// response header, the gin context ("request_id"), and the request context.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(RequestIDHeader)
		if rid == "" {
			rid = idgen.NewID().String()
		}
		c.Writer.Header().Set(RequestIDHeader, rid)
		c.Set("request_id", rid)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDKey, rid))
		c.Next()
	}
}

// RequestIDFrom returns the request id stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	rid, _ := ctx.Value(requestIDKey).(string)
	return rid
}
```

Create `internal/middleware/recovery.go`:

```go
package middleware

import (
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

// Recovery converts a panic in any downstream handler into a logged 500 response.
func Recovery(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic recovered",
					zap.Any("panic", r),
					zap.String("path", c.FullPath()),
					zap.ByteString("stack", debug.Stack()),
				)
				httpx.WriteError(c, apperr.Internal("internal.panic", "internal server error"))
				c.Abort()
			}
		}()
		c.Next()
	}
}
```

Create `internal/middleware/access_log.go`:

```go
package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AccessLog emits one structured log line per request after it completes.
func AccessLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("http_request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.FullPath()),          // route template, "" for unmatched (404) routes
			zap.String("uri", c.Request.URL.Path),      // raw path, always present
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("request_id", c.GetString("request_id")),
			zap.String("actor", c.GetString("actor_uid")),
		)
	}
}
```

Create `internal/middleware/cors.go`:

```go
package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS applies permissive defaults when origins is empty, otherwise reflects only
// allow-listed origins. Allow-Methods/Headers are emitted only for accepted origins;
// a disallowed-origin preflight is rejected with 403, an accepted one with 204.
func CORS(origins []string) gin.HandlerFunc {
	allowAll := len(origins) == 0
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			_, originAllowed := allowed[origin]
			switch {
			case allowAll:
				c.Header("Access-Control-Allow-Origin", "*")
			case originAllowed:
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			default:
				// disallowed origin: reject preflight, let real requests through sans CORS headers
				if c.Request.Method == http.MethodOptions {
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
			}
			if allowAll || originAllowed {
				c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,"+RequestIDHeader)
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
```

Create `internal/middleware/auth.go`:

```go
package middleware

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

// TokenValidator is the SSO capability the auth middleware needs. *sso.Client
// satisfies it.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
}

// Auth extracts the Bearer token, validates it via SSO, and injects the resulting
// uid as the request actor (both on the gin context for logging and the request
// context for the service layer).
func Auth(v TokenValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.missing_token", "missing bearer token"))
			c.Abort()
			return
		}
		uid, err := v.ValidateToken(c.Request.Context(), token)
		if err != nil || uid == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.invalid_token", "invalid or expired token"))
			c.Abort()
			return
		}
		c.Set("actor_uid", uid)
		c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), uid))
		c.Next()
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}
```

- [ ] **Step 6: Verify tests pass**

Run: `go test ./internal/middleware/ ./internal/pkg/httpx/ -v`
Expected: PASS (5 middleware tests; httpx compiles).

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/pkg/httpx/ internal/middleware/
git commit -m "feat(http): add httpx helpers and request_id/recovery/access_log/cors/auth middleware"
```

---

### Task 25: HTTP health + router skeleton

**Files:**
- Create: `internal/handler/http/router.go`
- Create: `internal/handler/http/health.go`
- Test: `internal/handler/http/router_test.go` (package `http_test`)

- [ ] **Step 1: Write failing test**

Create `internal/handler/http/router_test.go`:

```go
package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	httpapi "github.com/aero/aero-id/internal/handler/http"
)

type okValidator struct{}

func (okValidator) ValidateToken(context.Context, string) (string, error) { return "u1", nil }

// pingRegistrar registers a protected /v1/ping route to exercise the auth chain.
type pingRegistrar struct{}

func (pingRegistrar) RegisterRoutes(r httpapi.Routes) {
	r.Auth.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
}

func TestRouter_HealthLive(t *testing.T) {
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler(nil))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz = %d", w.Code)
	}
}

func TestRouter_ReadyFails503(t *testing.T) {
	bad := httpapi.NewHealthHandler(func(context.Context) error { return context.DeadlineExceeded })
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, bad)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", w.Code)
	}
}

func TestRouter_AuthRequiredOnV1(t *testing.T) {
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler(nil), pingRegistrar{})

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no-token /v1/ping = %d, want 401", w.Code)
	}

	w2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer t")
	e.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("with-token /v1/ping = %d, want 200", w2.Code)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/handler/http/`
Expected: FAIL (undefined: NewRouter, RouterConfig, Routes, NewHealthHandler).

- [ ] **Step 3: Implement `health.go`**

Create `internal/handler/http/health.go`:

```go
// Package http hosts aero-id's REST transport: a router plus thin handlers that
// delegate to the service layer.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler serves liveness and readiness probes.
type HealthHandler struct {
	ready func(ctx context.Context) error
}

// NewHealthHandler wires a readiness checker (typically a DB ping). A nil checker
// makes readiness always succeed.
func NewHealthHandler(ready func(ctx context.Context) error) *HealthHandler {
	return &HealthHandler{ready: ready}
}

// Live always reports OK as long as the process is serving.
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready reports 503 when the readiness checker fails (e.g. DB unreachable).
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if h.ready != nil {
		if err := h.ready(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
```

- [ ] **Step 4: Implement `router.go`**

Create `internal/handler/http/router.go`:

```go
package http

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/middleware"
)

// RouteRegistrar lets each feature handler attach its own routes without the
// router importing the concrete handler types — keeps the package decoupled and
// lets handlers be added task-by-task.
type RouteRegistrar interface {
	RegisterRoutes(r Routes)
}

// Routes carries the groups handed to each registrar.
type Routes struct {
	Auth   *gin.RouterGroup // mounted at /v1, requires a valid SSO token
	Public *gin.RouterGroup // anonymous, e.g. signature-verified file downloads
}

// RouterConfig holds cross-cutting router settings.
type RouterConfig struct {
	CORSOrigins []string
}

// NewRouter assembles the gin engine: global middleware, health probes, and the
// authenticated /v1 group, then lets every registrar attach its routes.
func NewRouter(
	log *zap.Logger,
	cfg RouterConfig,
	validator middleware.TokenValidator,
	health *HealthHandler,
	registrars ...RouteRegistrar,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.Use(middleware.RequestID())
	e.Use(middleware.Recovery(log))
	e.Use(middleware.AccessLog(log))
	e.Use(middleware.CORS(cfg.CORSOrigins))

	e.GET("/healthz", health.Live)
	e.GET("/readyz", health.Ready)

	routes := Routes{
		Public: e.Group(""),
		Auth:   e.Group("/v1", middleware.Auth(validator)),
	}
	for _, r := range registrars {
		r.RegisterRoutes(routes)
	}
	return e
}
```

- [ ] **Step 5: Verify tests pass**

Run: `go test ./internal/handler/http/ -v`
Expected: PASS (3 router tests).

- [ ] **Step 6: Commit**

```bash
git add internal/handler/http/router.go internal/handler/http/health.go internal/handler/http/router_test.go
git commit -m "feat(http): add router skeleton with health probes and registrar wiring"
```

---

### Task 26: HTTP file handler + tests

**Files:**
- Create: `internal/handler/http/file.go`
- Test: `internal/handler/http/file_test.go` (package `http_test`)

- [ ] **Step 1: Write failing test**

Create `internal/handler/http/file_test.go`:

```go
package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// serveWithActor builds an engine that injects a fixed actor (simulating the auth
// middleware) then registers the handler. Shared by all handler tests here.
func serveWithActor(h httpapi.RouteRegistrar, actor string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Set("actor_uid", actor)
		c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), actor))
		c.Next()
	})
	h.RegisterRoutes(httpapi.Routes{Auth: e.Group("/v1"), Public: e.Group("")})
	return e
}

type fakeFileService struct {
	createFn      func(context.Context, service.CreateFileInput) (*domain.File, error)
	getFn         func(context.Context, domain.ID) (*domain.File, error)
	listFn        func(context.Context, string, domain.Page) (*service.FileList, error)
	deleteFn      func(context.Context, domain.ID) error
	downloadURLFn func(context.Context, domain.ID, time.Duration) (string, error)
}

func (f *fakeFileService) Create(ctx context.Context, in service.CreateFileInput) (*domain.File, error) {
	return f.createFn(ctx, in)
}
func (f *fakeFileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return f.getFn(ctx, id)
}
func (f *fakeFileService) List(ctx context.Context, owner string, p domain.Page) (*service.FileList, error) {
	return f.listFn(ctx, owner, p)
}
func (f *fakeFileService) Delete(ctx context.Context, id domain.ID) error { return f.deleteFn(ctx, id) }
func (f *fakeFileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
	return f.downloadURLFn(ctx, id, ttl)
}

func TestFileHandler_Upload(t *testing.T) {
	svc := &fakeFileService{createFn: func(_ context.Context, in service.CreateFileInput) (*domain.File, error) {
		return &domain.File{ID: idgen.NewID(), OwnerUID: "u1", Name: in.Name, Size: 5}, nil
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "a.txt")
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.ID == "" || resp.Data.Name != "a.txt" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestFileHandler_GetNotFoundMapsTo404(t *testing.T) {
	svc := &fakeFileService{getFn: func(context.Context, domain.ID) (*domain.File, error) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/files/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestFileHandler_InvalidID400(t *testing.T) {
	e := serveWithActor(httpapi.NewFileHandler(&fakeFileService{}, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/files/not-a-uuid", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestFileHandler_DownloadURL(t *testing.T) {
	svc := &fakeFileService{downloadURLFn: func(context.Context, domain.ID, time.Duration) (string, error) {
		return "https://signed/x", nil
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/files/"+idgen.NewID().String()+"/url", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestFileHandler_Delete204(t *testing.T) {
	svc := &fakeFileService{deleteFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/files/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/handler/http/`
Expected: FAIL (undefined: httpapi.NewFileHandler).

- [ ] **Step 3: Implement `file.go`**

Create `internal/handler/http/file.go`:

```go
package http

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// SignedDownloader is the subset of the local storage backend needed to serve
// signature-verified anonymous downloads. *storage.Local satisfies it; it is nil
// for the S3 backend (whose presigned URLs point directly at the object store).
type SignedDownloader interface {
	VerifyPresignedURL(rawURL string, now time.Time) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

type FileHandler struct {
	files      service.FileService
	downloader SignedDownloader
	urlTTL     time.Duration
}

func NewFileHandler(files service.FileService, downloader SignedDownloader, urlTTL time.Duration) *FileHandler {
	return &FileHandler{files: files, downloader: downloader, urlTTL: urlTTL}
}

func (h *FileHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/files", h.Upload)
	r.Auth.GET("/files", h.List)
	r.Auth.GET("/files/:id", h.Get)
	r.Auth.GET("/files/:id/url", h.DownloadURL)
	r.Auth.DELETE("/files/:id", h.Delete)
	if h.downloader != nil {
		r.Public.GET("/files/dl", h.Download)
	}
}

func (h *FileHandler) Upload(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "missing 'file' form field"))
		return
	}
	src, err := fh.Open()
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "cannot read uploaded file"))
		return
	}
	defer func() { _ = src.Close() }()

	vis := domain.VisibilityPrivate
	if c.PostForm("visibility") == "1" {
		vis = domain.VisibilityPublic
	}
	name := c.PostForm("name")
	if name == "" {
		name = fh.Filename
	}
	created, err := h.files.Create(c.Request.Context(), service.CreateFileInput{
		Name:       name,
		MimeType:   fh.Header.Get("Content-Type"),
		Visibility: vis,
		Reader:     src,
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, toFileResponse(created))
}

func (h *FileHandler) List(c *gin.Context) {
	list, err := h.files.List(c.Request.Context(), c.Query("owner_uid"), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]fileResponse, 0, len(list.Files))
	for _, f := range list.Files {
		items = append(items, toFileResponse(f))
	}
	httpx.OK(c, gin.H{"files": items, "next_cursor": list.NextCursor})
}

func (h *FileHandler) Get(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	f, err := h.files.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, toFileResponse(f))
}

func (h *FileHandler) DownloadURL(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	url, err := h.files.DownloadURL(c.Request.Context(), id, h.urlTTL)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"url": url})
}

func (h *FileHandler) Delete(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.files.Delete(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

// Download serves a local-storage object after verifying the presigned signature.
// It is anonymous (no SSO token) — the HMAC signature is the authorization.
func (h *FileHandler) Download(c *gin.Context) {
	if err := h.downloader.VerifyPresignedURL(c.Request.URL.String(), time.Now()); err != nil {
		httpx.WriteError(c, apperr.Forbidden("file.bad_signature", "invalid or expired download link"))
		return
	}
	rc, err := h.downloader.Get(c.Request.Context(), c.Query("key"))
	if err != nil {
		httpx.WriteError(c, apperr.NotFound("file.not_found", "file not found"))
		return
	}
	defer func() { _ = rc.Close() }()
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, rc)
}

// ---- presentation DTO + shared path helper ----

type fileResponse struct {
	ID          string         `json:"id"`
	OwnerUID    string         `json:"owner_uid"`
	Name        string         `json:"name"`
	Size        int64          `json:"size"`
	MimeType    string         `json:"mime_type"`
	Visibility  int16          `json:"visibility"`
	StorageType string         `json:"storage_type"`
	Meta        map[string]any `json:"meta,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func toFileResponse(f *domain.File) fileResponse {
	return fileResponse{
		ID:          f.ID.String(),
		OwnerUID:    f.OwnerUID,
		Name:        f.Name,
		Size:        f.Size,
		MimeType:    f.MimeType,
		Visibility:  int16(f.Visibility),
		StorageType: f.StorageType,
		Meta:        f.Meta,
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
	}
}

// parseID parses a path param as a domain ID, returning a BadRequest AppError on
// failure. Shared by the message and comment handlers (same package).
func parseID(c *gin.Context, name string) (domain.ID, error) {
	id, err := idgen.Parse(c.Param(name))
	if err != nil {
		return domain.ID{}, apperr.BadRequest("request.invalid_id", "invalid id in path")
	}
	return id, nil
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/handler/http/ -run TestFileHandler -v`
Expected: PASS (5 `TestFileHandler_*`).

- [ ] **Step 5: Commit**

```bash
git add internal/handler/http/file.go internal/handler/http/file_test.go
git commit -m "feat(http): add file handler (upload/list/get/presign/delete + signed download)"
```

---

### Task 27: HTTP message handler + tests

**Files:**
- Create: `internal/handler/http/message.go`
- Test: `internal/handler/http/message_test.go` (package `http_test`)

- [ ] **Step 1: Write failing test**

Create `internal/handler/http/message_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

type fakeMessageService struct {
	sendFn         func(context.Context, service.SendMessageInput) (*domain.Message, error)
	getFn          func(context.Context, domain.ID) (*domain.Message, error)
	inboxFn        func(context.Context, domain.Page, bool) (*service.MessageList, error)
	conversationFn func(context.Context, string, domain.Page) (*service.MessageList, error)
	markReadFn     func(context.Context, domain.ID) error
}

func (f *fakeMessageService) Send(ctx context.Context, in service.SendMessageInput) (*domain.Message, error) {
	return f.sendFn(ctx, in)
}
func (f *fakeMessageService) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	return f.getFn(ctx, id)
}
func (f *fakeMessageService) Inbox(ctx context.Context, p domain.Page, unread bool) (*service.MessageList, error) {
	return f.inboxFn(ctx, p, unread)
}
func (f *fakeMessageService) Conversation(ctx context.Context, with string, p domain.Page) (*service.MessageList, error) {
	return f.conversationFn(ctx, with, p)
}
func (f *fakeMessageService) MarkRead(ctx context.Context, id domain.ID) error {
	return f.markReadFn(ctx, id)
}

func TestMessageHandler_Send(t *testing.T) {
	svc := &fakeMessageService{sendFn: func(_ context.Context, in service.SendMessageInput) (*domain.Message, error) {
		if in.ToUID != "b" || in.Content != "hi" {
			t.Errorf("input not bound: %+v", in)
		}
		return &domain.Message{ID: idgen.NewID(), FromUID: "a", ToUID: in.ToUID, Content: in.Content}, nil
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"to_uid":"b","content":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
}

func TestMessageHandler_InboxIncludesUsers(t *testing.T) {
	svc := &fakeMessageService{inboxFn: func(context.Context, domain.Page, bool) (*service.MessageList, error) {
		return &service.MessageList{
			Messages:   []*domain.Message{{ID: idgen.NewID(), FromUID: "x", ToUID: "a"}},
			NextCursor: "next",
			Users:      map[string]sso.User{"x": {UID: "x", Nickname: "ex"}},
		}, nil
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/messages?unread=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data struct {
			Users      map[string]map[string]any `json:"users"`
			NextCursor string                    `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.NextCursor != "next" || resp.Data.Users["x"]["nickname"] != "ex" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestMessageHandler_MarkRead204(t *testing.T) {
	svc := &fakeMessageService{markReadFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages/"+idgen.NewID().String()+"/read", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestMessageHandler_GetForbiddenMapsTo403(t *testing.T) {
	svc := &fakeMessageService{getFn: func(context.Context, domain.ID) (*domain.Message, error) {
		return nil, apperr.Forbidden("message.forbidden", "nope")
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/messages/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/handler/http/`
Expected: FAIL (undefined: httpapi.NewMessageHandler).

- [ ] **Step 3: Implement `message.go`**

Create `internal/handler/http/message.go`:

```go
package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

type MessageHandler struct {
	messages service.MessageService
}

func NewMessageHandler(messages service.MessageService) *MessageHandler {
	return &MessageHandler{messages: messages}
}

func (h *MessageHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/messages", h.Send)
	r.Auth.GET("/messages", h.Inbox)
	r.Auth.GET("/messages/:id", h.Get)
	r.Auth.POST("/messages/:id/read", h.MarkRead)
	r.Auth.GET("/conversations/:uid", h.Conversation)
}

type sendMessageRequest struct {
	ToUID   string         `json:"to_uid"`
	Kind    int16          `json:"kind"`
	Content string         `json:"content"`
	FileID  string         `json:"file_id"`
	Meta    map[string]any `json:"meta"`
}

func (h *MessageHandler) Send(c *gin.Context) {
	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("message.invalid", "invalid request body"))
		return
	}
	in := service.SendMessageInput{
		ToUID:   req.ToUID,
		Kind:    domain.MessageKind(req.Kind),
		Content: req.Content,
		Meta:    req.Meta,
	}
	if req.FileID != "" {
		fid, err := idgen.Parse(req.FileID)
		if err != nil {
			httpx.WriteError(c, apperr.BadRequest("message.invalid", "invalid file_id"))
			return
		}
		in.FileID = &fid
	}
	m, err := h.messages.Send(c.Request.Context(), in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, toMessageResponse(m))
}

func (h *MessageHandler) Get(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	m, err := h.messages.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, toMessageResponse(m))
}

func (h *MessageHandler) Inbox(c *gin.Context) {
	list, err := h.messages.Inbox(c.Request.Context(), httpx.PageFromQuery(c), c.Query("unread") == "true")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderMessageList(c, list)
}

func (h *MessageHandler) Conversation(c *gin.Context) {
	list, err := h.messages.Conversation(c.Request.Context(), c.Param("uid"), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderMessageList(c, list)
}

func (h *MessageHandler) MarkRead(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.messages.MarkRead(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func renderMessageList(c *gin.Context, list *service.MessageList) {
	items := make([]messageResponse, 0, len(list.Messages))
	for _, m := range list.Messages {
		items = append(items, toMessageResponse(m))
	}
	httpx.OK(c, gin.H{
		"messages":    items,
		"users":       toUserMap(list.Users),
		"next_cursor": list.NextCursor,
	})
}

// ---- presentation DTOs (userResponse/toUserMap reused by the comment handler) ----

type messageResponse struct {
	ID        string         `json:"id"`
	FromUID   string         `json:"from_uid"`
	ToUID     string         `json:"to_uid"`
	Kind      int16          `json:"kind"`
	Content   string         `json:"content"`
	FileID    *string        `json:"file_id,omitempty"`
	ReadAt    *time.Time     `json:"read_at,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func toMessageResponse(m *domain.Message) messageResponse {
	resp := messageResponse{
		ID:        m.ID.String(),
		FromUID:   m.FromUID,
		ToUID:     m.ToUID,
		Kind:      int16(m.Kind),
		Content:   m.Content,
		ReadAt:    m.ReadAt,
		Meta:      m.Meta,
		CreatedAt: m.CreatedAt,
	}
	if m.FileID != nil {
		s := m.FileID.String()
		resp.FileID = &s
	}
	return resp
}

type userResponse struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

func toUserMap(users map[string]sso.User) map[string]userResponse {
	out := make(map[string]userResponse, len(users))
	for uid, u := range users {
		out[uid] = userResponse{UID: u.UID, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/handler/http/ -run TestMessageHandler -v`
Expected: PASS (4 `TestMessageHandler_*`).

- [ ] **Step 5: Commit**

```bash
git add internal/handler/http/message.go internal/handler/http/message_test.go
git commit -m "feat(http): add message handler (send/inbox/conversation/get/mark-read)"
```

---

### Task 28: HTTP comment handler + tests

**Files:**
- Create: `internal/handler/http/comment.go`
- Test: `internal/handler/http/comment_test.go` (package `http_test`)

- [ ] **Step 1: Write failing test**

Create `internal/handler/http/comment_test.go`:

```go
package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type fakeCommentService struct {
	createFn  func(context.Context, service.CreateCommentInput) (*domain.Comment, error)
	listFn    func(context.Context, domain.TargetType, domain.ID, domain.Page) (*service.CommentList, error)
	repliesFn func(context.Context, domain.ID, domain.Page) (*service.CommentList, error)
	deleteFn  func(context.Context, domain.ID) error
}

func (f *fakeCommentService) Create(ctx context.Context, in service.CreateCommentInput) (*domain.Comment, error) {
	return f.createFn(ctx, in)
}
func (f *fakeCommentService) List(ctx context.Context, t domain.TargetType, id domain.ID, p domain.Page) (*service.CommentList, error) {
	return f.listFn(ctx, t, id, p)
}
func (f *fakeCommentService) Replies(ctx context.Context, root domain.ID, p domain.Page) (*service.CommentList, error) {
	return f.repliesFn(ctx, root, p)
}
func (f *fakeCommentService) Delete(ctx context.Context, id domain.ID) error { return f.deleteFn(ctx, id) }

func TestCommentHandler_Create(t *testing.T) {
	target := idgen.NewID()
	svc := &fakeCommentService{createFn: func(_ context.Context, in service.CreateCommentInput) (*domain.Comment, error) {
		if in.TargetType != domain.TargetTypeFile || in.TargetID != target {
			t.Errorf("input not bound: %+v", in)
		}
		return &domain.Comment{ID: idgen.NewID(), OwnerUID: "a", TargetType: in.TargetType, TargetID: in.TargetID, Content: in.Content}, nil
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	bodyJSON := `{"target_type":"file","target_id":"` + target.String() + `","content":"nice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/comments", strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_CreateNestingTooDeepMapsTo400(t *testing.T) {
	svc := &fakeCommentService{createFn: func(context.Context, service.CreateCommentInput) (*domain.Comment, error) {
		return nil, apperr.BadRequest("comment.nesting_too_deep", "too deep")
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	body := `{"target_type":"file","target_id":"` + idgen.NewID().String() + `","parent_id":"` + idgen.NewID().String() + `","content":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/comments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCommentHandler_ListRequiresTargetID(t *testing.T) {
	e := serveWithActor(httpapi.NewCommentHandler(&fakeCommentService{}), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments?target_type=file&target_id=bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCommentHandler_List200(t *testing.T) {
	target := idgen.NewID()
	svc := &fakeCommentService{listFn: func(context.Context, domain.TargetType, domain.ID, domain.Page) (*service.CommentList, error) {
		return &service.CommentList{Comments: []*domain.Comment{{ID: idgen.NewID(), OwnerUID: "a"}}, NextCursor: ""}, nil
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments?target_type=file&target_id="+target.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_Delete204(t *testing.T) {
	svc := &fakeCommentService{deleteFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/comments/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/handler/http/`
Expected: FAIL (undefined: httpapi.NewCommentHandler).

- [ ] **Step 3: Implement `comment.go`**

Create `internal/handler/http/comment.go`:

```go
package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type CommentHandler struct {
	comments service.CommentService
}

func NewCommentHandler(comments service.CommentService) *CommentHandler {
	return &CommentHandler{comments: comments}
}

func (h *CommentHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/comments", h.Create)
	r.Auth.GET("/comments", h.List)
	r.Auth.GET("/comments/:id/replies", h.Replies)
	r.Auth.DELETE("/comments/:id", h.Delete)
}

type createCommentRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	ParentID   string `json:"parent_id"`
	Content    string `json:"content"`
}

func (h *CommentHandler) Create(c *gin.Context) {
	var req createCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid request body"))
		return
	}
	targetID, err := idgen.Parse(req.TargetID)
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid target_id"))
		return
	}
	in := service.CreateCommentInput{
		TargetType: domain.TargetType(req.TargetType),
		TargetID:   targetID,
		Content:    req.Content,
	}
	if req.ParentID != "" {
		pid, err := idgen.Parse(req.ParentID)
		if err != nil {
			httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid parent_id"))
			return
		}
		in.ParentID = &pid
	}
	created, err := h.comments.Create(c.Request.Context(), in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, toCommentResponse(created))
}

func (h *CommentHandler) List(c *gin.Context) {
	targetID, err := idgen.Parse(c.Query("target_id"))
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("comment.invalid", "invalid or missing target_id"))
		return
	}
	list, err := h.comments.List(c.Request.Context(), domain.TargetType(c.Query("target_type")), targetID, httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderCommentList(c, list)
}

func (h *CommentHandler) Replies(c *gin.Context) {
	rootID, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	list, err := h.comments.Replies(c.Request.Context(), rootID, httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderCommentList(c, list)
}

func (h *CommentHandler) Delete(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.comments.Delete(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func renderCommentList(c *gin.Context, list *service.CommentList) {
	items := make([]commentResponse, 0, len(list.Comments))
	for _, cm := range list.Comments {
		items = append(items, toCommentResponse(cm))
	}
	httpx.OK(c, gin.H{
		"comments":    items,
		"users":       toUserMap(list.Users),
		"next_cursor": list.NextCursor,
	})
}

type commentResponse struct {
	ID         string    `json:"id"`
	OwnerUID   string    `json:"owner_uid"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	ParentID   *string   `json:"parent_id,omitempty"`
	RootID     string    `json:"root_id"`
	Content    string    `json:"content"`
	ReplyCount int       `json:"reply_count"`
	Status     int16     `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

func toCommentResponse(c *domain.Comment) commentResponse {
	resp := commentResponse{
		ID:         c.ID.String(),
		OwnerUID:   c.OwnerUID,
		TargetType: string(c.TargetType),
		TargetID:   c.TargetID.String(),
		RootID:     c.RootID.String(),
		Content:    c.Content,
		ReplyCount: c.ReplyCount,
		Status:     int16(c.Status),
		CreatedAt:  c.CreatedAt,
	}
	if c.ParentID != nil {
		s := c.ParentID.String()
		resp.ParentID = &s
	}
	return resp
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/handler/http/ -v`
Expected: PASS (all HTTP handler + router tests).

- [ ] **Step 5: Commit**

```bash
git add internal/handler/http/comment.go internal/handler/http/comment_test.go
git commit -m "feat(http): add comment handler (create/list/replies/delete)"
```

---

## Phase 8: gRPC Transport

> gRPC is the internal API. It shares the exact same service layer as HTTP. An
> auth interceptor mirrors the HTTP auth middleware (token from `authorization`
> metadata → SSO validate → inject actor). Errors map through `grpcError`, which
> turns `*AppError` into a `status.Status` using the AppError's gRPC code. File
> *upload* is HTTP-only (multipart); gRPC exposes file metadata operations.

### Task 29: aero_id proto + generation

**Files:**
- Create: `api/proto/aero_id/v1/aero_id.proto`
- Modify: `Makefile` (extend the `proto` target)
- Generated: `internal/handler/grpc/aeropb/*.go`

- [ ] **Step 1: Write the aero_id proto**

Create `api/proto/aero_id/v1/aero_id.proto`:

```proto
syntax = "proto3";

package aero_id.v1;

option go_package = "github.com/aero/aero-id/internal/handler/grpc/aeropb";

import "google/protobuf/timestamp.proto";

// ---- shared ----
message User {
  string uid      = 1;
  string nickname = 2;
  string avatar   = 3;
}

message Page {
  int32  limit  = 1;
  string cursor = 2;
}

// ---- files ----
message File {
  string id                              = 1;
  string owner_uid                       = 2;
  string name                            = 3;
  int64  size                            = 4;
  string mime_type                       = 5;
  int32  visibility                      = 6;
  string storage_type                    = 7;
  google.protobuf.Timestamp created_at   = 8;
  google.protobuf.Timestamp updated_at   = 9;
}

message GetFileRequest { string id = 1; }
message GetFileResponse { File file = 1; }
message ListFilesRequest { string owner_uid = 1; Page page = 2; }
message ListFilesResponse { repeated File files = 1; string next_cursor = 2; }
message DeleteFileRequest { string id = 1; }
message DeleteFileResponse {}
message DownloadURLRequest { string id = 1; int64 ttl_seconds = 2; }
message DownloadURLResponse { string url = 1; }

service FileService {
  rpc GetFile(GetFileRequest) returns (GetFileResponse);
  rpc ListFiles(ListFilesRequest) returns (ListFilesResponse);
  rpc DeleteFile(DeleteFileRequest) returns (DeleteFileResponse);
  rpc GetDownloadURL(DownloadURLRequest) returns (DownloadURLResponse);
}

// ---- messages ----
message Message {
  string id                            = 1;
  string from_uid                      = 2;
  string to_uid                        = 3;
  int32  kind                          = 4;
  string content                       = 5;
  string file_id                       = 6;
  google.protobuf.Timestamp read_at    = 7;
  google.protobuf.Timestamp created_at = 8;
}

message SendMessageRequest { string to_uid = 1; int32 kind = 2; string content = 3; string file_id = 4; }
message SendMessageResponse { Message message = 1; }
message GetMessageRequest { string id = 1; }
message GetMessageResponse { Message message = 1; }
message InboxRequest { Page page = 1; bool unread_only = 2; }
message InboxResponse { repeated Message messages = 1; map<string, User> users = 2; string next_cursor = 3; }
message ConversationRequest { string with_uid = 1; Page page = 2; }
message ConversationResponse { repeated Message messages = 1; map<string, User> users = 2; string next_cursor = 3; }
message MarkReadRequest { string id = 1; }
message MarkReadResponse {}

service MessageService {
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
  rpc GetMessage(GetMessageRequest) returns (GetMessageResponse);
  rpc Inbox(InboxRequest) returns (InboxResponse);
  rpc Conversation(ConversationRequest) returns (ConversationResponse);
  rpc MarkRead(MarkReadRequest) returns (MarkReadResponse);
}

// ---- comments ----
message Comment {
  string id                            = 1;
  string owner_uid                     = 2;
  string target_type                   = 3;
  string target_id                     = 4;
  string parent_id                     = 5;
  string root_id                       = 6;
  string content                       = 7;
  int32  reply_count                   = 8;
  int32  status                        = 9;
  google.protobuf.Timestamp created_at = 10;
}

message CreateCommentRequest { string target_type = 1; string target_id = 2; string parent_id = 3; string content = 4; }
message CreateCommentResponse { Comment comment = 1; }
message ListCommentsRequest { string target_type = 1; string target_id = 2; Page page = 3; }
message ListCommentsResponse { repeated Comment comments = 1; map<string, User> users = 2; string next_cursor = 3; }
message RepliesRequest { string root_id = 1; Page page = 2; }
message RepliesResponse { repeated Comment comments = 1; map<string, User> users = 2; string next_cursor = 3; }
message DeleteCommentRequest { string id = 1; }
message DeleteCommentResponse {}

service CommentService {
  rpc CreateComment(CreateCommentRequest) returns (CreateCommentResponse);
  rpc ListComments(ListCommentsRequest) returns (ListCommentsResponse);
  rpc Replies(RepliesRequest) returns (RepliesResponse);
  rpc DeleteComment(DeleteCommentRequest) returns (DeleteCommentResponse);
}
```

- [ ] **Step 2: Extend the `proto` Makefile target**

Replace the `proto` target in `Makefile` (added in Task 16) with:

```makefile
.PHONY: proto
proto:
	mkdir -p internal/sso/ssopb internal/handler/grpc/aeropb
	protoc \
	  --proto_path=api/proto \
	  --go_out=. --go_opt=module=github.com/aero/aero-id \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/aero/aero-id \
	  api/proto/sso/v1/sso.proto \
	  api/proto/aero_id/v1/aero_id.proto
```

- [ ] **Step 3: Generate**

Run: `make proto`
Expected: creates `internal/handler/grpc/aeropb/aero_id.pb.go` and `aero_id_grpc.pb.go`.

- [ ] **Step 4: Verify generated code compiles**

Run: `go build ./internal/handler/grpc/...`
Expected: success (package `aeropb` builds; the `grpc` package is still empty — that's fine).

- [ ] **Step 5: Commit**

```bash
git add api/proto/aero_id/ Makefile internal/handler/grpc/aeropb/
git commit -m "feat(grpc): define aero_id gRPC proto and code generation"
```

---

### Task 30: gRPC server + auth/recovery interceptors

**Files:**
- Create: `internal/handler/grpc/server.go`
- Test: `internal/handler/grpc/server_test.go` (package `grpc`, internal — exercises unexported `grpcError`)

> Note: this file lives in `package grpc` and imports `google.golang.org/grpc`.
> That is legal — a package never refers to itself by name, so the import binding
> `grpc` does not collide. (Same pattern as `package http` importing `net/http`.)

- [ ] **Step 1: Write failing test**

Create `internal/handler/grpc/server_test.go`:

```go
package grpc

import (
	"context"
	stderrors "errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

type fakeValidator struct {
	uid string
	err error
}

func (f fakeValidator) ValidateToken(context.Context, string) (string, error) { return f.uid, f.err }

func TestAuthInterceptor_missingToken(t *testing.T) {
	intc := AuthInterceptor(fakeValidator{uid: "u"})
	_, err := intc(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x/Y"},
		func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestAuthInterceptor_injectsActor(t *testing.T) {
	intc := AuthInterceptor(fakeValidator{uid: "u"})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer abc"))
	var got string
	_, err := intc(ctx, nil, &grpc.UnaryServerInfo{}, func(c context.Context, _ any) (any, error) {
		got, _ = service.ActorFrom(c)
		return "ok", nil
	})
	if err != nil || got != "u" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestAuthInterceptor_invalidToken(t *testing.T) {
	intc := AuthInterceptor(fakeValidator{err: stderrors.New("bad")})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer abc"))
	_, err := intc(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestGRPCError_mapsAppError(t *testing.T) {
	if status.Code(grpcError(apperr.NotFound("x.not_found", "nope"))) != codes.NotFound {
		t.Fatal("AppError should map to its gRPC code")
	}
}

func TestGRPCError_genericInternal(t *testing.T) {
	if status.Code(grpcError(stderrors.New("boom"))) != codes.Internal {
		t.Fatal("non-AppError should map to Internal")
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/handler/grpc/`
Expected: FAIL (undefined: AuthInterceptor, grpcError).

- [ ] **Step 3: Implement `server.go`**

Create `internal/handler/grpc/server.go`:

```go
// Package grpc hosts aero-id's internal gRPC transport: a server with auth and
// recovery interceptors plus handlers that delegate to the shared service layer.
package grpc

import (
	"context"
	stderrors "errors"
	"runtime/debug"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

// TokenValidator is the SSO capability the auth interceptor needs. *sso.Client
// satisfies it.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
}

// ServiceRegistrar lets each handler register itself on the server without
// server.go importing the concrete handler types.
type ServiceRegistrar interface {
	Register(s *grpc.Server)
}

// NewServer builds a *grpc.Server with recovery + auth interceptors and registers
// each provided handler.
func NewServer(validator TokenValidator, log *zap.Logger, regs ...ServiceRegistrar) *grpc.Server {
	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			RecoveryInterceptor(log),
			AuthInterceptor(validator),
		),
	)
	for _, r := range regs {
		r.Register(s)
	}
	return s
}

// AuthInterceptor validates the bearer token from request metadata and injects
// the resulting uid as the request actor.
func AuthInterceptor(v TokenValidator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		token := bearerFromMD(ctx)
		if token == "" {
			return nil, status.Error(codes.Unauthenticated, "missing bearer token")
		}
		uid, err := v.ValidateToken(ctx, token)
		if err != nil || uid == "" {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		return handler(service.WithActor(ctx, uid), req)
	}
}

// RecoveryInterceptor converts a panic into a logged Internal status.
func RecoveryInterceptor(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("grpc panic recovered",
					zap.Any("panic", r),
					zap.String("method", info.FullMethod),
					zap.ByteString("stack", debug.Stack()),
				)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// grpcError maps a service error to a gRPC status. *AppError carries its own code;
// anything else becomes Internal so internals are not leaked.
func grpcError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.AppError
	if stderrors.As(err, &ae) {
		return status.Error(ae.GRPC, ae.Message)
	}
	return status.Error(codes.Internal, "internal server error")
}

func bearerFromMD(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	const prefix = "Bearer "
	h := vals[0]
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/handler/grpc/ -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/handler/grpc/server.go internal/handler/grpc/server_test.go
git commit -m "feat(grpc): add server with auth/recovery interceptors and error mapping"
```

---

### Task 31: gRPC file/message/comment handlers + bufconn test

**Files:**
- Create: `internal/handler/grpc/file.go`
- Create: `internal/handler/grpc/message.go`
- Create: `internal/handler/grpc/comment.go`
- Test: `internal/handler/grpc/handlers_test.go` (package `grpc_test`, bufconn end-to-end)

- [ ] **Step 1: Write failing test**

Create `internal/handler/grpc/handlers_test.go`:

```go
package grpc_test

import (
	"context"
	"net"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type validator struct{ uid string }

func (v validator) ValidateToken(context.Context, string) (string, error) { return v.uid, nil }

// Embedding the service interface lets each fake implement only the methods the
// test exercises; unimplemented ones are never called.
type fileSvc struct {
	service.FileService
	getFn func(context.Context, domain.ID) (*domain.File, error)
}

func (f fileSvc) Get(ctx context.Context, id domain.ID) (*domain.File, error) { return f.getFn(ctx, id) }

type msgSvc struct {
	service.MessageService
	sendFn func(context.Context, service.SendMessageInput) (*domain.Message, error)
}

func (m msgSvc) Send(ctx context.Context, in service.SendMessageInput) (*domain.Message, error) {
	return m.sendFn(ctx, in)
}

func dial(t *testing.T, regs ...grpcsrv.ServiceRegistrar) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcsrv.NewServer(validator{uid: "u1"}, zap.NewNop(), regs...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func authCtx() context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer t"))
}

func TestGRPC_SendMessage(t *testing.T) {
	svc := msgSvc{sendFn: func(_ context.Context, in service.SendMessageInput) (*domain.Message, error) {
		return &domain.Message{ID: idgen.NewID(), FromUID: "u1", ToUID: in.ToUID, Content: in.Content}, nil
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)

	resp, err := client.SendMessage(authCtx(), &aeropb.SendMessageRequest{ToUid: "b", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.ToUid != "b" || resp.Message.FromUid != "u1" || resp.Message.Content != "hi" {
		t.Fatalf("unexpected message: %+v", resp.Message)
	}
}

func TestGRPC_AuthRequired(t *testing.T) {
	conn := dial(t, grpcsrv.NewMessageServer(msgSvc{}))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.SendMessage(context.Background(), &aeropb.SendMessageRequest{ToUid: "b", Content: "hi"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestGRPC_GetFileNotFound(t *testing.T) {
	svc := fileSvc{getFn: func(context.Context, domain.ID) (*domain.File, error) {
		return nil, apperr.NotFound("file.not_found", "nope")
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	_, err := client.GetFile(authCtx(), &aeropb.GetFileRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/handler/grpc/`
Expected: FAIL (undefined: NewMessageServer, NewFileServer).

- [ ] **Step 3: Implement `file.go`**

Create `internal/handler/grpc/file.go`:

```go
package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// defaultDownloadTTL is used when a DownloadURL request omits ttl_seconds.
const defaultDownloadTTL = 5 * time.Minute

type FileServer struct {
	aeropb.UnimplementedFileServiceServer
	files service.FileService
}

func NewFileServer(files service.FileService) *FileServer { return &FileServer{files: files} }

func (s *FileServer) Register(srv *grpc.Server) { aeropb.RegisterFileServiceServer(srv, s) }

func (s *FileServer) GetFile(ctx context.Context, req *aeropb.GetFileRequest) (*aeropb.GetFileResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	f, err := s.files.Get(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetFileResponse{File: fileToPB(f)}, nil
}

func (s *FileServer) ListFiles(ctx context.Context, req *aeropb.ListFilesRequest) (*aeropb.ListFilesResponse, error) {
	list, err := s.files.List(ctx, req.OwnerUid, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListFilesResponse{Files: filesToPB(list.Files), NextCursor: list.NextCursor}, nil
}

func (s *FileServer) DeleteFile(ctx context.Context, req *aeropb.DeleteFileRequest) (*aeropb.DeleteFileResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.files.Delete(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.DeleteFileResponse{}, nil
}

func (s *FileServer) GetDownloadURL(ctx context.Context, req *aeropb.DownloadURLRequest) (*aeropb.DownloadURLResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	ttl := time.Duration(req.TtlSeconds) * time.Second
	if ttl <= 0 {
		ttl = defaultDownloadTTL
	}
	url, err := s.files.DownloadURL(ctx, id, ttl)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.DownloadURLResponse{Url: url}, nil
}

// ---- shared converters (used by message.go and comment.go too) ----

func pageFromPB(p *aeropb.Page) domain.Page {
	if p == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(p.Limit), Cursor: p.Cursor}
}

func fileToPB(f *domain.File) *aeropb.File {
	return &aeropb.File{
		Id:          f.ID.String(),
		OwnerUid:    f.OwnerUID,
		Name:        f.Name,
		Size:        f.Size,
		MimeType:    f.MimeType,
		Visibility:  int32(f.Visibility),
		StorageType: f.StorageType,
		CreatedAt:   timestamppb.New(f.CreatedAt),
		UpdatedAt:   timestamppb.New(f.UpdatedAt),
	}
}

func filesToPB(fs []*domain.File) []*aeropb.File {
	out := make([]*aeropb.File, 0, len(fs))
	for _, f := range fs {
		out = append(out, fileToPB(f))
	}
	return out
}
```

- [ ] **Step 4: Implement `message.go`**

Create `internal/handler/grpc/message.go`:

```go
package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

type MessageServer struct {
	aeropb.UnimplementedMessageServiceServer
	messages service.MessageService
}

func NewMessageServer(messages service.MessageService) *MessageServer {
	return &MessageServer{messages: messages}
}

func (s *MessageServer) Register(srv *grpc.Server) { aeropb.RegisterMessageServiceServer(srv, s) }

func (s *MessageServer) SendMessage(ctx context.Context, req *aeropb.SendMessageRequest) (*aeropb.SendMessageResponse, error) {
	in := service.SendMessageInput{
		ToUID:   req.ToUid,
		Kind:    domain.MessageKind(req.Kind),
		Content: req.Content,
	}
	if req.FileId != "" {
		fid, err := idgen.Parse(req.FileId)
		if err != nil {
			return nil, grpcError(apperr.BadRequest("message.invalid", "invalid file_id"))
		}
		in.FileID = &fid
	}
	m, err := s.messages.Send(ctx, in)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.SendMessageResponse{Message: msgToPB(m)}, nil
}

func (s *MessageServer) GetMessage(ctx context.Context, req *aeropb.GetMessageRequest) (*aeropb.GetMessageResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	m, err := s.messages.Get(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetMessageResponse{Message: msgToPB(m)}, nil
}

func (s *MessageServer) Inbox(ctx context.Context, req *aeropb.InboxRequest) (*aeropb.InboxResponse, error) {
	list, err := s.messages.Inbox(ctx, pageFromPB(req.Page), req.UnreadOnly)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.InboxResponse{
		Messages:   msgsToPB(list.Messages),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *MessageServer) Conversation(ctx context.Context, req *aeropb.ConversationRequest) (*aeropb.ConversationResponse, error) {
	list, err := s.messages.Conversation(ctx, req.WithUid, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ConversationResponse{
		Messages:   msgsToPB(list.Messages),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *MessageServer) MarkRead(ctx context.Context, req *aeropb.MarkReadRequest) (*aeropb.MarkReadResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.messages.MarkRead(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.MarkReadResponse{}, nil
}

func msgToPB(m *domain.Message) *aeropb.Message {
	pb := &aeropb.Message{
		Id:        m.ID.String(),
		FromUid:   m.FromUID,
		ToUid:     m.ToUID,
		Kind:      int32(m.Kind),
		Content:   m.Content,
		CreatedAt: timestamppb.New(m.CreatedAt),
	}
	if m.FileID != nil {
		pb.FileId = m.FileID.String()
	}
	if m.ReadAt != nil {
		pb.ReadAt = timestamppb.New(*m.ReadAt)
	}
	return pb
}

func msgsToPB(ms []*domain.Message) []*aeropb.Message {
	out := make([]*aeropb.Message, 0, len(ms))
	for _, m := range ms {
		out = append(out, msgToPB(m))
	}
	return out
}

// usersToPB is shared with the comment handler.
func usersToPB(users map[string]sso.User) map[string]*aeropb.User {
	out := make(map[string]*aeropb.User, len(users))
	for uid, u := range users {
		out[uid] = &aeropb.User{Uid: u.UID, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out
}
```

- [ ] **Step 5: Implement `comment.go`**

Create `internal/handler/grpc/comment.go`:

```go
package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type CommentServer struct {
	aeropb.UnimplementedCommentServiceServer
	comments service.CommentService
}

func NewCommentServer(comments service.CommentService) *CommentServer {
	return &CommentServer{comments: comments}
}

func (s *CommentServer) Register(srv *grpc.Server) { aeropb.RegisterCommentServiceServer(srv, s) }

func (s *CommentServer) CreateComment(ctx context.Context, req *aeropb.CreateCommentRequest) (*aeropb.CreateCommentResponse, error) {
	tid, err := idgen.Parse(req.TargetId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("comment.invalid", "invalid target_id"))
	}
	in := service.CreateCommentInput{
		TargetType: domain.TargetType(req.TargetType),
		TargetID:   tid,
		Content:    req.Content,
	}
	if req.ParentId != "" {
		pid, err := idgen.Parse(req.ParentId)
		if err != nil {
			return nil, grpcError(apperr.BadRequest("comment.invalid", "invalid parent_id"))
		}
		in.ParentID = &pid
	}
	c, err := s.comments.Create(ctx, in)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.CreateCommentResponse{Comment: commentToPB(c)}, nil
}

func (s *CommentServer) ListComments(ctx context.Context, req *aeropb.ListCommentsRequest) (*aeropb.ListCommentsResponse, error) {
	tid, err := idgen.Parse(req.TargetId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("comment.invalid", "invalid target_id"))
	}
	list, err := s.comments.List(ctx, domain.TargetType(req.TargetType), tid, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListCommentsResponse{
		Comments:   commentsToPB(list.Comments),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *CommentServer) Replies(ctx context.Context, req *aeropb.RepliesRequest) (*aeropb.RepliesResponse, error) {
	rootID, err := idgen.Parse(req.RootId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid root_id"))
	}
	list, err := s.comments.Replies(ctx, rootID, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.RepliesResponse{
		Comments:   commentsToPB(list.Comments),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *CommentServer) DeleteComment(ctx context.Context, req *aeropb.DeleteCommentRequest) (*aeropb.DeleteCommentResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.comments.Delete(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.DeleteCommentResponse{}, nil
}

func commentToPB(c *domain.Comment) *aeropb.Comment {
	pb := &aeropb.Comment{
		Id:         c.ID.String(),
		OwnerUid:   c.OwnerUID,
		TargetType: string(c.TargetType),
		TargetId:   c.TargetID.String(),
		RootId:     c.RootID.String(),
		Content:    c.Content,
		ReplyCount: int32(c.ReplyCount),
		Status:     int32(c.Status),
		CreatedAt:  timestamppb.New(c.CreatedAt),
	}
	if c.ParentID != nil {
		pb.ParentId = c.ParentID.String()
	}
	return pb
}

func commentsToPB(cs []*domain.Comment) []*aeropb.Comment {
	out := make([]*aeropb.Comment, 0, len(cs))
	for _, c := range cs {
		out = append(out, commentToPB(c))
	}
	return out
}
```

- [ ] **Step 6: Verify tests pass**

Run: `go test ./internal/handler/grpc/ -v`
Expected: PASS (server unit tests + 3 bufconn tests).

- [ ] **Step 7: Commit**

```bash
git add internal/handler/grpc/file.go internal/handler/grpc/message.go internal/handler/grpc/comment.go internal/handler/grpc/handlers_test.go
git commit -m "feat(grpc): add file/message/comment handlers with bufconn integration test"
```

---

## Phase 9: Wire-up & Delivery

> The composition root assembles every layer and runs both transports with graceful
> shutdown. There is no unit test for `app.go`/`main.go` (they are wiring); the gates
> are `go build ./...`, `go vet ./...`, the full `make test`, and a docker-compose
> smoke run. Migrations are **not** auto-applied at startup (avoids multi-replica
> races) — they run via `make migrate-up` or the compose `migrate` job.

### Task 32: `app/app.go` + `cmd/server/main.go` (wiring + graceful shutdown)

**Files:**
- Create: `internal/app/app.go`
- Modify: `cmd/server/main.go` (replace the Task 1 placeholder)

- [ ] **Step 1: Implement `app.go`**

Create `internal/app/app.go`:

```go
// Package app is aero-id's composition root: it wires config + logger into the
// repository, storage, SSO, service, and transport layers, then runs both the
// HTTP and gRPC servers with graceful shutdown.
package app

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
	grpclib "google.golang.org/grpc"

	"github.com/aero/aero-id/internal/config"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
)

// presignTTL is how long a generated download URL stays valid.
const presignTTL = 5 * time.Minute

type App struct {
	cfg        *config.Config
	log        *zap.Logger
	sqlDB      *sql.DB
	sso        *sso.Client
	httpServer *http.Server
	grpcServer *grpclib.Server
	grpcLis    net.Listener
}

// New builds the fully wired application. It opens the DB, storage, and SSO
// connections; constructs repositories, services, and both transports.
func New(ctx context.Context, cfg *config.Config, log *zap.Logger) (*App, error) {
	gdb, err := db.Open(db.Options{
		DSN:             cfg.Database.DSN(),
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		LogLevel:        cfg.Database.LogLevel,
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("sql db handle: %w", err)
	}

	store, downloader, err := buildStorage(ctx, cfg)
	if err != nil {
		return nil, err
	}

	ssoClient, err := sso.NewClient(ctx, cfg.SSO.Endpoint, cfg.SSO.Insecure, sso.ClientConfig{
		TokenCacheTTL: cfg.SSO.TokenCacheTTL,
		UserCacheTTL:  cfg.SSO.UserCacheTTL,
		Timeout:       cfg.SSO.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("sso client: %w", err)
	}

	fileRepo := repository.NewFileRepository(gdb)
	msgRepo := repository.NewMessageRepository(gdb)
	commentRepo := repository.NewCommentRepository(gdb)

	fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type)
	msgSvc := service.NewMessageService(msgRepo, fileRepo, ssoClient)
	commentSvc := service.NewCommentService(commentRepo, fileRepo, msgRepo, ssoClient)

	// HTTP transport
	health := httpapi.NewHealthHandler(func(c context.Context) error { return sqlDB.PingContext(c) })
	router := httpapi.NewRouter(
		log,
		httpapi.RouterConfig{},
		ssoClient,
		health,
		httpapi.NewFileHandler(fileSvc, downloader, presignTTL),
		httpapi.NewMessageHandler(msgSvc),
		httpapi.NewCommentHandler(commentSvc),
	)
	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.HTTP.Host, cfg.Server.HTTP.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.HTTP.ReadTimeout,
		WriteTimeout: cfg.Server.HTTP.WriteTimeout,
	}

	// gRPC transport
	grpcServer := grpcsrv.NewServer(
		ssoClient, log,
		grpcsrv.NewFileServer(fileSvc),
		grpcsrv.NewMessageServer(msgSvc),
		grpcsrv.NewCommentServer(commentSvc),
	)
	grpcAddr := fmt.Sprintf("%s:%d", cfg.Server.GRPC.Host, cfg.Server.GRPC.Port)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return nil, fmt.Errorf("grpc listen on %s: %w", grpcAddr, err)
	}

	return &App{
		cfg:        cfg,
		log:        log,
		sqlDB:      sqlDB,
		sso:        ssoClient,
		httpServer: httpServer,
		grpcServer: grpcServer,
		grpcLis:    grpcLis,
	}, nil
}

// buildStorage selects the configured backend. Only the local backend serves
// signature-verified downloads through aero-id; S3 presigned URLs target the
// object store directly, so its downloader is nil.
func buildStorage(ctx context.Context, cfg *config.Config) (storage.FileStorage, httpapi.SignedDownloader, error) {
	switch cfg.Storage.Type {
	case "local":
		local := storage.NewLocal(storage.LocalConfig{
			Root:          cfg.Storage.Local.Root,
			SigningKey:    cfg.SigningKey,
			PublicBaseURL: cfg.Storage.Local.PublicBaseURL,
		})
		return local, local, nil
	case "s3":
		s3, err := storage.NewS3(ctx, storage.S3Config{
			Region:       cfg.Storage.S3.Region,
			Bucket:       cfg.Storage.S3.Bucket,
			Endpoint:     cfg.Storage.S3.Endpoint,
			AccessKey:    cfg.Storage.S3.AccessKey,
			SecretKey:    cfg.Storage.S3.SecretKey,
			UsePathStyle: cfg.Storage.S3.UsePathStyle,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("s3 storage: %w", err)
		}
		return s3, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown storage type %q", cfg.Storage.Type)
	}
}

// Run starts both servers and blocks until ctx is cancelled or a server fails,
// then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 2)
	go func() {
		a.log.Info("grpc server starting", zap.String("addr", a.grpcLis.Addr().String()))
		if err := a.grpcServer.Serve(a.grpcLis); err != nil {
			errCh <- fmt.Errorf("grpc serve: %w", err)
		}
	}()
	go func() {
		a.log.Info("http server starting", zap.String("addr", a.httpServer.Addr))
		if err := a.httpServer.ListenAndServe(); err != nil && !stderrors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		a.log.Info("shutdown signal received")
	case err := <-errCh:
		a.log.Error("server error, shutting down", zap.Error(err))
		a.shutdown()
		return err
	}
	a.shutdown()
	return nil
}

func (a *App) shutdown() {
	shCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.HTTP.ShutdownTimeout)
	defer cancel()
	if err := a.httpServer.Shutdown(shCtx); err != nil {
		a.log.Error("http shutdown error", zap.Error(err))
	}
	a.grpcServer.GracefulStop()
}

// Close releases long-lived resources. Safe to call after Run returns.
func (a *App) Close() {
	if a.sso != nil {
		a.sso.Close()
	}
	if a.sqlDB != nil {
		_ = a.sqlDB.Close()
	}
}
```

- [ ] **Step 2: Replace `cmd/server/main.go`**

Replace `cmd/server/main.go` (the Task 1 placeholder) with:

```go
package main

import (
	"context"
	"flag"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/app"
	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/pkg/logger"
)

func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		panic("load config: " + err.Error())
	}

	log, err := logger.New(cfg.Log.Level, cfg.Log.Encoding)
	if err != nil {
		panic("init logger: " + err.Error())
	}
	defer func() { _ = log.Sync() }()

	// Cancel ctx on SIGINT/SIGTERM so Run shuts down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Fatal("initialize app", zap.Error(err))
	}
	defer application.Close()

	if err := application.Run(ctx); err != nil {
		log.Fatal("run app", zap.Error(err))
	}
	log.Info("server stopped cleanly")
}
```

- [ ] **Step 3: Tidy modules (drops any unused first-attempt deps)**

Run: `go mod tidy`

- [ ] **Step 4: Verify the whole tree builds and vets**

Run:
```
go build ./...
go vet ./...
```
Expected: both succeed with no output. (`./...` silently skips the integration-only `testpg` package — confirmed behavior.)

- [ ] **Step 5: Verify all unit tests still pass**

Run: `make test`
Expected: PASS across every package (integration-tagged tests are excluded).

- [ ] **Step 6: Commit**

```bash
git add internal/app/ cmd/server/main.go go.mod go.sum
git commit -m "feat(app): wire composition root and server entrypoint with graceful shutdown"
```

---

### Task 33: Dockerfile + docker-compose

**Files:**
- Create: `deployments/Dockerfile`
- Create: `deployments/docker-compose.yml`
- Create/Modify: `.dockerignore`

- [ ] **Step 1: Write `.dockerignore`**

Create `.dockerignore` at the repo root:

```
.git
data
*.md
docs
deployments
bin
*_test.go
```

- [ ] **Step 2: Write the Dockerfile**

Create `deployments/Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/aero-id ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/aero-id /app/aero-id
COPY configs/config.yaml /app/configs/config.yaml
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/app/aero-id", "-config", "/app/configs/config.yaml"]
```

- [ ] **Step 3: Write docker-compose**

Create `deployments/docker-compose.yml`:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: aero
      POSTGRES_PASSWORD: aero
      POSTGRES_DB: aero_id
    ports: ["5432:5432"]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U aero -d aero_id"]
      interval: 5s
      timeout: 3s
      retries: 10
    volumes:
      - pgdata:/var/lib/postgresql/data

  migrate:
    image: migrate/migrate:v4.17.1
    depends_on:
      postgres:
        condition: service_healthy
    volumes:
      - ../migrations:/migrations
    command:
      ["-path", "/migrations", "-database",
       "postgres://aero:aero@postgres:5432/aero_id?sslmode=disable", "up"]

  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    ports: ["9000:9000", "9001:9001"]
    volumes:
      - miniodata:/data

  aero-id:
    build:
      context: ..
      dockerfile: deployments/Dockerfile
    depends_on:
      postgres:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
    environment:
      # Override the file config for the compose network (see config.yaml defaults).
      AERO_ID_DATABASE_HOST: postgres
      AERO_ID_DATABASE_PASSWORD: aero
      AERO_ID_SIGNING_KEY: docker-dev-signing-key-change-me
      AERO_ID_SSO_ENDPOINT: "sso:9000"
    ports: ["8080:8080", "9090:9090"]

volumes:
  pgdata:
  miniodata:
```

> SSO is an **external** dependency and is not part of this compose file. The app
> boots without it (the gRPC client connects lazily); SSO-dependent endpoints fail
> only when called until a real SSO endpoint is reachable at `AERO_ID_SSO_ENDPOINT`.

- [ ] **Step 4: Validate the compose file**

Run: `docker compose -f deployments/docker-compose.yml config -q`
Expected: no output, exit 0 (compose syntax is valid).

- [ ] **Step 5: Commit**

```bash
git add deployments/ .dockerignore
git commit -m "chore(deploy): add Dockerfile and docker-compose (pg + minio + migrate + app)"
```

---

### Task 34: README + final verification + smoke run

**Files:**
- Create: `README.md`

- [ ] **Step 1: Write `README.md`**

Create `README.md`:

```markdown
# aero-id

A Go service that manages **files, messages, and comments** owned by accounts.
Account identity lives in an external **SSO** service, which aero-id calls over
gRPC for token validation and batch user lookups. aero-id stores no account
records of its own — every row references an SSO `uid`.

## Architecture

Horizontally layered, with two transports sharing one service layer:

```
HTTP (Gin, :8080)  ─┐
                    ├─►  service  ──►  repository (GORM/Postgres)
gRPC (:9090)       ─┘                └►  storage (local | S3)
                                     └►  sso (gRPC client + caches)
```

- **handler** (`internal/handler/{http,grpc}`) — transport-specific I/O only.
- **service** (`internal/service`) — all business logic and authorization.
- **repository** (`internal/repository`) — GORM persistence on PostgreSQL 16.
- **storage** (`internal/storage`) — `FileStorage` with `local` (HMAC-signed
  presigned URLs) and `s3` (MinIO-compatible) backends.
- **sso** (`internal/sso`) — gRPC client with a token cache and a batch user cache.

See `docs/superpowers/specs/2026-05-22-aero-id-design.md` for the full design.

## Prerequisites

- Go 1.26+
- Docker (for integration tests and local stack)
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (only to regenerate protos)
- `migrate` CLI (golang-migrate) for running migrations locally

## Quickstart (Docker)

```bash
docker compose -f deployments/docker-compose.yml up --build
curl -s localhost:8080/healthz        # {"status":"ok"}
curl -s localhost:8080/readyz         # {"status":"ready"} once Postgres is up
```

## Local development

```bash
make migrate-up        # apply DDL to a local Postgres at 127.0.0.1:5432
make run               # run the server with configs/config.yaml
make test              # unit tests
make test-integration  # integration tests (spins up Postgres/MinIO via testcontainers)
make proto             # regenerate gRPC stubs
```

## Configuration

Config is read from `configs/config.yaml`, with any value overridable by an
`AERO_ID_*` environment variable (e.g. `AERO_ID_DATABASE_PASSWORD`,
`AERO_ID_SIGNING_KEY`, `AERO_ID_SSO_ENDPOINT`). Secrets should be supplied via
env, never committed.

## HTTP API (all under `/v1`, Bearer token required)

| Method | Path | Description |
|---|---|---|
| POST | `/v1/files` | Upload a file (multipart `file`) |
| GET | `/v1/files` | List your files |
| GET | `/v1/files/:id` | File metadata |
| GET | `/v1/files/:id/url` | Presigned download URL |
| DELETE | `/v1/files/:id` | Delete a file |
| POST | `/v1/messages` | Send a message |
| GET | `/v1/messages` | Inbox (`?unread=true`) |
| GET | `/v1/messages/:id` | Get a message |
| POST | `/v1/messages/:id/read` | Mark read |
| GET | `/v1/conversations/:uid` | Conversation with a peer |
| POST | `/v1/comments` | Create a comment (`parent_id` for replies) |
| GET | `/v1/comments?target_type=&target_id=` | Top-level comments |
| GET | `/v1/comments/:id/replies` | Replies under a root comment |
| DELETE | `/v1/comments/:id` | Delete your comment |

`GET /healthz` and `GET /readyz` are unauthenticated. Local presigned downloads
are served at `GET /files/dl` (signature-verified, no token).

The gRPC API (`api/proto/aero_id/v1/aero_id.proto`) mirrors these operations for
internal callers.

## Comment nesting

Comments support exactly two levels (top-level and one reply layer, 楼中楼).
Replying to a reply is rejected with `comment.nesting_too_deep`.
```

- [ ] **Step 2: Run the full unit test suite**

Run: `make test`
Expected: PASS for every package.

- [ ] **Step 3: Run the integration suite**

Run: `make test-integration`
Expected: PASS (repository tests against real Postgres, local+S3 storage against MinIO, SSO bufconn) — pulls images on first run.

- [ ] **Step 4: Smoke-test the running stack**

Run:
```
docker compose -f deployments/docker-compose.yml up --build -d
sleep 5
curl -sf localhost:8080/healthz
curl -sf localhost:8080/readyz
docker compose -f deployments/docker-compose.yml down
```
Expected: `/healthz` returns `{"status":"ok"}`; `/readyz` returns `{"status":"ready"}`.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs: add README with architecture, API, and quickstart"
```

---

## Plan Complete — Definition of Done

When all 34 tasks are committed, the following must hold:

- [ ] `go build ./...` and `go vet ./...` are clean.
- [ ] `make test` passes (every unit test, all packages).
- [ ] `make test-integration` passes (Postgres repo tests, MinIO S3 tests, bufconn SSO/gRPC).
- [ ] `docker compose ... up` brings up the stack; `/healthz` and `/readyz` return 200.
- [ ] Every commit follows Conventional Commits and corresponds to one task.

### Spec → task coverage

| Spec area | Tasks |
|---|---|
| Module skeleton, config, logging, errors, IDs, DB helper | 1–6 |
| Schema / migrations (files, messages, comments) | 7 |
| Domain model + two-level comment rule | 8–9 |
| Repository layer (GORM, cursor pagination, soft delete) | 10–12 |
| File storage (interface, local HMAC presign, S3/MinIO) | 13–15 |
| SSO integration (proto, token cache, user cache, client) | 16–19 |
| Service layer (ACL, enrichment, nesting enforcement) | 20–23 |
| HTTP transport (middleware, router, handlers) | 24–28 |
| gRPC transport (proto, server, interceptors, handlers) | 29–31 |
| Composition root, Docker, README | 32–34 |

### Deferred (per design §11.2, explicitly out of MVP)

AI columns/outbox/consents, MCP server, metrics/tracing. Do **not** implement these
unless their trigger conditions in the spec are met.





