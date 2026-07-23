package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticConsoleSecurityHeaders(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	staticHandler(root).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		recorder.Header().Get("X-Frame-Options") != "DENY" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		recorder.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers = %#v", recorder.Header())
	}
}
