// Package ratelimit implements a simple fixed-window rate limiter backed
// by Redis, used to bound the public HTTP API. Redis being unavailable
// fails open (requests are allowed) rather than taking the API down —
// rate limiting protects against abuse, it is not part of the payment
// correctness path.
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limiter struct {
	redis  *redis.Client
	limit  int
	window time.Duration
	logger *slog.Logger
}

func NewLimiter(client *redis.Client, limit int, window time.Duration, logger *slog.Logger) *Limiter {
	return &Limiter{redis: client, limit: limit, window: window, logger: logger}
}

// Allow reports whether the caller identified by key may proceed. On a
// Redis error, it logs and allows the request rather than blocking
// traffic on a caching-layer outage.
func (l *Limiter) Allow(ctx context.Context, key string) bool {
	windowID := time.Now().Unix() / int64(l.window.Seconds())
	redisKey := fmt.Sprintf("ratelimit:%s:%d", key, windowID)

	count, err := l.redis.Incr(ctx, redisKey).Result()
	if err != nil {
		l.logger.Warn("rate limiter redis error; failing open", "error", err)
		return true
	}
	if count == 1 {
		l.redis.Expire(ctx, redisKey, l.window)
	}
	return count <= int64(l.limit)
}
