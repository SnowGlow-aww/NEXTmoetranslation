package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moesekai/server/internal/lifecycle"
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

func TestWorkspaceRoutesAlwaysFailClosed(t *testing.T) {
	mux := http.NewServeMux()
	registerWorkspaceRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("admin")) })
	for _, requestPath := range []string{"/workspace", "/workspace/", "/workspace/editor/cards", "/workspace/assets/app.js"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "admin") {
			t.Fatalf("disabled workspace %s status=%d body=%q", requestPath, recorder.Code, recorder.Body.String())
		}
		assertWorkspaceErrorHeaders(t, recorder)
	}
	admin := httptest.NewRecorder()
	mux.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/", nil))
	if admin.Code != http.StatusOK || admin.Body.String() != "admin" {
		t.Fatalf("disabled workspace changed admin status=%d body=%q", admin.Code, admin.Body.String())
	}
}

func TestWorkspaceTombstoneClaimsEscapedNonCanonicalAndPreflightPaths(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("admin")) })
	handler := workspaceTombstoneMiddleware(preflightMiddleware(mux))
	requests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/workspace"},
		{http.MethodHead, "/workspace/"},
		{http.MethodPost, "/workspace/editor/cards"},
		{http.MethodOptions, "/workspace"},
		{http.MethodOptions, "/workspace/editor"},
		{http.MethodGet, "/workspace%2Feditor/cards"},
		{http.MethodGet, "/workspace%2F"},
		{http.MethodGet, "/workspace%5Ceditor"},
		{http.MethodGet, "/workspace%25252Feditor"},
		{http.MethodGet, "/workspace%252Feditor%25"},
		{http.MethodGet, "/%2577orkspace%252Feditor%25"},
		{http.MethodGet, "/x/%25252e%25252e/workspace"},
		{http.MethodGet, "/x/%25252e%25252e/workspace%25"},
		{http.MethodGet, "/workspace//editor"},
		{http.MethodGet, "/workspace/./editor"},
		{http.MethodGet, "/workspace/../"},
		{http.MethodGet, "/x/../workspace"},
		{http.MethodPost, "/x/../workspace/editor"},
		{http.MethodOptions, "/x/../workspace"},
		{http.MethodGet, "/./workspace"},
		{http.MethodGet, "/x/%2e%2e/workspace"},
	}
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "admin") {
			t.Fatalf("workspace tombstone %s %s status=%d body=%q", request.method, request.path, recorder.Code, recorder.Body.String())
		}
		assertWorkspaceErrorHeaders(t, recorder)
	}
	deeplyEncoded := "/workspace/editor"
	for range 20 {
		deeplyEncoded = url.PathEscape(deeplyEncoded)
	}
	if !retiredWorkspacePath(deeplyEncoded) {
		t.Fatalf("deeply encoded workspace path bypassed tombstone: %q", deeplyEncoded)
	}
}

func TestWorkspaceTombstonePrecedesLifecycleDrain(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("admin")) })
	state := &lifecycle.State{}
	state.Drain()
	handler := workspaceTombstoneMiddleware(lifecycleMiddleware(state, preflightMiddleware(mux)))

	workspace := httptest.NewRecorder()
	handler.ServeHTTP(workspace, httptest.NewRequest(http.MethodOptions, "/workspace%25252Feditor", nil))
	if workspace.Code != http.StatusNotFound || strings.Contains(workspace.Body.String(), "admin") {
		t.Fatalf("draining workspace status=%d body=%q", workspace.Code, workspace.Body.String())
	}
	assertWorkspaceErrorHeaders(t, workspace)

	ordinary := httptest.NewRecorder()
	handler.ServeHTTP(ordinary, httptest.NewRequest(http.MethodGet, "/", nil))
	if ordinary.Code != http.StatusServiceUnavailable || ordinary.Body.String() != `{"status":"draining"}` {
		t.Fatalf("draining ordinary status=%d body=%q", ordinary.Code, ordinary.Body.String())
	}
}

func assertWorkspaceSecurityHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		recorder.Header().Get("X-Frame-Options") != "DENY" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		recorder.Header().Get("Referrer-Policy") != "no-referrer" ||
		recorder.Header().Get("Permissions-Policy") != "camera=(), microphone=(), geolocation=()" {
		t.Fatalf("workspace security headers=%#v", recorder.Header())
	}
}

func assertWorkspaceErrorHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	assertWorkspaceSecurityHeaders(t, recorder)
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("workspace error cache=%q", recorder.Header().Get("Cache-Control"))
	}
}

func TestStaticHandlersDoNotFollowSymlinksOutsideRoot(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	adminRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(adminRoot, "index.html"), []byte("admin-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(adminRoot, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	admin := httptest.NewRecorder()
	staticHandler(adminRoot).ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/leak.txt", nil))
	if strings.Contains(admin.Body.String(), "outside-secret") {
		t.Fatalf("admin static handler exposed symlink target: %q", admin.Body.String())
	}

	symlinkIndexRoot := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(symlinkIndexRoot, "index.html")); err != nil {
		t.Fatal(err)
	}
	index := httptest.NewRecorder()
	staticHandler(symlinkIndexRoot).ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusNotFound || strings.Contains(index.Body.String(), "outside-secret") {
		t.Fatalf("admin index symlink status=%d body=%q", index.Code, index.Body.String())
	}
}
