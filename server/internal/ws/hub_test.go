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
