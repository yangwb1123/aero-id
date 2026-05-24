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
