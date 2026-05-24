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
