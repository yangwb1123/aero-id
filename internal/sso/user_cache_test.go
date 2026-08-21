package sso

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestUserCache_PreservesHitsOnAvailabilityError(t *testing.T) {
	f := &fakeFetcher{}
	c := NewUserCache(60*time.Second, f.Fetch)
	if _, err := c.GetMany(context.Background(), []string{"hit"}); err != nil {
		t.Fatal(err)
	}
	c.fetcher = func(context.Context, []string) (map[string]User, error) {
		return nil, status.Error(codes.Unavailable, "directory down")
	}
	got, err := c.GetMany(context.Background(), []string{"hit", "miss"})
	if err == nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("error = %v, want Unavailable", err)
	}
	if got["hit"].UID != "hit" {
		t.Fatalf("cached hit lost on partial failure: %+v", got)
	}
}

func TestUserCache_ServesStaleOnlyWithinGrace(t *testing.T) {
	clock := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	f := &fakeFetcher{}
	c := NewUserCacheWithStale(time.Minute, 5*time.Minute, f.Fetch)
	c.now = func() time.Time { return clock }
	if _, err := c.GetMany(context.Background(), []string{"stale"}); err != nil {
		t.Fatal(err)
	}
	c.fetcher = func(context.Context, []string) (map[string]User, error) {
		return nil, status.Error(codes.Unavailable, "directory down")
	}
	clock = clock.Add(2 * time.Minute)
	got, err := c.GetMany(context.Background(), []string{"stale"})
	if err == nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("stale lookup error = %v, want Unavailable", err)
	}
	if got["stale"].UID != "stale" {
		t.Fatalf("stale entry = %+v, want cached user", got)
	}
	clock = clock.Add(5 * time.Minute)
	got, err = c.GetMany(context.Background(), []string{"stale"})
	if err == nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("expired stale lookup error = %v, want Unavailable", err)
	}
	if len(got) != 0 {
		t.Fatalf("entry beyond stale grace was served: %+v", got)
	}
}

func TestUserCache_SweepExpiredKeepsGraceThenEvicts(t *testing.T) {
	clock := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	f := &fakeFetcher{}
	c := NewUserCacheWithStale(time.Minute, 5*time.Minute, f.Fetch)
	c.now = func() time.Time { return clock }
	if _, err := c.GetMany(context.Background(), []string{"u"}); err != nil {
		t.Fatal(err)
	}
	c.SweepExpired(clock.Add(2 * time.Minute))
	if len(c.items) != 1 {
		t.Fatalf("fresh/stale entry removed too early; items=%d", len(c.items))
	}
	c.SweepExpired(clock.Add(7 * time.Minute))
	if len(c.items) != 0 {
		t.Fatalf("expired entry retained after grace; items=%d", len(c.items))
	}
}

func TestUserCache_RunCleanerStopIsIdempotent(t *testing.T) {
	c := NewUserCache(time.Minute, func(context.Context, []string) (map[string]User, error) {
		return nil, errors.New("unused")
	})
	stop := c.RunCleaner(time.Hour)
	stop()
	stop()
}
