package main

import (
	"context"
	"errors"
	"net"
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
	authService := auth.New(database, "operations-secret-at-least-32-bytes", time.Hour)
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

func TestSeedAdminUsesFirstSuccessfullyCreatedAccount(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "seed-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authService := auth.New(database, "operations-secret-at-least-32-bytes", time.Hour)
	t.Setenv("TRANSLATOR_ACCOUNTS", ":bad,editor:strong-password-123")
	t.Setenv("ADMIN_PASSWORD", "")
	if err := seedAdminFromEnv(authService); err != nil {
		t.Fatal(err)
	}
	user, err := authService.GetUser("editor")
	if err != nil || user.Role != auth.RoleAdmin {
		t.Fatalf("first valid account = %+v err=%v", user, err)
	}
}

func TestHTTPServerShutdownDrainsInflightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := newHTTPServer("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	requestResult := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			response.Body.Close()
		}
		requestResult <- err
	}()
	<-started
	shutdownResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownResult <- server.Shutdown(ctx)
	}()
	select {
	case err := <-shutdownResult:
		t.Fatalf("shutdown returned before request drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-requestResult; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatal(err)
	}
	if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve result = %v", err)
	}
}
