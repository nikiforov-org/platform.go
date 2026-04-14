// internal/services/xhttp/cache.go
package xhttp

import (
	"context"

	"platform/internal/platform/nc"

	"github.com/rs/zerolog"
)

// cache — тонкая обёртка над NATS KV для работы с кэшем сервиса.
// Все ошибки логируются и не прерывают основной поток: кэш деградирует
// до сквозных запросов в PostgreSQL без потери функциональности.
type cache struct {
	nc     *nc.PlatformClient
	bucket string
	log    zerolog.Logger
}

// newCache создаёт экземпляр кэша для указанного KV-бакета.
func newCache(nc *nc.PlatformClient, bucket string, log zerolog.Logger) *cache {
	return &cache{nc: nc, bucket: bucket, log: log}
}

// Get возвращает значение из KV-кэша по ключу.
// Возвращает nil при промахе или любой ошибке.
func (c *cache) Get(ctx context.Context, key string) []byte {
	val, err := c.nc.GetValue(ctx, c.bucket, key)
	if err != nil {
		c.log.Error().Err(err).Str("key", key).Msg("cache.Get")
		return nil
	}
	return val
}

// Put записывает значение в KV-кэш.
func (c *cache) Put(ctx context.Context, key string, val []byte) {
	if err := c.nc.PutValue(ctx, c.bucket, key, val); err != nil {
		c.log.Error().Err(err).Str("key", key).Msg("cache.Put")
	}
}

// Invalidate помечает ключи как недействительные через перезапись пустым значением.
// NATS KV API не гарантирует наличия Delete во всех версиях Go SDK,
// поэтому используем маркер: пустой []byte распознаётся как промах в Get.
func (c *cache) Invalidate(ctx context.Context, keys ...string) {
	for _, key := range keys {
		if err := c.nc.PutValue(ctx, c.bucket, key, []byte{}); err != nil {
			c.log.Error().Err(err).Str("key", key).Msg("cache.Invalidate")
		}
	}
}
