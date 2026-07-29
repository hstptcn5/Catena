package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const hubTestTimeout = 3 * time.Second

type hubTestServer struct {
	hub    *Hub
	server *httptest.Server
	wsURL  string
}

func newHubTestServer(t *testing.T) *hubTestServer {
	t.Helper()

	hub := NewHub()
	go hub.Run()
	select {
	case <-hub.started:
	case <-time.After(hubTestTimeout):
		t.Fatal("hub did not start")
	}

	server := httptest.NewServer(NewServer(nil, hub, ServerConfig{}))
	testServer := &hubTestServer{
		hub:    hub,
		server: server,
		wsURL:  "ws" + strings.TrimPrefix(server.URL, "http") + "/ws",
	}
	t.Cleanup(func() {
		hub.Stop()
		server.Close()
	})
	return testServer
}

func (s *hubTestServer) dial(t *testing.T) *websocket.Conn {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(s.wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	return conn
}

func waitForHubClientCount(t *testing.T, hub *Hub, want int64) {
	t.Helper()

	deadline := time.Now().Add(hubTestTimeout)
	for time.Now().Before(deadline) {
		if got := hub.metrics.Snapshot().WebSocketClients; got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("websocket client metric did not reach %d; got %d", want, hub.metrics.Snapshot().WebSocketClients)
}

func waitForGoroutine(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(hubTestTimeout):
		t.Fatalf("%s did not exit", description)
	}
}

func subscribeAndReadAck(t *testing.T, conn *websocket.Conn, table string) {
	t.Helper()

	if err := conn.WriteJSON(WSMessage{Type: "subscribe", Table: table}); err != nil {
		t.Fatalf("websocket subscribe failed: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(hubTestTimeout)); err != nil {
		t.Fatalf("set read deadline failed: %v", err)
	}
	var ack map[string]string
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("websocket subscribe acknowledgment failed: %v", err)
	}
	if ack["type"] != "subscribed" || ack["table"] != table {
		t.Fatalf("unexpected subscribe acknowledgment: %+v", ack)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline failed: %v", err)
	}
}

func TestHubStopBeforeRunIsSafe(t *testing.T) {
	hub := NewHub()

	done := make(chan struct{})
	go func() {
		hub.Stop()
		hub.Stop()
		close(done)
	}()

	waitForGoroutine(t, done, "Stop before Run")
}

func TestHubStopWithConnectedWebSocket(t *testing.T) {
	testServer := newHubTestServer(t)
	conn := testServer.dial(t)
	defer conn.Close()

	subscribeAndReadAck(t, conn, "*")
	waitForHubClientCount(t, testServer.hub, 1)

	stopped := make(chan struct{})
	go func() {
		testServer.hub.Stop()
		close(stopped)
	}()
	waitForGoroutine(t, stopped, "Hub.Stop")

	if got := testServer.hub.metrics.Snapshot().WebSocketClients; got != 0 {
		t.Fatalf("expected zero websocket clients after Stop, got %d", got)
	}
	if err := conn.SetReadDeadline(time.Now().Add(hubTestTimeout)); err != nil {
		t.Fatalf("set read deadline failed: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected server-side websocket connection to close")
	}
}

func TestHubStopWhileClientSubscribesAndUnsubscribes(t *testing.T) {
	testServer := newHubTestServer(t)
	conn := testServer.dial(t)
	defer conn.Close()

	subscribeAndReadAck(t, conn, "initial")
	waitForHubClientCount(t, testServer.hub, 1)

	var sent atomic.Int64
	var received atomic.Int64
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; ; i++ {
			kind := "subscribe"
			if i%2 == 1 {
				kind = "unsubscribe"
			}
			if err := conn.SetWriteDeadline(time.Now().Add(hubTestTimeout)); err != nil {
				return
			}
			if err := conn.WriteJSON(WSMessage{Type: kind, Table: fmt.Sprintf("table_%d", i%4)}); err != nil {
				return
			}
			sent.Add(1)
		}
	}()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			received.Add(int64(len(strings.Split(string(payload), "\n"))))
		}
	}()

	deadline := time.Now().Add(hubTestTimeout)
	for (sent.Load() < 100 || received.Load() < 20) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sent.Load() < 100 || received.Load() < 20 {
		t.Fatalf("insufficient websocket activity before shutdown: sent=%d received=%d", sent.Load(), received.Load())
	}

	testServer.hub.Stop()
	waitForGoroutine(t, writerDone, "websocket writer")
	waitForGoroutine(t, readerDone, "websocket reader")
	if got := testServer.hub.metrics.Snapshot().WebSocketClients; got != 0 {
		t.Fatalf("expected zero websocket clients after Stop, got %d", got)
	}
}

