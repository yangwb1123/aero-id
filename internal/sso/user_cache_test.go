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
