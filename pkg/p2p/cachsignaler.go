package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/InsulaLabs/ferry/pkg/core"
)

type CacheSignalingClient struct {
	cacheController core.CacheController[string]
	logger          *slog.Logger
	pollInterval    time.Duration
	timeout         time.Duration
}

type CacheSignalingConfig struct {
	CacheController core.CacheController[string]
	Logger          *slog.Logger
	PollInterval    time.Duration
	Timeout         time.Duration
}

func NewCacheSignalingClient(config CacheSignalingConfig) *CacheSignalingClient {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.PollInterval == 0 {
		config.PollInterval = 500 * time.Millisecond
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Minute
	}

	return &CacheSignalingClient{
		cacheController: config.CacheController,
		logger:          config.Logger,
		pollInterval:    config.PollInterval,
		timeout:         config.Timeout,
	}
}

func (c *CacheSignalingClient) makeOfferKey(sessionID string) string {
	return fmt.Sprintf("p2p:offer:%s", sessionID)
}

func (c *CacheSignalingClient) makeAnswerKey(sessionID string) string {
	return fmt.Sprintf("p2p:answer:%s", sessionID)
}

func (c *CacheSignalingClient) RegisterOffer(ctx context.Context, sessionID string, data *SignalingData) error {
	key := c.makeOfferKey(sessionID)
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal offer data: %w", err)
	}

	return c.cacheController.Set(ctx, key, string(jsonData))
}

func (c *CacheSignalingClient) WaitForAnswer(ctx context.Context, sessionID string) (*SignalingData, error) {
	key := c.makeAnswerKey(sessionID)
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("timeout waiting for answer")
		case <-ticker.C:
			value, err := c.cacheController.Get(timeoutCtx, key)
			if err != nil {
				if err == core.ErrKeyNotFound {
					continue
				}
				return nil, fmt.Errorf("failed to get answer: %w", err)
			}

			if value == "" || len(strings.TrimSpace(value)) == 0 {
				c.logger.Debug("answer key exists but value is empty, continuing to poll", "key", key)
				continue
			}

			var data SignalingData
			if err := json.Unmarshal([]byte(value), &data); err != nil {
				previewLen := 100
				if len(value) < previewLen {
					previewLen = len(value)
				}
				c.logger.Warn("failed to unmarshal answer data, continuing to poll", "error", err, "value_length", len(value), "value_preview", value[:previewLen])
				continue
			}

			if err := c.cacheController.Delete(timeoutCtx, key); err != nil {
				c.logger.Warn("failed to clean up answer from cache", "error", err)
			}

			return &data, nil
		}
	}
}

func (c *CacheSignalingClient) GetOffer(ctx context.Context, sessionID string) (*SignalingData, error) {
	key := c.makeOfferKey(sessionID)
	value, err := c.cacheController.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get offer: %w", err)
	}

	var data SignalingData
	if err := json.Unmarshal([]byte(value), &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal offer data: %w", err)
	}

	return &data, nil
}

func (c *CacheSignalingClient) SendAnswer(ctx context.Context, sessionID string, data *SignalingData) error {
	key := c.makeAnswerKey(sessionID)
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal answer data: %w", err)
	}

	return c.cacheController.Set(ctx, key, string(jsonData))
}

func (c *CacheSignalingClient) WaitForOffer(ctx context.Context, sessionID string) (*SignalingData, error) {
	key := c.makeOfferKey(sessionID)
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("timeout waiting for offer")
		case <-ticker.C:
			value, err := c.cacheController.Get(timeoutCtx, key)
			if err != nil {
				if err == core.ErrKeyNotFound {
					continue
				}
				return nil, fmt.Errorf("failed to get offer: %w", err)
			}

			if value == "" || len(strings.TrimSpace(value)) == 0 {
				c.logger.Debug("offer key exists but value is empty, continuing to poll", "key", key)
				continue
			}

			var data SignalingData
			if err := json.Unmarshal([]byte(value), &data); err != nil {
				previewLen := 100
				if len(value) < previewLen {
					previewLen = len(value)
				}
				c.logger.Warn("failed to unmarshal offer data, continuing to poll", "error", err, "value_length", len(value), "value_preview", value[:previewLen])
				continue
			}

			return &data, nil
		}
	}
}

func (c *CacheSignalingClient) CleanupOffer(ctx context.Context, sessionID string) {
	offerKey := c.makeOfferKey(sessionID)
	if err := c.cacheController.Delete(ctx, offerKey); err != nil {
		c.logger.Debug("failed to cleanup offer", "session_id", sessionID, "error", err)
	}
}

func (c *CacheSignalingClient) CleanupAnswer(ctx context.Context, sessionID string) {
	answerKey := c.makeAnswerKey(sessionID)
	if err := c.cacheController.Delete(ctx, answerKey); err != nil {
		c.logger.Debug("failed to cleanup answer", "session_id", sessionID, "error", err)
	}
}

func (c *CacheSignalingClient) Cleanup(ctx context.Context, sessionID string) {
	c.CleanupOffer(ctx, sessionID)
	c.CleanupAnswer(ctx, sessionID)
}
