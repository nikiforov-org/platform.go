// internal/services/xhttp/cache.go
package xhttp

import (
	"context"
	"log"

	"platform/internal/platform/natsclient"
)

// cache — тонкая обёртка над NATS KV для работы с кэшем сервиса.
// Все ошибки логируются и не прерывают основной поток: кэш деградирует
// до сквозных запросов в PostgreSQL без потери функциональности.
type cache struct {
	nc     *natsclient.PlatformClient
	bucket string
}

// newCache создаёт экземпляр кэша для указанного KV-бакета.
func newCache(nc *natsclient.PlatformClient, bucket string) *cache {
	return &cache{nc: nc, bucket: bucket}
}

// Get возвращает значение из KV-кэша по ключу.
// Возвращает nil при промахе или любой ошибке.
func (c *cache) Get(ctx context.Context, key string) []byte {
	val, err := c.nc.GetValue(ctx, c.bucket, key)
	if err != nil {
		log.Printf("http-ms: cache.Get %q: %v", key, err)
		return nil
	}
	return val
}

// Put записывает значение в KV-кэш.
func (c *cache) Put(ctx context.Context, key string, val []byte) {
	if err := c.nc.PutValue(ctx, c.bucket, key, val); err != nil {
		log.Printf("http-ms: cache.Put %q: %v", key, err)
	}
}

// Invalidate помечает ключи как недействительные через перезапись пустым значением.
// NATS KV API не гарантирует наличия Delete во всех версиях Go SDK,
// поэтому используем маркер: пустой []byte распознаётся как промах в Get.
func (c *cache) Invalidate(ctx context.Context, keys ...string) {
	for _, key := range keys {
		if err := c.nc.PutValue(ctx, c.bucket, key, []byte{}); err != nil {
			log.Printf("http-ms: cache.Invalidate %q: %v", key, err)
		}
	}
}
