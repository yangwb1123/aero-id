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

func TestTokenCache_GetStaleWithinAndBeyondGrace(t *testing.T) {
	c := NewTokenCacheWithStale(60*time.Second, 10*time.Second)

	// Expired but within the grace window: Get misses, GetStale serves.
	c.Set("stale", "u-stale", time.Now().Add(-2*time.Second))
	if _, ok := c.Get("stale"); ok {
		t.Fatal("expired entry must miss the fresh Get")
	}
	if uid, ok := c.GetStale("stale"); !ok || uid != "u-stale" {
		t.Fatalf("within-grace GetStale uid=%q ok=%v", uid, ok)
	}

	// Beyond expiry+staleFor: GetStale also misses.
	c.Set("gone", "u-gone", time.Now().Add(-20*time.Second))
	if _, ok := c.GetStale("gone"); ok {
		t.Fatal("entry past expiry+staleFor must not be served")
	}
}

func TestTokenCache_StaleDisabledByDefault(t *testing.T) {
	c := NewTokenCache(60 * time.Second) // staleFor == 0
	c.Set("x", "u", time.Now().Add(-time.Second))
	if _, ok := c.GetStale("x"); ok {
		t.Fatal("with staleFor=0 an expired entry must not be served")
	}
}

func TestTokenCache_SweepKeepsStaleWithinGrace(t *testing.T) {
	c := NewTokenCacheWithStale(60*time.Second, time.Hour)
	c.Set("recent", "u", time.Now().Add(-time.Second)) // expired 1s ago, within 1h grace
	c.SweepExpired(time.Now())
	if _, ok := c.GetStale("recent"); !ok {
		t.Fatal("sweep must keep entries still within the stale grace window")
	}
}

func TestTokenCache_InvalidateEvictsAndFencesInflightSet(t *testing.T) {
	c := NewTokenCacheWithStale(time.Minute, time.Hour)
	c.Set("revoked", "uid-old", time.Now().Add(time.Minute))
	epoch := c.Epoch("revoked")
	c.Invalidate("revoked")
	if _, ok := c.Get("revoked"); ok {
		t.Fatal("invalidated token must miss the fresh cache")
	}
	if _, ok := c.GetStale("revoked"); ok {
		t.Fatal("invalidated token must not be served stale")
	}
	if c.SetAtEpoch("revoked", "uid-stale", time.Now().Add(time.Minute), epoch) {
		t.Fatal("in-flight result from before invalidation must be rejected")
	}
	if _, ok := c.Get("revoked"); ok {
		t.Fatal("stale in-flight result repopulated the cache")
	}
	if !c.SetAtEpoch("revoked", "uid-new", time.Now().Add(time.Minute), c.Epoch("revoked")) {
		t.Fatal("current epoch result should be accepted")
	}
	if uid, ok := c.Get("revoked"); !ok || uid != "uid-new" {
		t.Fatalf("current epoch cache = %q/%v, want uid-new/true", uid, ok)
	}
}
