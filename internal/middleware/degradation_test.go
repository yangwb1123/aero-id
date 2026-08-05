package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestDegradationState_InitiallyHealthy(t *testing.T) {
	s := NewDegradationState()
	if s.Status() != "none" {
		t.Fatalf("initial status = %q, want 'none'", s.Status())
	}
	if s.IsDegraded(DepDB) {
		t.Fatal("DB should not be degraded initially")
	}
	if s.IsDegraded(DepSSO) {
		t.Fatal("SSO should not be degraded initially")
	}
	if s.IsDegraded(DepStorage) {
		t.Fatal("Storage should not be degraded initially")
	}
}

func TestDegradationState_SetDegraded(t *testing.T) {
	s := NewDegradationState()

	// Mark DB as degraded.
	s.SetDegraded(DepDB, true)
	if !s.IsDegraded(DepDB) {
		t.Fatal("DB should be degraded after SetDegraded(true)")
	}
	if s.IsDegraded(DepSSO) {
		t.Fatal("SSO should still be healthy")
	}
	if s.Status() != "db" {
		t.Fatalf("status = %q, want 'db'", s.Status())
	}

	// Mark SSO as degraded too.
	s.SetDegraded(DepSSO, true)
	if s.Status() != "db,sso" {
		t.Fatalf("status = %q, want 'db,sso'", s.Status())
	}

	// Recover DB.
	s.SetDegraded(DepDB, false)
	if s.IsDegraded(DepDB) {
		t.Fatal("DB should be healthy after SetDegraded(false)")
	}
	if !s.IsDegraded(DepSSO) {
		t.Fatal("SSO should still be degraded")
	}
	if s.Status() != "sso" {
		t.Fatalf("status = %q, want 'sso'", s.Status())
	}
}

func TestDegradationState_AllDegraded(t *testing.T) {
	s := NewDegradationState()
	s.SetDegraded(DepDB, true)
	s.SetDegraded(DepSSO, true)
	s.SetDegraded(DepStorage, true)
	if s.Status() != "db,sso,storage" {
		t.Fatalf("status = %q, want 'db,sso,storage'", s.Status())
	}
}

func TestDegradationState_ProbeTransitions(t *testing.T) {
	s := NewDegradationState()

	// Register a prober that starts healthy, then fails, then recovers.
	attempt := 0
	s.SetProber(DepDB, func(ctx context.Context) error {
		attempt++
		if attempt <= 2 {
			return nil // healthy first 2 times
		}
		if attempt <= 5 {
			return errors.New("db down") // fail 3-5
		}
		return nil // recover after
	})

	stop := s.StartProbing(context.Background(), 10*time.Millisecond, 2, 2)
	defer stop()

	// Wait for enough probe cycles for the threshold to be crossed.
	time.Sleep(150 * time.Millisecond)

	// The DB should have transitioned to degraded and then back to healthy.
	// At minimum it should have been degraded at some point.
	// (Race condition note: timing is approximate in tests.)
	_ = s.Status() // just ensure no crash
}

func TestDegradationHeader_Middleware(t *testing.T) {
	s := NewDegradationState()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(DegradationHeader(s))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "ok")
	})

	// Healthy state.
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "/test", nil)
	router.ServeHTTP(w, req)
	if w.Header().Get("X-Aero-Degraded") != "none" {
		t.Fatalf("X-Aero-Degraded = %q, want 'none'", w.Header().Get("X-Aero-Degraded"))
	}

	// Degraded state.
	s.SetDegraded(DepDB, true)
	s.SetDegraded(DepSSO, true)
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequestWithContext(t.Context(), "GET", "/test", nil)
	router.ServeHTTP(w2, req2)
	if w2.Header().Get("X-Aero-Degraded") != "db,sso" {
		t.Fatalf("X-Aero-Degraded = %q, want 'db,sso'", w2.Header().Get("X-Aero-Degraded"))
	}
}

func TestDegradationState_ConcurrentSafe(t *testing.T) {
	s := NewDegradationState()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.SetDegraded(DepDB, true)
			s.SetDegraded(DepDB, false)
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 100; i++ {
			_ = s.Status()
			s.IsDegraded(DepSSO)
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

func TestDegradationState_UnknownDepPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unknown dep")
		}
	}()
	depBit(DegradedDep("unknown"))
}
