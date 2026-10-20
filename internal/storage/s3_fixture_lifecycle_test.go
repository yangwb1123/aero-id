//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

type minioCleanupRecorder struct {
	cleanups []func()
	errors   []string
	logs     []string
}

func (r *minioCleanupRecorder) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }
func (r *minioCleanupRecorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *minioCleanupRecorder) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

type minioOwnedContainer struct {
	testcontainers.Container
	deadline time.Time
	ctxErr   error
	calls    int
}

func (*minioOwnedContainer) GetContainerID() string { return "exact-owned-minio" }
func (c *minioOwnedContainer) Terminate(ctx context.Context) error {
	c.calls++
	c.deadline, _ = ctx.Deadline()
	c.ctxErr = ctx.Err()
	return nil
}

func TestMinioOwnershipPrecedesReadinessAndUsesFreshCleanupBudget(t *testing.T) {
	owner := &minioCleanupRecorder{}
	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: "minio/minio:RELEASE.2025-01-20T14-49-07Z"},
	}
	if err := minioOwnership(owner).Customize(&req); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	container := &minioOwnedContainer{}
	if err := req.LifecycleHooks[0].PostCreates[0](ctx, container); err != nil {
		t.Fatal(err)
	}
	if len(owner.cleanups) != 1 || container.calls != 0 {
		t.Fatal("ownership was not registered before readiness")
	}
	owner.cleanups[0]()
	remaining := time.Until(container.deadline)
	if container.calls != 1 || container.ctxErr != nil || remaining <= 0 || remaining > minioFixtureTimeout {
		t.Fatalf("cleanup calls=%d, context error=%v, deadline=%v", container.calls, container.ctxErr, container.deadline)
	}
	if len(owner.errors) != 0 {
		t.Fatal(owner.errors)
	}
}

func TestMinioCleanupFailureIsReportedNotSwallowed(t *testing.T) {
	owner := &minioCleanupRecorder{}
	cleanupMinioFixture(owner, func(context.Context) error { return errors.New("delete failed") })
	if len(owner.errors) != 1 || !strings.Contains(owner.errors[0], "terminate MinIO container") {
		t.Fatalf("cleanup failure was swallowed: %v", owner.errors)
	}
	if !strings.Contains(strings.Join(owner.logs, "\n"), "phase=terminate") ||
		!strings.Contains(strings.Join(owner.logs, "\n"), "outcome=failed") {
		t.Fatalf("failed cleanup lacks diagnostic: %v", owner.logs)
	}
}
