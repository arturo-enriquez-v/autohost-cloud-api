package platform

import (
	"os"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient creates a Redis client from env vars.
// REDIS_ADDR defaults to localhost:6379.
func NewRedisClient() *redis.Client {
	addr := os.Getenv("REDIS_ADDR")

	return redis.NewClient(&redis.Options{
		Addr: addr,
	})
}
