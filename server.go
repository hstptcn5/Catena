package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
)

// Server handles the HTTP requests and WebSocket upgrades
type Server struct {
	db         *DB
	hub        *Hub
	router     *chi.Mux
	config     ServerConfig
	metrics    *Metrics
	httpServer *http.Server
	rateMu     sync.Mutex
	rateWindow map[string]rateBucket
}

// ServerConfig controls HTTP behavior and security.
type ServerConfig struct {
	APIKey          string
	ReadOnly        bool
	CORSOrigin      string
	BodyLimitBytes  int64
	QueryTimeout    time.Duration
	RateLimitPerMin int
	MaxRows         int
	BackupDir       string
}

type rateBucket struct {
	windowStart time.Time
	count       int
}

// QueryRequest defines the structure for POST /query body
type QueryRequest struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params"`
}

// BatchRequest defines the structure for POST /transaction body.
type BatchRequest struct {
	Statements []QueryRequest `json:"statements"`
}

// QueryResponse defines the structure for query outputs
type QueryResponse struct {
	Columns      []string `json:"columns,omitempty"`
	Rows         [][]any  `json:"rows,omitempty"`
	LastInsertID int64    `json:"last_insert_id,omitempty"`
	RowsAffected int64    `json:"rows_affected"`
}

// ErrorResponse defines uniform API error structures
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// NewServer initializes the HTTP router and configures middleware
func NewServer(db *DB, hub *Hub, config ServerConfig) *Server {
	if config.CORSOrigin == "" {
		config.CORSOrigin = "*"
	}
	if config.BodyLimitBytes <= 0 {
		config.BodyLimitBytes = 1 << 20
	}
	if config.QueryTimeout <= 0 {
		config.QueryTimeout = 30 * time.Second
	}
	if config.MaxRows <= 0 {
		config.MaxRows = 10000
	}
	if config.BackupDir == "" {
		config.BackupDir = "backups"
	}

	r := chi.NewRouter()
	metrics := NewMetrics()

	// A custom simple slog logger middleware
	r.Use(middleware.RequestID)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			metrics.IncHTTP(ww.Status())
			slog.Info("Request handled",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration", time.Since(start),
				"reqId", middleware.GetReqID(r.Context()),
			)
		})
	})
	r.Use(middleware.Recoverer)

	// Simple CORS Middleware
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", config.CORSOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	s := &Server{
		db:         db,
		hub:        hub,
		router:     r,
		config:     config,
		metrics:    metrics,
		rateWindow: make(map[string]rateBucket),
	}
	hub.metrics = metrics

	r.Get("/health", s.handleHealth)
	r.Get("/openapi.json", s.handleOpenAPI)
	r.Get("/", s.handleAdmin)
	r.Group(func(protected chi.Router) {
		protected.Use(s.authMiddleware)
		protected.Use(s.rateLimitMiddleware)
		protected.Post("/query", s.handleQuery)
		protected.Post("/transaction", s.handleTransaction)
		protected.Get("/export", s.handleExport)
		protected.Post("/backup", s.handleBackup)
		protected.Get("/metrics", s.handleMetrics)
		protected.Get("/ws", s.handleWS)
	})

	return s
}

// ServeHTTP implements http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": appVersion})
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req QueryRequest
	r.Body = http.MaxBytesReader(w, r.Body, s.config.BodyLimitBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("Failed to parse request JSON", "err", err)
		s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON request body", err.Error())
		return
	}

	kind, err := ClassifySQL(req.SQL)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_sql", err.Error(), "")
		return
	}
	if s.config.ReadOnly && kind == SQLWrite {
		s.writeError(w, http.StatusForbidden, "read_only", "Server is running in read-only mode", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.config.QueryTimeout)
	defer cancel()
	start := time.Now()

	// Dynamic routing based on statement type
	if kind == SQLRead {
		res, err := s.db.QueryContextLimit(ctx, req.SQL, s.config.MaxRows, req.Params...)
		if errors.Is(err, ErrRowLimitExceeded) {
			s.writeError(w, http.StatusUnprocessableEntity, "row_limit_exceeded", err.Error(), "")
			return
		}
		if err != nil {
			slog.Error("Read query error", "sql", req.SQL, "err", err)
			s.writeError(w, http.StatusInternalServerError, "query_failed", "Read query failed", err.Error())
			return
		}
		s.metrics.IncQuery(kind, time.Since(start))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(QueryResponse{
			Columns: res.Columns,
			Rows:    res.Rows,
		})
	} else {
		res, err := s.db.ExecContext(ctx, req.SQL, req.Params...)
		if err != nil {
			slog.Error("Write query error", "sql", req.SQL, "err", err)
			s.writeError(w, http.StatusInternalServerError, "query_failed", "Write query failed", err.Error())
			return
		}
		s.metrics.IncQuery(kind, time.Since(start))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(QueryResponse{
			LastInsertID: res.LastInsertID,
			RowsAffected: res.RowsAffected,
		})
	}
}

