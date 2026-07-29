package main

import (
	"encoding/json"
	"sync"
	"time"
)

// Metrics stores lightweight process-local counters for operational visibility.
type Metrics struct {
	mu sync.RWMutex

	StartedAt time.Time `json:"started_at"`

	HTTPRequestsTotal   int64 `json:"http_requests_total"`
	HTTPErrorTotal      int64 `json:"http_error_total"`
	QueryTotal          int64 `json:"query_total"`
	ReadQueryTotal      int64 `json:"read_query_total"`
	WriteQueryTotal     int64 `json:"write_query_total"`
	TransactionTotal    int64 `json:"transaction_total"`
	WebSocketClients    int64 `json:"websocket_clients"`
	WebSocketEventsSent int64 `json:"websocket_events_sent"`
	BackupTotal         int64 `json:"backup_total"`
	ExportTotal         int64 `json:"export_total"`

	LastQueryDurationMs int64 `json:"last_query_duration_ms"`
}

func NewMetrics() *Metrics {
	return &Metrics{StartedAt: time.Now().UTC()}
}

func (m *Metrics) IncHTTP(status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HTTPRequestsTotal++
	if status >= 400 {
		m.HTTPErrorTotal++
	}
}

func (m *Metrics) IncQuery(kind SQLKind, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.QueryTotal++
	m.LastQueryDurationMs = duration.Milliseconds()
	if kind == SQLRead {
		m.ReadQueryTotal++
	} else {
		m.WriteQueryTotal++
	}
}

func (m *Metrics) IncTransaction() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TransactionTotal++
}

func (m *Metrics) AddWebSocketClient(delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WebSocketClients += delta
	if m.WebSocketClients < 0 {
		m.WebSocketClients = 0
	}
}

func (m *Metrics) IncWebSocketEvent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WebSocketEventsSent++
}

func (m *Metrics) IncBackup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.BackupTotal++
}

func (m *Metrics) IncExport() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ExportTotal++
}

// MetricsSnapshot is an immutable, JSON-safe view of Metrics.
type MetricsSnapshot struct {
	StartedAt time.Time `json:"started_at"`

	HTTPRequestsTotal   int64 `json:"http_requests_total"`
	HTTPErrorTotal      int64 `json:"http_error_total"`
	QueryTotal          int64 `json:"query_total"`
	ReadQueryTotal      int64 `json:"read_query_total"`
	WriteQueryTotal     int64 `json:"write_query_total"`
	TransactionTotal    int64 `json:"transaction_total"`
	WebSocketClients    int64 `json:"websocket_clients"`
	WebSocketEventsSent int64 `json:"websocket_events_sent"`
	BackupTotal         int64 `json:"backup_total"`
	ExportTotal         int64 `json:"export_total"`

	LastQueryDurationMs int64 `json:"last_query_duration_ms"`
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MetricsSnapshot{
		StartedAt:           m.StartedAt,
		HTTPRequestsTotal:   m.HTTPRequestsTotal,
		HTTPErrorTotal:      m.HTTPErrorTotal,
		QueryTotal:          m.QueryTotal,
		ReadQueryTotal:      m.ReadQueryTotal,
		WriteQueryTotal:     m.WriteQueryTotal,
		TransactionTotal:    m.TransactionTotal,
		WebSocketClients:    m.WebSocketClients,
		WebSocketEventsSent: m.WebSocketEventsSent,
		BackupTotal:         m.BackupTotal,
		ExportTotal:         m.ExportTotal,
		LastQueryDurationMs: m.LastQueryDurationMs,
	}
}

func (m *Metrics) JSON() ([]byte, error) {
	snapshot := m.Snapshot()
	return json.MarshalIndent(snapshot, "", "  ")
}
