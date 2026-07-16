package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
)

// Server handles the HTTP requests and WebSocket upgrades
type Server struct {
	db     *DB
	hub    *Hub
	router *chi.Mux
}

// QueryRequest defines the structure for POST /query body
type QueryRequest struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params"`
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
	Error string `json:"error"`
}

// NewServer initializes the HTTP router and configures middleware
func NewServer(db *DB, hub *Hub) *Server {
	r := chi.NewRouter()

	// A custom simple slog logger middleware
	r.Use(middleware.RequestID)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
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
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	s := &Server{
		db:     db,
		hub:    hub,
		router: r,
	}

	r.Get("/health", s.handleHealth)
	r.Post("/query", s.handleQuery)
	r.Get("/ws", s.handleWS)

	return s
}

// ServeHTTP implements http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("Failed to parse request JSON", "err", err)
		s.writeError(w, http.StatusBadRequest, "Invalid JSON request body")
		return
	}

	if req.SQL == "" {
		s.writeError(w, http.StatusBadRequest, "SQL statement is required")
		return
	}

	// Dynamic routing based on statement type
	if IsReadQuery(req.SQL) {
		res, err := s.db.Query(req.SQL, req.Params...)
		if err != nil {
			slog.Error("Read query error", "sql", req.SQL, "err", err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(QueryResponse{
			Columns: res.Columns,
			Rows:    res.Rows,
		})
	} else {
		res, err := s.db.Exec(req.SQL, req.Params...)
		if err != nil {
			slog.Error("Write query error", "sql", req.SQL, "err", err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(QueryResponse{
			LastInsertID: res.LastInsertID,
			RowsAffected: res.RowsAffected,
		})
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all for development / API usage
	},
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) writeError(w http.ResponseWriter, statusCode int, msg string) {
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

// Start runs the HTTP server at the specified address
func (s *Server) Start(addr string) error {
	slog.Info("Starting Catena HTTP server", "addr", addr)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
