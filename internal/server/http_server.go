package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/amustaque97/eventra/internal/models"
	"github.com/amustaque97/eventra/internal/replication"
	"github.com/amustaque97/eventra/internal/store"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
)

// HTTPServer handles HTTP API requests
type HTTPServer struct {
	store      *store.EventStore
	replicator *replication.Replicator
	router     *mux.Router
	server     *http.Server
	logger     *zap.Logger
	nodeID     string
	httpPort   string
}

// NewHTTPServer creates a new HTTP server
func NewHTTPServer(
	nodeID string,
	httpPort string,
	store *store.EventStore,
	replicator *replication.Replicator,
	logger *zap.Logger,
) *HTTPServer {
	s := &HTTPServer{
		store:      store,
		replicator: replicator,
		logger:     logger,
		nodeID:     nodeID,
		httpPort:   httpPort,
	}

	s.setupRoutes()
	return s
}

// setupRoutes configures HTTP routes
func (s *HTTPServer) setupRoutes() {
	s.router = mux.NewRouter()

	// Client API endpoints
	s.router.HandleFunc("/events", s.handleWriteEvent).Methods("POST")
	s.router.HandleFunc("/events", s.handleReadEvents).Methods("GET")
	s.router.HandleFunc("/events/{aggregate_id}", s.handleReadEventsByAggregate).Methods("GET")

	// Replication endpoints (internal)
	s.router.HandleFunc("/internal/append-entries", s.handleAppendEntries).Methods("POST")

	// Health and status endpoints
	s.router.HandleFunc("/health", s.handleHealth).Methods("GET")
	s.router.HandleFunc("/status", s.handleStatus).Methods("GET")

	// Middleware
	s.router.Use(s.loggingMiddleware)
	s.router.Use(s.corsMiddleware)
}

// Start starts the HTTP server
func (s *HTTPServer) Start() error {
	s.server = &http.Server{
		Addr:         ":" + s.httpPort,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("starting HTTP server", zap.String("port", s.httpPort))
	return s.server.ListenAndServe()
}

// Stop stops the HTTP server
func (s *HTTPServer) Stop(ctx context.Context) error {
	s.logger.Info("stopping HTTP server")
	return s.server.Shutdown(ctx)
}

// handleWriteEvent handles write event requests
func (s *HTTPServer) handleWriteEvent(w http.ResponseWriter, r *http.Request) {
	// Check if this node is the leader
	if !s.replicator.IsLeader() {
		// Redirect to leader
		leaderID := s.replicator.GetLeaderID()
		if leaderID == "" {
			s.respondError(w, http.StatusServiceUnavailable, "no leader elected")
			return
		}

		leaderAddr := s.replicator.GetLeaderAddress()
		if leaderAddr == "" {
			s.respondError(w, http.StatusServiceUnavailable, "leader address unknown")
			return
		}

		// Return redirect response
		s.respondJSON(w, http.StatusTemporaryRedirect, map[string]string{
			"message":        "not the leader, redirect to leader",
			"leader_id":      leaderID,
			"leader_address": leaderAddr,
		})
		return
	}

	// Parse request
	var req models.WriteEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate request
	if req.Type == "" {
		s.respondError(w, http.StatusBadRequest, "event type is required")
		return
	}

	// Write event to local store
	term := s.store.GetCurrentTerm()
	resp, err := s.store.WriteEvent(&req, term)
	if err != nil {
		s.logger.Error("failed to write event", zap.Error(err))
		s.respondError(w, http.StatusInternalServerError, "failed to write event")
		return
	}

	// Replicate to followers
	if err := s.replicator.ReplicateEvent(resp.Index); err != nil {
		s.logger.Error("failed to replicate event", zap.Error(err))
		// Event is in WAL but not replicated - will be retried
	}

	s.respondJSON(w, http.StatusCreated, resp)
}

// handleReadEvents handles read events requests
func (s *HTTPServer) handleReadEvents(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	_ = r.URL.Query() // Reserved for future query parameter parsing

	req := &models.ReadEventsRequest{}

	// TODO: Parse fromIndex, toIndex, limit from query params

	resp, err := s.store.ReadEvents(req)
	if err != nil {
		s.logger.Error("failed to read events", zap.Error(err))
		s.respondError(w, http.StatusInternalServerError, "failed to read events")
		return
	}

	s.respondJSON(w, http.StatusOK, resp)
}

// handleReadEventsByAggregate handles reading events for a specific aggregate
func (s *HTTPServer) handleReadEventsByAggregate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	aggregateID := vars["aggregate_id"]

	req := &models.ReadEventsRequest{
		AggregateID: aggregateID,
	}

	resp, err := s.store.ReadEvents(req)
	if err != nil {
		s.logger.Error("failed to read events", zap.Error(err))
		s.respondError(w, http.StatusInternalServerError, "failed to read events")
		return
	}

	s.respondJSON(w, http.StatusOK, resp)
}

// handleAppendEntries handles replication requests from leader
func (s *HTTPServer) handleAppendEntries(w http.ResponseWriter, r *http.Request) {
	var req models.AppendEntriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp, err := s.store.AppendEntries(&req)
	if err != nil {
		s.logger.Error("failed to append entries", zap.Error(err))
		s.respondError(w, http.StatusInternalServerError, "failed to append entries")
		return
	}

	resp.FollowerID = s.nodeID
	s.respondJSON(w, http.StatusOK, resp)
}

// handleHealth handles health check requests
func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := &models.HealthResponse{
		Status:    "healthy",
		NodeID:    s.nodeID,
		IsLeader:  s.replicator.IsLeader(),
		LeaderID:  s.replicator.GetLeaderID(),
		Timestamp: time.Now(),
	}

	s.respondJSON(w, http.StatusOK, health)
}

// handleStatus handles status requests
func (s *HTTPServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := &models.NodeInfo{
		ID:            s.nodeID,
		IsLeader:      s.replicator.IsLeader(),
		LeaderID:      s.replicator.GetLeaderID(),
		Term:          s.store.GetCurrentTerm(),
		CommitIndex:   s.store.GetCommitIndex(),
		LastApplied:   s.store.GetLastApplied(),
		LastHeartbeat: time.Now(),
	}

	s.respondJSON(w, http.StatusOK, status)
}

// respondJSON sends a JSON response
func (s *HTTPServer) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// respondError sends an error response
func (s *HTTPServer) respondError(w http.ResponseWriter, status int, message string) {
	s.respondJSON(w, status, map[string]string{
		"error": message,
	})
}

// loggingMiddleware logs HTTP requests
func (s *HTTPServer) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Debug("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Duration("duration", time.Since(start)))
	})
}

// corsMiddleware adds CORS headers
func (s *HTTPServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
