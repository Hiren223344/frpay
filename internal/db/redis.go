package db

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ConnectRedis returns a client used only as a rate-limiting cache.
// Postgres remains the durable source of truth for chain cursors and
// order state, so a flushed Redis instance never causes a missed or
// double-processed transfer.
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
