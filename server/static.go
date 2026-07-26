package main

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// staticHandler serves the statically-exported Next.js console as a single-page
// app, so the Go server is the only process: no nginx, no Node.js.
//
// Resolution order for a request path:
//  1. the exact file (e.g. /_next/static/chunks/abc.js)
//  2. its ".html" sibling (App Router exports /admin -> admin.html)
//  3. a directory index (/foo/ -> /foo/index.html)
//  4. index.html, so client-side routes and page refreshes resolve
//
// Content-hashed assets under /_next/static are immutable and cached for a year;
// HTML is served no-cache so console updates are picked up immediately.
func staticHandler(root string) http.HandlerFunc {
	root = filepath.Clean(root)
	return func(w http.ResponseWriter, r *http.Request) {
		setStaticSecurityHeaders(w.Header())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		upath := r.URL.Path
		if !strings.HasPrefix(upath, "/") {
			upath = "/" + upath
		}
		clean := path.Clean(upath)
		rootDir, err := os.OpenRoot(root)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer rootDir.Close()

		// serve writes the file at rel (relative to root) if it exists and is a
		// regular file, applying cache headers. Returns true if it served.
		serve := func(rel string, immutable bool) bool {
			name := strings.TrimPrefix(path.Clean("/"+rel), "/")
			file, err := rootDir.Open(name)
			if err != nil {
				return false
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				return false
			}
			if immutable {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if strings.HasSuffix(name, ".html") {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeContent(w, r, path.Base(name), info.ModTime(), file)
			return true
		}

		switch {
		case clean != "/" && serve(clean, strings.HasPrefix(clean, "/_next/static/")): // 1) exact file
		case clean != "/" && serve(clean+".html", strings.HasPrefix(clean, "/_next/static/")): // 2) .html sibling
		case strings.HasSuffix(upath, "/") && serve(clean+"/index.html", strings.HasPrefix(clean, "/_next/static/")): // 3) dir index
		default: // 4) SPA fallback
			w.Header().Set("Cache-Control", "no-cache")
			if !serve("index.html", false) {
				http.NotFound(w, r)
			}
		}
	}
}

// registerWorkspaceRoutes always claims /workspace when the optional artifact
// is absent, so it cannot fall through to the existing admin console SPA.
func registerWorkspaceRoutes(mux *http.ServeMux, root string) bool {
	root = strings.TrimSpace(root)
	enabled := workspaceRootAvailable(root)
	notFound := func(w http.ResponseWriter, r *http.Request) { workspaceNotFound(w, r) }
	if !enabled {
		mux.HandleFunc("/workspace", notFound)
		mux.HandleFunc("/workspace/", notFound)
		return false
	}
	handler := workspaceStaticHandler(root)
	mux.HandleFunc("/workspace", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			handler(w, r)
			return
		}
		setStaticSecurityHeaders(w.Header())
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/workspace/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("/workspace/", handler)
	return true
}

func workspaceRootAvailable(root string) bool {
	if root == "" {
		return false
	}
	rootDir, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer rootDir.Close()
	index, err := rootDir.Open("index.html")
	if err != nil {
		return false
	}
	defer index.Close()
	indexInfo, err := index.Stat()
	return err == nil && indexInfo.Mode().IsRegular()
}

func workspaceStaticHandler(root string) http.HandlerFunc {
	root = filepath.Clean(root)
	return func(w http.ResponseWriter, r *http.Request) {
		setStaticSecurityHeaders(w.Header())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		requestPath := strings.TrimPrefix(r.URL.Path, "/workspace/")
		clean := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
		rootDir, err := os.OpenRoot(root)
		if err != nil {
			workspaceNotFound(w, r)
			return
		}
		defer rootDir.Close()
		serve := func(rel string, immutable bool) bool {
			name := strings.TrimPrefix(path.Clean("/"+rel), "/")
			file, err := rootDir.Open(name)
			if err != nil {
				return false
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				return false
			}
			if immutable {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if strings.HasSuffix(name, ".html") {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeContent(w, r, path.Base(name), info.ModTime(), file)
			return true
		}
		if clean != "" && serve(clean, strings.HasPrefix(clean, "assets/")) {
			return
		}
		// Asset misses must remain 404 instead of returning HTML to a script load.
		if strings.HasPrefix(clean, "assets/") || path.Ext(clean) != "" {
			workspaceNotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if !serve("index.html", false) {
			workspaceNotFound(w, r)
		}
	}
}

func workspaceNotFound(w http.ResponseWriter, r *http.Request) {
	setStaticSecurityHeaders(w.Header())
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, r)
}

func setStaticSecurityHeaders(headers http.Header) {
	headers.Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; connect-src 'self'; img-src 'self' data: https:; object-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; form-action 'self'")
	headers.Set("X-Frame-Options", "DENY")
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("Referrer-Policy", "no-referrer")
	headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}
