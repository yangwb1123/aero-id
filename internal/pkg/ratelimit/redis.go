package ratelimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/rueidis"
)

const redisRateScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return count
`

type RedisLimiter struct {
	client rueidis.Client
	rate   float64
	burst  int64
	window time.Duration
}

func NewRedisLimiter(client rueidis.Client, rate float64, burst int) *RedisLimiter {
	if client == nil || rate <= 0 || burst <= 0 {
		return nil
	}
	window := time.Duration(float64(time.Second) * float64(burst) / rate)
	if window < time.Second {
		window = time.Second
	}
	return &RedisLimiter{client: client, rate: rate, burst: int64(burst), window: window}
}

func (l *RedisLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	if l == nil || l.client == nil || l.burst <= 0 {
		return true, 0, nil
	}
	redisKey := l.key(key)
	count, err := l.client.Do(ctx, l.client.B().Eval().Script(redisRateScript).Numkeys(1).
		Key(redisKey).Arg(strconv.FormatInt(l.window.Milliseconds(), 10)).Build()).AsInt64()
	if err != nil {
		return false, 0, fmt.Errorf("redis rate limit increment: %w", err)
	}
	if count <= l.burst {
		return true, 0, nil
	}
	ttl, err := l.client.Do(ctx, l.client.B().Pttl().Key(redisKey).Build()).AsInt64()
	if err != nil {
		return false, 0, fmt.Errorf("redis rate limit ttl: %w", err)
	}
	if ttl < 1 {
		ttl = l.window.Milliseconds()
	}
	return false, time.Duration(ttl) * time.Millisecond, nil
}

func (l *RedisLimiter) key(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("aero-id:rate:%x", sum[:])
}

var _ DistributedLimiter = (*RedisLimiter)(nil)
