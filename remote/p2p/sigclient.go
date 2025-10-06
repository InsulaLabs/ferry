package p2p

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	AuthHeader = "Authorization"
)

type signalClientOptions struct {
	skipVerify bool
	token      string
	url        string
	logger     *slog.Logger
}

type signalClient struct {
	skipVerify bool
	token      string
	url        string
	httpClient *http.Client
	logger     *slog.Logger
}

func newSignalClient(options signalClientOptions) *signalClient {
	if options.logger == nil {
		options.logger = slog.Default()
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: options.skipVerify,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}

	options.logger.Debug("newSignalClient", "url", options.url)
	return &signalClient{
		logger:     options.logger,
		skipVerify: options.skipVerify,
		token:      options.token,
		url:        options.url,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
	}
}

// doRequest is a helper function to make HTTP requests
func (c *signalClient) doRequest(method, endpoint string, body interface{}, response interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			c.logger.Error("failed to marshal request body", "error", err)
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	url := c.url + endpoint
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		c.logger.Error("failed to create request", "error", err)
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set(AuthHeader, c.token)
	}

	c.logger.Debug("Making request", "method", method, "url", url)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("request failed", "error", err)
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("unexpected status code", "status", resp.StatusCode)
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if response != nil {
		if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
			c.logger.Error("failed to decode response", "error", err)
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

func (c *signalClient) registerOffer(offerID string, data *SignalingData) error {
	req := ConnectDetails{
		OfferID:    offerID,
		SignalData: data,
	}
	return c.doRequest("POST", RouteOffer, req, nil)
}

func (c *signalClient) waitForAnswer(offerID string) (*SignalingData, error) {
	req := AnswerRequest{
		OfferID: offerID,
	}

	var result SignalDataResponse
	if err := c.doRequest("POST", RouteAnswer, req, &result); err != nil {
		return nil, err
	}

	if result.SignalData == nil {
		return nil, fmt.Errorf("no signal data received")
	}

	return result.SignalData, nil
}

func (c *signalClient) getOffer(offerID string) (*SignalingData, error) {
	req := ConnectDetails{
		OfferID:    offerID,
		SignalData: nil,
	}

	var result SignalDataResponse
	if err := c.doRequest("POST", RouteConnect, req, &result); err != nil {
		return nil, err
	}

	return result.SignalData, nil
}

func (c *signalClient) sendAnswer(offerID string, data *SignalingData) error {
	req := ConnectDetails{
		OfferID:    offerID,
		SignalData: data,
	}

	return c.doRequest("POST", RouteConnect, req, nil)
}
