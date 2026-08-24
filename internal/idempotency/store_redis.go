package idempotency

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/redis/rueidis"
)

// redisReservationTTL is how long an in-progress reservation lives before Redis
// auto-expires it (matching the GormStore reservationTTL). A crashed leader's
// reservation is reclaimed after this TTL, rather than needing a manual reaper.
const redisReservationTTL = 2 * time.Minute

// redisRecord is the JSON-serialised form of a Record stored in Redis.
type redisRecord struct {
	Status      int    `json:"s"`
	ContentType string `json:"ct,omitempty"`
	Body        []byte `json:"b,omitempty"`
	Completed   bool   `json:"c"`
	// CreatedAt is set on Reserve and used by the stale-reclamation check.
	// Stored as UnixNano int64 for compact JSON.
	CreatedAt int64  `json:"t,omitempty"`
	Owner     string `json:"o,omitempty"`
}

// RedisStore implements Store backed by Redis. Key format:
//
//	idempot:{uid}:{endpoint}:{key}
//
// The first Reserve creates the key with a short TTL (redisReservationTTL).
// Complete persists the full response and extends the TTL to the caller-provided
// value so subsequent idempotent retries can replay the cached response.
// Stale reservations (crashed leaders) are auto-reclaimed by Redis TTL expiry.
type RedisStore struct {
	client rueidis.Client
}

// NewRedisStore wraps a rueidis.Client into an idempotency Store.
// The client is NOT owned by the store; the caller manages its lifecycle.
// Returns nil when client is nil (so callers can fall back gracefully).
func NewRedisStore(client rueidis.Client) *RedisStore {
	return &RedisStore{client: client}
}

// redisKey builds the Redis key for an idempotency entry.
func redisKey(uid, endpoint, key string) string {
	return "idempot:" + uid + ":" + endpoint + ":" + key
}

// recordTTL is the TTL applied to a successfully completed record so callers
// can replay the cached response. 24h matches the GormStore default.
const recordTTL = 24 * time.Hour

// Reserve implements Store.Reserve. It atomically creates a reservation key in
// Redis (SET NX). On success it returns (nil, true, nil) — the caller must
// run the handler and call Complete or Release. On conflict it returns the
// existing Record so the middleware can replay a completed response or signal
// an in-flight conflict.
func (s *RedisStore) Reserve(ctx context.Context, uid, endpoint, key string) (*Record, bool, error) {
	rec, reserved, _, err := s.ReserveLease(ctx, uid, endpoint, key)
	return rec, reserved, err
}

// ReserveLease stores a random owner token inside the reservation payload. The
// token is compared atomically by the lease completion/release scripts.
func (s *RedisStore) ReserveLease(ctx context.Context, uid, endpoint, key string) (*Record, bool, string, error) {
	k := redisKey(uid, endpoint, key)
	now := time.Now().UTC()
	owner := uuid.NewString()
	inProgress := &redisRecord{Status: 0, Completed: false, CreatedAt: now.UnixNano(), Owner: owner}
	payload, err := json.Marshal(inProgress)
	if err != nil {
		return nil, false, "", fmt.Errorf("redis reserve marshal: %w", err)
	}
	cmd := s.client.B().Set().Key(k).Value(string(payload)).Nx().Ex(redisReservationTTL).Build()
	if err := s.client.Do(ctx, cmd).Error(); err != nil {
		rec, reserved, getErr := s.getExisting(ctx, k)
		return rec, reserved, "", getErr
	}
	return nil, true, owner, nil
}

