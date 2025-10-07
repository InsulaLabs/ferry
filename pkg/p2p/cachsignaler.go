package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/InsulaLabs/ferry/pkg/core"
	"github.com/InsulaLabs/insi/db/models"
)

type CacheSignalingClient struct {
	cacheController core.CacheController[string]
	events          core.Events
	logger          *slog.Logger
	timeout         time.Duration
}

type CacheSignalingConfig struct {
	CacheController core.CacheController[string]
	Events          core.Events
	Logger          *slog.Logger
	Timeout         time.Duration
}

func NewCacheSignalingClient(config CacheSignalingConfig) *CacheSignalingClient {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Minute
	}

	return &CacheSignalingClient{
		cacheController: config.CacheController,
		events:          config.Events,
		logger:          config.Logger,
		timeout:         config.Timeout,
	}
}

func (c *CacheSignalingClient) makeOfferKey(sessionID string) string {
	return fmt.Sprintf("p2p:offer:%s", sessionID)
}

func (c *CacheSignalingClient) makeAnswerKey(sessionID string) string {
	return fmt.Sprintf("p2p:answer:%s", sessionID)
}

func (c *CacheSignalingClient) makeOfferEventTopic(sessionID string) string {
	return fmt.Sprintf("p2p:offer:event:%s", sessionID)
}

func (c *CacheSignalingClient) makeAnswerEventTopic(sessionID string) string {
	return fmt.Sprintf("p2p:answer:event:%s", sessionID)
}

func (c *CacheSignalingClient) RegisterOffer(ctx context.Context, sessionID string, data *SignalingData) error {
	key := c.makeOfferKey(sessionID)
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal offer data: %w", err)
	}

	if err := c.cacheController.Set(ctx, key, string(jsonData)); err != nil {
		return err
	}

	topic := c.makeOfferEventTopic(sessionID)
	publisher := c.events.GetPublisher(topic)
	if err := publisher.Publish(ctx, "ready"); err != nil {
		c.logger.Warn("failed to publish offer ready event", "error", err, "session_id", sessionID)
	}

	return nil
}

func (c *CacheSignalingClient) WaitForAnswer(ctx context.Context, sessionID string) (*SignalingData, error) {
	key := c.makeAnswerKey(sessionID)
	topic := c.makeAnswerEventTopic(sessionID)

	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var result *SignalingData
	var resultErr error
	var wg sync.WaitGroup
	wg.Add(1)

	subscriber := c.events.GetSubscriber(topic)

	go func() {
		defer wg.Done()

		err := subscriber.Subscribe(timeoutCtx, func(event models.EventPayload) {
			value, err := c.cacheController.Get(ctx, key)
			if err != nil {
				c.logger.Error("failed to get answer from cache after event", "error", err, "session_id", sessionID)
				resultErr = fmt.Errorf("failed to get answer: %w", err)
				cancel()
				return
			}

			var data SignalingData
			if err := json.Unmarshal([]byte(value), &data); err != nil {
				c.logger.Error("failed to unmarshal answer data", "error", err, "session_id", sessionID)
				resultErr = fmt.Errorf("failed to unmarshal answer data: %w", err)
				cancel()
				return
			}

			if err := c.cacheController.Delete(ctx, key); err != nil {
				c.logger.Warn("failed to clean up answer from cache", "error", err)
			}

			result = &data
			cancel()
		})

		if err != nil && err != context.Canceled {
			c.logger.Error("subscription failed", "error", err, "session_id", sessionID)
			resultErr = fmt.Errorf("subscription failed: %w", err)
		}
	}()

	wg.Wait()

	if resultErr != nil {
		return nil, resultErr
	}

	if result == nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("timeout waiting for answer")
		}
		return nil, fmt.Errorf("no answer received")
	}

	return result, nil
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

	if err := c.cacheController.Set(ctx, key, string(jsonData)); err != nil {
		return err
	}

	topic := c.makeAnswerEventTopic(sessionID)
	publisher := c.events.GetPublisher(topic)
	if err := publisher.Publish(ctx, "ready"); err != nil {
		c.logger.Warn("failed to publish answer ready event", "error", err, "session_id", sessionID)
	}

	return nil
}

func (c *CacheSignalingClient) WaitForOffer(ctx context.Context, sessionID string) (*SignalingData, error) {
	key := c.makeOfferKey(sessionID)
	topic := c.makeOfferEventTopic(sessionID)

	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var result *SignalingData
	var resultErr error
	var wg sync.WaitGroup
	wg.Add(1)

	subscriber := c.events.GetSubscriber(topic)

	go func() {
		defer wg.Done()

		err := subscriber.Subscribe(timeoutCtx, func(event models.EventPayload) {
			value, err := c.cacheController.Get(ctx, key)
			if err != nil {
				c.logger.Error("failed to get offer from cache after event", "error", err, "session_id", sessionID)
				resultErr = fmt.Errorf("failed to get offer: %w", err)
				cancel()
				return
			}

			var data SignalingData
			if err := json.Unmarshal([]byte(value), &data); err != nil {
				c.logger.Error("failed to unmarshal offer data", "error", err, "session_id", sessionID)
				resultErr = fmt.Errorf("failed to unmarshal offer data: %w", err)
				cancel()
				return
			}

			result = &data
			cancel()
		})

		if err != nil && err != context.Canceled {
			c.logger.Error("subscription failed", "error", err, "session_id", sessionID)
			resultErr = fmt.Errorf("subscription failed: %w", err)
		}
	}()

	wg.Wait()

	if resultErr != nil {
		return nil, resultErr
	}

	if result == nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("timeout waiting for offer")
		}
		return nil, fmt.Errorf("no offer received")
	}

	return result, nil
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
