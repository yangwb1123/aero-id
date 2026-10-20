//go:build integration

package testpg

import (
	"context"
	"testing"

	"github.com/docker/docker/errdefs"
	"github.com/testcontainers/testcontainers-go"
	pg "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Exercise the real v0.32 module's nil-container error path, not just a fake
// terminator: cancel startup immediately after Docker assigns the exact ID.
func TestPostgresStartupFailureCleansOwnedContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	defer cancel()
	owner := &cleanupRecorder{}
	var owned testcontainers.Container
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		for _, fn := range owner.cleanups {
			fn()
		}
		for _, err := range owner.errors {
			t.Error(err)
		}
		for _, log := range owner.logs {
			t.Log(log)
		}
	}
	t.Cleanup(cleanup)
	failAfterCreation := testcontainers.CustomizeRequestOption(func(req *testcontainers.GenericContainerRequest) error {
		req.LifecycleHooks = append(req.LifecycleHooks, testcontainers.ContainerLifecycleHooks{
			PostCreates: []testcontainers.ContainerHook{func(_ context.Context, c testcontainers.Container) error {
				owned = c
				cancel()
				return nil
			}},
		})
		return nil
	})
	c, err := pg.Run(ctx, "postgres:16-alpine", pg.WithDatabase("aero_id_test"),
		pg.WithUsername("aero"), pg.WithPassword("aero"), pg.BasicWaitStrategies(),
		postgresOwnership(owner, nil), failAfterCreation)
	if err == nil || c != nil || owned == nil || len(owner.cleanups) != 1 {
		t.Fatalf("startup failure did not retain exact ownership: err=%v, module=%v, owned=%v", err, c, owned)
	}
	cleanup()
	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	defer cancelVerify()
	if _, err := owned.State(verifyCtx); !errdefs.IsNotFound(err) {
		t.Fatalf("exact owned container deletion was not verified: %v", err)
	}
}