func (s *Server) handleTransaction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.config.ReadOnly {
		s.writeError(w, http.StatusForbidden, "read_only", "Server is running in read-only mode", "")
		return
	}

	var req BatchRequest
	r.Body = http.MaxBytesReader(w, r.Body, s.config.BodyLimitBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON request body", err.Error())
		return
	}
	if len(req.Statements) == 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "At least one statement is required", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.config.QueryTimeout)
	defer cancel()

	statements := make([]ExecStatement, 0, len(req.Statements))
	for _, stmt := range req.Statements {
		kind, err := ClassifySQL(stmt.SQL)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_sql", err.Error(), "")
			return
		}
		if kind == SQLRead {
			s.writeError(w, http.StatusBadRequest, "invalid_sql", "Transaction statements must be writes", "")
			return
		}
		statements = append(statements, ExecStatement{SQL: stmt.SQL, Params: stmt.Params})
	}

	results, err := s.db.ExecBatchContext(ctx, statements)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "transaction_failed", "Transaction failed and was rolled back", err.Error())
		return
	}
	s.metrics.IncTransaction()

	responses := make([]QueryResponse, 0, len(results))
	for _, res := range results {
		responses = append(responses, QueryResponse{LastInsertID: res.LastInsertID, RowsAffected: res.RowsAffected})
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"results": responses})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.QueryTimeout)
	defer cancel()

	var buf bytes.Buffer
	if err := s.db.Export(ctx, &buf); err != nil {
		slog.Error("Database export failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "export_failed", "Database export failed", err.Error())
		return
	}
	s.metrics.IncExport()

	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="catena-export.db"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), s.config.QueryTimeout)
	defer cancel()

	path, err := s.db.Backup(ctx, s.config.BackupDir)
	if err != nil {
		slog.Error("Database backup failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "backup_failed", "Database backup failed", err.Error())
		return
	}
	s.metrics.IncBackup()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"path": path})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	payload, err := s.metrics.JSON()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "metrics_failed", "Failed to encode metrics", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(payload)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all for development / API usage
	},
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.isAllowedOrigin(r.Header.Get("Origin")) {
		s.writeError(w, http.StatusForbidden, "origin_forbidden", "WebSocket origin is not allowed", "")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("Failed to upgrade connection to WebSocket", "err", err)
		return
	}

	client := &Client{
		hub:           s.hub,
		conn:          conn,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	client.hub.register <- client

	// Start reading and writing asynchronously for this client connection
	go client.WritePump()
	go client.ReadPump()
}

func (s *Server) isAllowedOrigin(origin string) bool {
	if s.config.CORSOrigin == "*" || origin == "" {
		return true
	}
	return origin == s.config.CORSOrigin
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(openAPISpec))
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(adminHTML))
}

func (s *Server) writeError(w http.ResponseWriter, statusCode int, code, msg, details string) {
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{Code: code, Message: msg, Details: details})
}

// Start runs the HTTP server at the specified address
func (s *Server) Start(addr string) error {
	slog.Info("Starting Catena HTTP server", "addr", addr)
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.config.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth {
			token = r.Header.Get("X-API-Key")
		}
		// Browser WebSocket clients cannot set arbitrary Authorization headers.
		// Limit query-string credentials to the WebSocket handshake only.
		if token == "" && r.URL.Path == "/ws" {
			token = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.APIKey)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "Valid API key is required", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.config.RateLimitPerMin <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		key := r.RemoteAddr
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			key = host
		}
		if host := r.Header.Get("X-Forwarded-For"); host != "" {
			key = strings.TrimSpace(strings.Split(host, ",")[0])
		}
		now := time.Now()
		s.rateMu.Lock()
		bucket := s.rateWindow[key]
		if now.Sub(bucket.windowStart) >= time.Minute {
			bucket = rateBucket{windowStart: now}
		}
		bucket.count++
		s.rateWindow[key] = bucket
		allowed := bucket.count <= s.config.RateLimitPerMin
		s.rateMu.Unlock()
		if !allowed {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(time.Until(bucket.windowStart.Add(time.Minute)).Seconds())))
			s.writeError(w, http.StatusTooManyRequests, "rate_limited", "Rate limit exceeded", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
