package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"moesekai/server/internal/auth"
	"moesekai/server/internal/db"
)

func TestOperationalDetailsRequireAdminAndServerBoundsBodyRead(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authService := auth.New(database, "operations-secret", time.Hour)
	mux := http.NewServeMux()
	registerOperationalRoutes(mux, database, authService)

	public := httptest.NewRecorder()
	mux.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/healthz/details", nil))
	if public.Code != http.StatusUnauthorized {
		t.Fatalf("public health details status = %d", public.Code)
	}
	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || health.Body.String() != `{"status":"ok"}` {
		t.Fatalf("legacy health response status=%d body=%q", health.Code, health.Body.String())
	}

	server := newHTTPServer(":0", mux)
	if server.ReadTimeout != 30*time.Second || server.ReadHeaderTimeout != 10*time.Second || server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("HTTP bounds read=%s header=%s maxHeader=%d", server.ReadTimeout, server.ReadHeaderTimeout, server.MaxHeaderBytes)
	}
}
