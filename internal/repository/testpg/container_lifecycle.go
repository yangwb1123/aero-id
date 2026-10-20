//go:build integration

package testpg

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	pg "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const postgresFixtureTimeout = time.Minute

type fixtureReporter interface {
	Errorf(string, ...any)
	Logf(string, ...any)
}

type fixtureOwner interface {
	fixtureReporter
	Cleanup(func())
}

// StartContainer preserves the per-test image, credentials and wait strategies.
// Cleanup is registered in PostCreates, before startup/readiness can fail:
// postgres.Run in testcontainers v0.32 discards the container on such errors.
// closePool must resolve the current pool when cleanup runs, not at creation.
func StartContainer(t *testing.T, ctx context.Context, closePool func() error) (*pg.PostgresContainer, error) {
	t.Helper()
	started := time.Now()
	c, err := pg.Run(ctx,
		"postgres:16-alpine",
		pg.WithDatabase("aero_id_test"),
		pg.WithUsername("aero"),
		pg.WithPassword("aero"),
		pg.BasicWaitStrategies(),
		postgresOwnership(t, closePool),
	)
	logFixturePhase(t, "run", started, err)
	return c, err
}

func postgresOwnership(t fixtureOwner, closePool func() error) testcontainers.CustomizeRequestOption {
	return func(req *testcontainers.GenericContainerRequest) error {
		req.LifecycleHooks = append(req.LifecycleHooks, testcontainers.ContainerLifecycleHooks{
			PostCreates: []testcontainers.ContainerHook{
				func(_ context.Context, c testcontainers.Container) error {
					t.Logf("fixture=postgres phase=owned container_id=%s", c.GetContainerID())
					t.Cleanup(func() { cleanupFixture(t, c.Terminate, closePool) })
					return nil
				},
			},
		})
		return nil
	}
}

// RegisterCleanup closes the SQL pool before terminating an already-owned
// container. New startup callers must use StartContainer so failed readiness
// also has an explicit cleanup registered for that exact container.
func RegisterCleanup(t *testing.T, terminate func(context.Context) error, closePool func() error) {
	t.Helper()
	t.Cleanup(func() { cleanupFixture(t, terminate, closePool) })
}

func cleanupFixture(t fixtureReporter, terminate func(context.Context) error, closePool func() error) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	defer cancel()
	if closePool != nil {
		started := time.Now()
		err := closePool()
		logFixturePhase(t, "close_pool", started, err)
		if err != nil {
			t.Errorf("close PostgreSQL pool: %v", err)
		}
	}
	started := time.Now()
	err := terminate(cleanupCtx)
	logFixturePhase(t, "terminate", started, err)
	if err != nil {
		t.Errorf("terminate PostgreSQL container: %v", err)
	}
}

func logFixturePhase(t fixtureReporter, phase string, started time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	t.Logf("fixture=postgres phase=%s elapsed=%s outcome=%s", phase, time.Since(started).Round(time.Millisecond), outcome)
}
