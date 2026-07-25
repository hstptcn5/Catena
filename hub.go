package main

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

// TableEvent represents the structure sent to WebSocket clients when a table changes
type TableEvent struct {
	Type         string `json:"type"` // e.g. "update"
	Table        string `json:"table"`
	Operation    string `json:"operation,omitempty"`
	RowsAffected int64  `json:"rows_affected,omitempty"`
	Timestamp    string `json:"timestamp"`
}

// WSMessage represents the structure received from clients
type WSMessage struct {
	Type  string `json:"type"`  // "subscribe" or "unsubscribe"
	Table string `json:"table"` // Name of the table
}

// Client represents a WebSocket connection
type Client struct {
	hub *Hub

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan []byte

	// Mutex to protect subscriptions map
	subMu sync.RWMutex
	// Set of tables the client is subscribed to
	subscriptions map[string]bool
}

// Hub maintains the set of active clients and broadcasts events
type Hub struct {
	// Registered clients.
	clients map[*Client]bool

	// Inbound messages from the clients.
	broadcast chan TableEvent

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	// Read/Write lock for clients map
	mu sync.RWMutex
}

// NewHub creates and returns a new Hub
func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan TableEvent),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

// Run starts the event loop for managing clients and broadcasting events
func (h *Hub) Run() {
	slog.Info("WebSocket Hub is running")
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			slog.Debug("WebSocket client registered", "addr", client.conn.RemoteAddr().String())

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				slog.Debug("WebSocket client unregistered", "addr", client.conn.RemoteAddr().String())
			}
			h.mu.Unlock()

		case event := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				client.subMu.RLock()
				subscribed := client.subscriptions[event.Table] || client.subscriptions["*"]
				client.subMu.RUnlock()

				if subscribed {
					payload, err := json.Marshal(event)
					if err != nil {
						slog.Error("Failed to marshal table event", "err", err)
						continue
					}
					select {
					case client.send <- payload:
					default:
						// If the client's channel is blocked, unregister the client
						go func(c *Client) {
							h.unregister <- c
						}(client)
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast notifies the Hub that a table has changed.
func (h *Hub) Broadcast(event WriteEvent) {
	h.BroadcastEvent(TableEvent{
		Type:         "update",
		Table:        event.Table,
		Operation:    event.Operation,
		RowsAffected: event.RowsAffected,
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// BroadcastEvent sends a fully formed event to subscribed clients.
func (h *Hub) BroadcastEvent(event TableEvent) {
	slog.Debug("Broadcasting table update", "table", event.Table, "operation", event.Operation)
	h.broadcast <- event
}

// ReadPump pumps messages from the websocket connection to the hub
func (c *Client) ReadPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("WebSocket read error", "err", err)
			}
			break
		}

		var req WSMessage
		if err := json.Unmarshal(message, &req); err != nil {
			slog.Warn("Failed to unmarshal websocket message", "err", err, "msg", string(message))
			continue
		}

		c.subMu.Lock()
		switch req.Type {
		case "subscribe":
			if req.Table == "" {
				c.sendJSON(map[string]string{"type": "error", "message": "table is required"})
				c.subMu.Unlock()
				continue
			}
			c.subscriptions[req.Table] = true
			c.sendJSON(map[string]string{"type": "subscribed", "table": req.Table})
			slog.Debug("Client subscribed to table", "table", req.Table, "addr", c.conn.RemoteAddr().String())
		case "unsubscribe":
			delete(c.subscriptions, req.Table)
			c.sendJSON(map[string]string{"type": "unsubscribed", "table": req.Table})
			slog.Debug("Client unsubscribed from table", "table", req.Table, "addr", c.conn.RemoteAddr().String())
		default:
			c.sendJSON(map[string]string{"type": "error", "message": "unsupported websocket message type"})
		}
		c.subMu.Unlock()
	}
}

func (c *Client) sendJSON(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		slog.Error("Failed to marshal websocket response", "err", err)
		return
	}
	select {
	case c.send <- payload:
	default:
	}
}

// WritePump pumps messages from the hub to the websocket connection
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued chat messages to the current websocket message
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
