package db

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ConnectRedis returns a client used for the public API rate limiter and
// as a hot cache in front of Postgres (e.g. active watch-address lookups).
// Postgres remains the durable source of truth for chain cursors and
// order state; Redis is a performance/rate-limiting layer only, so a
// flushed cache never causes a missed or double-processed deposit.
func ConnectRedis(ctx context.Context, addr string, dbIndex int) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr: addr,
		DB:   dbIndex,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	return client, nil
}
