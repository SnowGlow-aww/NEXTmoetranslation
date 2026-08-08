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

func TestWorkspaceRoutesServeIsolatedSPAAndImmutableAssets(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("workspace-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app-a1b2c3.js"), []byte("workspace-asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if !registerWorkspaceRoutes(mux, root) {
		t.Fatal("valid workspace root was disabled")
	}
	for _, preservedPath := range []string{"/api/preserved", "/files/preserved", "/translation/preserved", "/healthz"} {
		preservedPath := preservedPath
		mux.HandleFunc(preservedPath, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(preservedPath)) })
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("admin")) })

	exact := httptest.NewRecorder()
	mux.ServeHTTP(exact, httptest.NewRequest(http.MethodGet, "/workspace", nil))
	if exact.Code != http.StatusPermanentRedirect || exact.Header().Get("Location") != "/workspace/" {
		t.Fatalf("workspace redirect status=%d location=%q", exact.Code, exact.Header().Get("Location"))
	}
	assertWorkspaceSecurityHeaders(t, exact)
	if exact.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("workspace redirect cache=%q", exact.Header().Get("Cache-Control"))
	}
	for _, requestPath := range []string{"/workspace/", "/workspace/editor/cards"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != "workspace-index" || recorder.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("workspace SPA %s status=%d cache=%q body=%q", requestPath, recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
		}
	}
	asset := httptest.NewRecorder()
	mux.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/workspace/assets/app-a1b2c3.js", nil))
	if asset.Code != http.StatusOK || asset.Body.String() != "workspace-asset" || asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("workspace asset status=%d cache=%q body=%q", asset.Code, asset.Header().Get("Cache-Control"), asset.Body.String())
	}
	missingAsset := httptest.NewRecorder()
	mux.ServeHTTP(missingAsset, httptest.NewRequest(http.MethodGet, "/workspace/assets/missing.js", nil))
	if missingAsset.Code != http.StatusNotFound || strings.Contains(missingAsset.Body.String(), "workspace-index") {
		t.Fatalf("missing asset status=%d body=%q", missingAsset.Code, missingAsset.Body.String())
	}
	assertWorkspaceErrorHeaders(t, missingAsset)
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/workspace/", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("workspace POST status=%d", post.Code)
	}
	assertWorkspaceErrorHeaders(t, post)
	for _, preservedPath := range []string{"/api/preserved", "/files/preserved", "/translation/preserved", "/healthz"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, preservedPath, nil))
		if recorder.Body.String() != preservedPath {
			t.Fatalf("workspace mount changed %s route: %q", preservedPath, recorder.Body.String())
		}
	}
	adminRecorder := httptest.NewRecorder()
	mux.ServeHTTP(adminRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if adminRecorder.Body.String() != "admin" {
		t.Fatalf("workspace mount changed admin route: %q", adminRecorder.Body.String())
	}
}

func TestWorkspaceRoutesFailClosedWhenArtifactIsAbsentOrInvalid(t *testing.T) {
	for _, root := range []string{"", t.TempDir()} {
		mux := http.NewServeMux()
		if registerWorkspaceRoutes(mux, root) {
			t.Fatalf("invalid workspace root %q was enabled", root)
		}
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
}

func assertWorkspaceSecurityHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		recorder.Header().Get("X-Frame-Options") != "DENY" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
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

	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "index.html"), []byte("workspace-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspaceRoot, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(workspaceRoot, "assets", "leak.js")); err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewRecorder()
	workspaceStaticHandler(workspaceRoot).ServeHTTP(workspace, httptest.NewRequest(http.MethodGet, "/workspace/assets/leak.js", nil))
	if workspace.Code != http.StatusNotFound || strings.Contains(workspace.Body.String(), "outside-secret") {
		t.Fatalf("workspace symlink status=%d body=%q", workspace.Code, workspace.Body.String())
	}

	symlinkIndexRoot := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(symlinkIndexRoot, "index.html")); err != nil {
		t.Fatal(err)
	}
	if workspaceRootAvailable(symlinkIndexRoot) {
		t.Fatal("workspace accepted an index symlink escaping its root")
	}
	index := httptest.NewRecorder()
	staticHandler(symlinkIndexRoot).ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusNotFound || strings.Contains(index.Body.String(), "outside-secret") {
		t.Fatalf("admin index symlink status=%d body=%q", index.Code, index.Body.String())
	}
}