// getExisting retrieves the existing record for a key. Returns (rec, false, nil)
// when the record is found, or (nil, false, nil) when the key has expired between
// the NX check and the GET (race is harmless — retry will re-reserve).
func (s *RedisStore) getExisting(ctx context.Context, key string) (*Record, bool, error) {
	resp, err := s.client.Do(ctx, s.client.B().Get().Key(key).Build()).AsBytes()
	if err != nil {
		if rueidis.IsRedisNil(err) {
			// Key expired between NX and GET — rare race; caller can retry.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("redis get existing: %w", err)
	}
	var rr redisRecord
	if err := json.Unmarshal(resp, &rr); err != nil {
		return nil, false, fmt.Errorf("redis unmarshal existing: %w", err)
	}
	rec := &Record{
		Status:      rr.Status,
		ContentType: rr.ContentType,
		Body:        rr.Body,
		Completed:   rr.Completed,
	}
	return rec, false, nil
}

// Complete implements Store.Complete. It stores the completed response with the
// given TTL so retries can replay it. The key must exist (from Reserve); if it
// was deleted or expired the store silently creates a new key — this is safe
// because the caller still has the reservation and will only call Complete
// once per key.
var redisCompleteLeaseScript = rueidis.NewLuaScript(`
local value = redis.call("GET", KEYS[1])
if not value or not string.find(value, ARGV[1], 1, true) then return 0 end
redis.call("SET", KEYS[1], ARGV[2], "EX", ARGV[3])
return 1
`)

var redisReleaseLeaseScript = rueidis.NewLuaScript(`
local value = redis.call("GET", KEYS[1])
if not value or not string.find(value, ARGV[1], 1, true) then return 0 end
redis.call("DEL", KEYS[1])
return 1
`)

func (s *RedisStore) CompleteLease(ctx context.Context, uid, endpoint, key, owner string,
	rec Record, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = recordTTL
	}
	payload, err := json.Marshal(&redisRecord{Status: rec.Status, ContentType: rec.ContentType,
		Body: rec.Body, Completed: true, CreatedAt: time.Now().UTC().UnixNano()})
	if err != nil {
		return fmt.Errorf("redis complete marshal: %w", err)
	}
	result, err := redisCompleteLeaseScript.Exec(ctx, s.client, []string{redisKey(uid, endpoint, key)},
		[]string{owner, string(payload), strconv.FormatInt(int64(ttl/time.Second), 10)}).ToInt64()
	if err != nil {
		return fmt.Errorf("redis complete lease: %w", err)
	}
	if result != 1 {
		return fmt.Errorf("redis complete lease: %w", ErrReservationLost)
	}
	return nil
}

func (s *RedisStore) ReleaseLease(ctx context.Context, uid, endpoint, key, owner string) error {
	result, err := redisReleaseLeaseScript.Exec(ctx, s.client, []string{redisKey(uid, endpoint, key)}, []string{owner}).ToInt64()
	if err != nil {
		return fmt.Errorf("redis release lease: %w", err)
	}
	if result != 1 {
		return fmt.Errorf("redis release lease: %w", ErrReservationLost)
	}
	return nil
}

func (s *RedisStore) Complete(ctx context.Context, uid, endpoint, key string, rec Record, ttl time.Duration) error {
	k := redisKey(uid, endpoint, key)
	if _, err := s.client.Do(ctx, s.client.B().Get().Key(k).Build()).AsBytes(); err != nil {
		if rueidis.IsRedisNil(err) {
			return fmt.Errorf("redis complete: %w", ErrReservationLost)
		}
		return fmt.Errorf("redis complete get reservation: %w", err)
	}
	if ttl <= 0 {
		ttl = recordTTL
	}
	rr := &redisRecord{
		Status:      rec.Status,
		ContentType: rec.ContentType,
		Body:        rec.Body,
		Completed:   true,
		CreatedAt:   time.Now().UTC().UnixNano(),
	}
	payload, err := json.Marshal(rr)
	if err != nil {
		return fmt.Errorf("redis complete marshal: %w", err)
	}
	cmd := s.client.B().Set().Key(k).Value(string(payload)).Ex(ttl).Build()
	if err := s.client.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("redis complete: %w", err)
	}
	return nil
}

// Release implements Store.Release. It deletes the idempotency key so a
// subsequent retry starts fresh. Redis errors remain observable; a failed
// delete is not equivalent to a released reservation.
func (s *RedisStore) Release(ctx context.Context, uid, endpoint, key string) error {
	k := redisKey(uid, endpoint, key)
	if err := s.client.Do(ctx, s.client.B().Del().Key(k).Build()).Error(); err != nil {
		return fmt.Errorf("redis release: %w", err)
	}
	return nil
}