func TestHubConcurrentBroadcastAndStop(t *testing.T) {
	testServer := newHubTestServer(t)
	conn := testServer.dial(t)
	defer conn.Close()

	subscribeAndReadAck(t, conn, "*")
	waitForHubClientCount(t, testServer.hub, 1)

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	start := make(chan struct{})
	ready := make(chan struct{}, 8)
	stopBroadcast := make(chan struct{})
	var broadcasters sync.WaitGroup
	for i := 0; i < 8; i++ {
		broadcasters.Add(1)
		go func(worker int) {
			defer broadcasters.Done()
			<-start
			event := 0
			for {
				select {
				case <-stopBroadcast:
					return
				default:
				}
				testServer.hub.BroadcastEvent(TableEvent{
					Type:      "update",
					Table:     fmt.Sprintf("table_%d", worker),
					Operation: "insert",
				})
				if event == 0 {
					ready <- struct{}{}
				}
				event++
			}
		}(i)
	}

	close(start)
	for i := 0; i < 8; i++ {
		select {
		case <-ready:
		case <-time.After(hubTestTimeout):
			t.Fatal("broadcaster did not become active")
		}
	}
	testServer.hub.Stop()
	close(stopBroadcast)
	broadcastDone := make(chan struct{})
	go func() {
		broadcasters.Wait()
		close(broadcastDone)
	}()
	waitForGoroutine(t, broadcastDone, "broadcasters")
	waitForGoroutine(t, readerDone, "websocket reader")
	if got := testServer.hub.metrics.Snapshot().WebSocketClients; got != 0 {
		t.Fatalf("expected zero websocket clients after Stop, got %d", got)
	}
}

func TestHubConcurrentRegisterUnregisterAndStop(t *testing.T) {
	testServer := newHubTestServer(t)

	const clientCount = 12
	connections := make([]*websocket.Conn, 0, clientCount)
	for i := 0; i < clientCount; i++ {
		connections = append(connections, testServer.dial(t))
	}
	waitForHubClientCount(t, testServer.hub, clientCount)

	start := make(chan struct{})
	var operations sync.WaitGroup
	for _, conn := range connections {
		operations.Add(1)
		go func(conn *websocket.Conn) {
			defer operations.Done()
			<-start
			conn.Close()
		}(conn)
	}
	for i := 0; i < clientCount; i++ {
		operations.Add(1)
		go func() {
			defer operations.Done()
			<-start
			conn, _, err := websocket.DefaultDialer.Dial(testServer.wsURL, nil)
			if err == nil {
				conn.Close()
			}
		}()
	}

	stopDone := make(chan struct{})
	go func() {
		<-start
		testServer.hub.Stop()
		close(stopDone)
	}()
	close(start)

	operationsDone := make(chan struct{})
	go func() {
		operations.Wait()
		close(operationsDone)
	}()
	waitForGoroutine(t, operationsDone, "concurrent register and unregister operations")
	waitForGoroutine(t, stopDone, "Hub.Stop")
	if got := testServer.hub.metrics.Snapshot().WebSocketClients; got != 0 {
		t.Fatalf("expected zero websocket clients after concurrent shutdown, got %d", got)
	}
}

func TestHubMultipleConcurrentStops(t *testing.T) {
	testServer := newHubTestServer(t)
	conn := testServer.dial(t)
	defer conn.Close()
	waitForHubClientCount(t, testServer.hub, 1)

	const callers = 32
	start := make(chan struct{})
	var stops sync.WaitGroup
	stops.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer stops.Done()
			<-start
			testServer.hub.Stop()
		}()
	}
	close(start)

	done := make(chan struct{})
	go func() {
		stops.Wait()
		close(done)
	}()
	waitForGoroutine(t, done, "concurrent Stop calls")
	if got := testServer.hub.metrics.Snapshot().WebSocketClients; got != 0 {
		t.Fatalf("expected zero websocket clients after concurrent Stop calls, got %d", got)
	}
}

func TestHubOperationsAfterStopDoNotBlock(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	<-hub.started
	hub.Stop()

	done := make(chan struct{})
	go func() {
		if hub.Register(&Client{send: make(chan []byte, 1), done: make(chan struct{}), subscriptions: map[string]bool{"*": true}}) {
			t.Error("Register succeeded after Stop")
		}
		hub.Unregister(&Client{send: make(chan []byte, 1), done: make(chan struct{})})
		hub.BroadcastEvent(TableEvent{Type: "update", Table: "items", Operation: "insert"})
		close(done)
	}()

	waitForGoroutine(t, done, "hub operations after Stop")
}
