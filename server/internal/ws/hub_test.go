package ws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/editorgate"

	"golang.org/x/net/websocket"
)

func TestWebSocketHubBroadcastAndGateStatus(t *testing.T) {
	gate := editorgate.MustNew()
	hub := NewHub(gate)
	defer hub.Close()

	handler := hub.Handler(func(_ *http.Request) string { return "testuser" }, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	// Expect initial gate.status event immediately upon connection.
	var initMsg Message
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.JSON.Receive(ws, &initMsg); err != nil {
		t.Fatalf("failed to receive initial gate.status: %v", err)
	}
	if initMsg.Event != EventGateStatus {
		t.Fatalf("got event %q, want %q", initMsg.Event, EventGateStatus)
	}

	// Test broadcast
	hub.Broadcast(EventEntryUpdated, map[string]string{"key": "test_key"})

	var bcastMsg Message
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.JSON.Receive(ws, &bcastMsg); err != nil {
		t.Fatalf("failed to receive broadcast event: %v", err)
	}
	if bcastMsg.Event != EventEntryUpdated {
		t.Fatalf("got event %q, want %q", bcastMsg.Event, EventEntryUpdated)
	}
}

func TestWebSocketHubPongKeepsConnectionAliveWithoutGateStatus(t *testing.T) {
	gate := editorgate.MustNew()
	hub := NewHub(gate)
	hub.heartbeatInterval = 10 * time.Millisecond
	hub.clientReadTimeout = 150 * time.Millisecond
	defer hub.Close()

	handler := hub.Handler(func(_ *http.Request) string { return "heartbeat-user" }, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	var initial Message
	_ = ws.SetReadDeadline(time.Now().Add(time.Second))
	if err := websocket.JSON.Receive(ws, &initial); err != nil {
		t.Fatalf("failed to receive initial gate status: %v", err)
	}
	if initial.Event != EventGateStatus {
		t.Fatalf("initial event = %q, want %q", initial.Event, EventGateStatus)
	}

	// Reply to enough heartbeats to cross the configured read timeout. A pong
	// must extend connection liveness without producing another gate status.
	pingCount := 0
	deadline := time.Now().Add(2 * time.Second)
	for pingCount < 20 {
		var msg Message
		_ = ws.SetReadDeadline(deadline)
		if err := websocket.JSON.Receive(ws, &msg); err != nil {
			t.Fatalf("connection closed while acknowledging heartbeat %d: %v", pingCount, err)
		}
		switch msg.Event {
		case EventGateStatus:
			t.Fatal("pong unexpectedly triggered gate.status")
		case EventPing:
			if err := websocket.JSON.Send(ws, ClientMsg{Type: "pong"}); err != nil {
				t.Fatalf("failed to send pong %d: %v", pingCount, err)
			}
			pingCount++
		}
	}

	hub.Broadcast(EventEntryUpdated, map[string]string{"key": "after-heartbeats"})
	deadline = time.Now().Add(2 * time.Second)
	for {
		var msg Message
		_ = ws.SetReadDeadline(deadline)
		if err := websocket.JSON.Receive(ws, &msg); err != nil {
			t.Fatalf("silent connection did not receive broadcast after heartbeats: %v", err)
		}
		if msg.Event == EventEntryUpdated {
			break
		}
		if msg.Event == EventGateStatus {
			t.Fatal("pong unexpectedly triggered gate.status before broadcast")
		}
		if msg.Event == EventPing {
			if err := websocket.JSON.Send(ws, ClientMsg{Type: "pong"}); err != nil {
				t.Fatalf("failed to send pong while awaiting broadcast: %v", err)
			}
		}
	}
}

func TestWebSocketHubDisconnectsClientThatDoesNotPong(t *testing.T) {
	hub := NewHub(nil)
	hub.heartbeatInterval = 10 * time.Millisecond
	hub.clientReadTimeout = 60 * time.Millisecond
	defer hub.Close()

	handler := hub.Handler(func(_ *http.Request) string { return "silent-user" }, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	deadline := time.Now().Add(time.Second)
	pingCount := 0
	for {
		var msg Message
		_ = ws.SetReadDeadline(deadline)
		err := websocket.JSON.Receive(ws, &msg)
		if err != nil {
			break
		}
		if msg.Event == EventPing {
			pingCount++
		}
	}
	if pingCount == 0 {
		t.Fatal("silent client was disconnected before receiving a heartbeat")
	}
}
