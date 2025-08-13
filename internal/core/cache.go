package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/InsulaLabs/insi/client"
)

type CacheController[T ~string] interface {
	PushScope(prefix string) error
	PopScope() error
	GetPrefix() string

	Get(ctx context.Context, key string) (T, error)
	Set(ctx context.Context, key string, value T) error
	SetNX(ctx context.Context, key string, value T) error
	Delete(ctx context.Context, key string) error
	CompareAndSwap(ctx context.Context, key string, oldValue, newValue T) error
	IterateByPrefix(ctx context.Context, prefix string, offset, limit int) ([]string, error)
}

type ccImpl[T ~string] struct {
	defaultT T
	client   *client.Client
	logger   *slog.Logger

	scopes       []string
	prefix       string
	scopeChanged atomic.Bool
}

func NewCacheController[T ~string](defaultT T, c *client.Client, logger *slog.Logger) CacheController[T] {
	return &ccImpl[T]{
		defaultT: defaultT,
		client:   c,
		logger:   logger.WithGroup("cache_controller"),
	}
}

func (cc *ccImpl[T]) buildPrefix(key string) string {
	if len(cc.scopes) == 0 {
		return key
	}
	if !cc.scopeChanged.Load() {
		return fmt.Sprintf("%s:%s", cc.prefix, key)
	}

	cc.prefix = strings.Join(cc.scopes, ":")
	cc.scopeChanged.Store(false)
	return fmt.Sprintf("%s:%s", cc.prefix, key)
}

func (cc *ccImpl[T]) GetPrefix() string {
	return cc.prefix
}

func (cc *ccImpl[T]) PushScope(prefix string) error {
	if prefix == "" {
		return ErrEmptyScopePush
	}

	cc.scopes = append(cc.scopes, prefix)
	cc.scopeChanged.Store(true)
	return nil
}

func (cc *ccImpl[T]) PopScope() error {
	if len(cc.scopes) == 0 {
		return ErrEndScope
	}

	cc.scopes = cc.scopes[:len(cc.scopes)-1]
	cc.scopeChanged.Store(true)
	return nil
}

func (cc *ccImpl[T]) Get(ctx context.Context, key string) (T, error) {
	var result T

	fullKey := cc.buildPrefix(key)

	err := client.WithRetriesVoid(ctx, cc.logger, func() error {
		valueStr, err := cc.client.GetCache(fullKey)
		if err != nil {
			if errors.Is(err, client.ErrKeyNotFound) {
				result = cc.defaultT
				return nil
			}
			return err
		}

		result = T(valueStr)
		return nil
	})

	return result, translateError(err)
}

func (cc *ccImpl[T]) Set(ctx context.Context, key string, value T) error {
	err := client.WithRetriesVoid(ctx, cc.logger, func() error {
		fullKey := cc.buildPrefix(key)
		return cc.client.SetCache(fullKey, string(value))
	})

	return translateError(err)
}

func (cc *ccImpl[T]) SetNX(ctx context.Context, key string, value T) error {
	err := client.WithRetriesVoid(ctx, cc.logger, func() error {
		fullKey := cc.buildPrefix(key)
		return cc.client.SetCacheNX(fullKey, string(value))
	})

	return translateError(err)
}

func (cc *ccImpl[T]) Delete(ctx context.Context, key string) error {
	err := client.WithRetriesVoid(ctx, cc.logger, func() error {
		fullKey := cc.buildPrefix(key)
		return cc.client.DeleteCache(fullKey)
	})

	return translateError(err)
}

func (cc *ccImpl[T]) CompareAndSwap(ctx context.Context, key string, oldValue, newValue T) error {
	err := client.WithRetriesVoid(ctx, cc.logger, func() error {
		fullKey := cc.buildPrefix(key)
		return cc.client.CompareAndSwapCache(fullKey, string(oldValue), string(newValue))
	})

	return translateError(err)
}

func (cc *ccImpl[T]) IterateByPrefix(ctx context.Context, prefix string, offset, limit int) ([]string, error) {
	var result []string

	err := client.WithRetriesVoid(ctx, cc.logger, func() error {
		fullPrefix := cc.buildPrefix(prefix)
		keys, err := cc.client.IterateCacheByPrefix(fullPrefix, offset, limit)
		if err != nil {
			return err
		}

		scopePrefix := ""
		if len(cc.scopes) > 0 {
			scopePrefix = strings.Join(cc.scopes, ":") + ":"
		}

		result = make([]string, 0, len(keys))
		for _, key := range keys {
			if strings.HasPrefix(key, scopePrefix) {
				result = append(result, strings.TrimPrefix(key, scopePrefix))
			} else {
				result = append(result, key)
			}
		}
		return nil
	})

	return result, translateError(err)
}
