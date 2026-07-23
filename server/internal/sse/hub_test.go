package sse

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStreamClosesAtTokenExpiry(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(hub.Handler(func(*http.Request) string { return "editor" },
		func(*http.Request) bool { return true }, func(*http.Request) time.Time { return time.Now().Add(40 * time.Millisecond) }))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(response.Body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE stream survived token expiry")
	}
}
