package main

import (
	"testing"
	"time"
)

func TestHubStopBeforeRunIsSafe(t *testing.T) {
	hub := NewHub()

	done := make(chan struct{})
	go func() {
		hub.Stop()
		hub.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop before Run blocked")
	}
}

func TestHubStopAfterRunIsIdempotent(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	<-hub.started

	done := make(chan struct{})
	go func() {
		hub.Stop()
		hub.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop after Run blocked")
	}
}

func TestHubOperationsAfterStopDoNotBlock(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	<-hub.started
	hub.Stop()

	done := make(chan struct{})
	go func() {
		if hub.Register(&Client{send: make(chan []byte, 1), subscriptions: map[string]bool{"*": true}}) {
			t.Error("Register succeeded after Stop")
		}
		hub.Unregister(&Client{send: make(chan []byte, 1)})
		hub.BroadcastEvent(TableEvent{Type: "update", Table: "items", Operation: "insert"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hub operations blocked after Stop")
	}
}
