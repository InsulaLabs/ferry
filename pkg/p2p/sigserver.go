package p2p

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	RouteOffer   = "/offer"
	RouteAnswer  = "/answer"
	RouteConnect = "/connect"
)

type AnswerRequest struct {
	OfferID string `json:"offer_id"`
}

type SignalDataResponse struct {
	SignalData *SignalingData `json:"signal_data"`
}

type ConnectDetails struct {
	OfferID    string         `json:"offer_id"`
	SignalData *SignalingData `json:"signal_data,omitempty"`
}

type connection struct {
	logger     *slog.Logger
	offerData  *SignalingData
	answerData *SignalingData
	answered   chan struct{}
	created    time.Time
	ctx        context.Context
	cancel     context.CancelFunc
}

type SignalingServer struct {
	logger            *slog.Logger
	mu                sync.RWMutex
	connections       map[string]*connection
	cleanup           *time.Ticker
	tokenValidationCb func(token string) bool

	offerTimeout  time.Duration
	answerTimeout time.Duration
}

type TokenValidationCb func(token string) bool

type SignalingServerConfig struct {
	Logger            *slog.Logger
	TokenValidationCb TokenValidationCb
	CleanupInterval   time.Duration
	OfferTimeout      time.Duration
	AnswerTimeout     time.Duration
}

func NewSignalingServer(config SignalingServerConfig) *SignalingServer {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	if config.CleanupInterval <= 0 {
		config.Logger.Warn("cleanup interval is too short, setting to 1 minute")
		config.CleanupInterval = 1 * time.Minute
	}

	if config.OfferTimeout <= 0 {
		config.Logger.Warn("offer timeout is too short, setting to 5 minutes")
		config.OfferTimeout = 5 * time.Minute
	}

	if config.AnswerTimeout <= 0 {
		config.Logger.Warn("answer timeout is too short, setting to 30 seconds")
		config.AnswerTimeout = 30 * time.Second
	}

	s := &SignalingServer{
		logger:            config.Logger,
		connections:       make(map[string]*connection),
		cleanup:           time.NewTicker(config.CleanupInterval),
		tokenValidationCb: config.TokenValidationCb,
		offerTimeout:      config.OfferTimeout,
		answerTimeout:     config.AnswerTimeout,
	}
	return s
}

func (s *SignalingServer) Start() error {
	go s.cleanupLoop()
	return nil
}

func (s *SignalingServer) Stop() error {
	s.cleanup.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.connections {
		conn.cancel()
	}
	s.connections = make(map[string]*connection)
	return nil
}

func (s *SignalingServer) GetRouteBinder() func(mux *http.ServeMux) error {
	return s.bindRoutes
}

func (x *SignalingServer) bindRoutes(mux *http.ServeMux) error {

	mux.HandleFunc(RouteOffer, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		token := r.Header.Get(AuthHeader)
		if !x.tokenValidationCb(token) {
			x.logger.Error("invalid token")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		x.logger.Debug("received offer - token validated")

		var req ConnectDetails
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			x.logger.Error("failed to decode body", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithCancel(context.Background())
		conn := &connection{
			offerData: req.SignalData,
			answered:  make(chan struct{}),
			created:   time.Now(),
			ctx:       ctx,
			cancel:    cancel,
			logger:    x.logger.With("offer_id", req.OfferID),
		}

		x.mu.Lock()
		x.connections[req.OfferID] = conn
		x.mu.Unlock()
		x.logger.Debug("offer added")
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc(RouteAnswer, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		token := r.Header.Get(AuthHeader)
		if !x.tokenValidationCb(token) {
			x.logger.Error("invalid token")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		x.logger.Debug("received answer - token validated")

		var req AnswerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			x.logger.Error("failed to decode body", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		x.mu.RLock()
		conn, exists := x.connections[req.OfferID]
		x.mu.RUnlock()

		if !exists {
			x.logger.Error("connection not found")
			http.Error(w, "connection not found", http.StatusNotFound)
			return
		}

		select {
		case <-conn.answered:
			x.logger.Debug("answer received")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(SignalDataResponse{SignalData: conn.answerData})
			return
		case <-conn.ctx.Done():
			x.logger.Error("connection closed")
			http.Error(w, "connection closed", http.StatusGone)
			return
		case <-time.After(x.answerTimeout):
			x.logger.Error("timeout waiting for answer")
			http.Error(w, "timeout waiting for answer", http.StatusRequestTimeout)
			return
		}
	})

	mux.HandleFunc(RouteConnect, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		token := r.Header.Get(AuthHeader)
		if !x.tokenValidationCb(token) {
			x.logger.Error("invalid token")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		var req ConnectDetails
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			x.logger.Error("failed to decode body", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		x.mu.Lock()
		defer x.mu.Unlock()

		conn, exists := x.connections[req.OfferID]
		if !exists {
			x.logger.Error("connection not found")
			http.Error(w, "connection not found", http.StatusNotFound)
			return
		}

		// If the signal data is nil, we are getting a request for the offer
		if req.SignalData == nil {
			if conn.offerData == nil {
				x.logger.Error("offer data not available")
				http.Error(w, "offer data not available", http.StatusNotFound)
				return
			}
			data := conn.offerData
			conn.offerData = nil
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(SignalDataResponse{SignalData: data})
			return
		}

		// Client sent the answer
		conn.answerData = req.SignalData
		close(conn.answered)
		w.WriteHeader(http.StatusOK)
	})

	return nil
}

func (s *SignalingServer) cleanupLoop() {
	for range s.cleanup.C {
		s.logger.Debug("running cleanup loop")
		toDelete := make([]string, 0)

		// Identify expired connections
		s.mu.RLock()
		now := time.Now()
		for id, conn := range s.connections {
			timeout := s.offerTimeout
			if conn.answerData != nil {
				timeout = s.answerTimeout
			}
			if now.Sub(conn.created) > timeout {
				toDelete = append(toDelete, id)
			}
		}
		s.mu.RUnlock()

		// Delete expired connections
		if len(toDelete) > 0 {
			s.logger.Debug("deleting expired connections", "count", len(toDelete))
			s.mu.Lock()
			for _, id := range toDelete {
				if conn, exists := s.connections[id]; exists {
					conn.cancel()
					delete(s.connections, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
