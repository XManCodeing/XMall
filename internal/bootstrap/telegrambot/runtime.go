package telegrambot

import (
	"fmt"
	"github.com/XManCodeing/XMall/internal/config"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimeapi "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/botapi"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/productmedia"
	"github.com/redis/go-redis/v9"
	"strings"
	"time"
)

// NewLeaseClient uses the queue's Redis, not the optional application cache.
// No connection is opened or goroutine started during dependency construction.
func NewLeaseClient(cfg config.QueueConfig) *redis.Client {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port <= 0 {
		port = 6379
	}
	return redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", host, port), Password: cfg.Password, DB: cfg.DB, ContextTimeoutEnabled: true, DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, MaxRetries: -1})
}
func NewManager(store runtimecontract.Store, secret string) *runtimeapp.Manager {
	return runtimeapp.NewManager(store, runtimeapi.Adapter{}, secret)
}

// NewManagerWithMedia shares the upload root supplied by the composition root.
func NewManagerWithMedia(store runtimecontract.Store, secret, uploadRoot string) *runtimeapp.Manager {
	cache, _ := store.(productmedia.Cache)
	return runtimeapp.NewManagerWithMediaReader(store, productmedia.New(runtimeapi.Adapter{}, uploadRoot, cache), secret, func(ref string, maxBytes int64) ([]byte, error) {
		return productmedia.ReadUploadFile(uploadRoot, ref, maxBytes)
	})
}
