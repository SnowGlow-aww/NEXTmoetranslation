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

// workspaceTombstoneMiddleware claims the decoded retired workspace prefix
// before ServeMux canonicalization and generic OPTIONS handling. This prevents
// escaped separators and noncanonical paths from bypassing the explicit 404 and
// falling through to the admin SPA.
func workspaceTombstoneMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retiredWorkspacePath(r.URL.Path) || (r.URL.RawPath != "" && retiredWorkspacePath(r.URL.RawPath)) {
			workspaceNotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func retiredWorkspacePath(candidate string) bool {
	const maxDecodePasses = 16
	for range maxDecodePasses {
		decoded, changed := decodePathPercentPass(candidate)
		if !changed {
			break
		}
		candidate = decoded
	}
	if _, changed := decodePathPercentPass(candidate); changed {
		// Excessive nested encoding is noncanonical and deliberately fails closed
		// instead of consuming unbounded CPU or reaching the admin SPA.
		return true
	}
	candidate = strings.ReplaceAll(candidate, `\`, "/")
	if hasRetiredWorkspacePrefix(candidate) {
		return true
	}
	return hasRetiredWorkspacePrefix(path.Clean(candidate))
}

func hasRetiredWorkspacePrefix(candidate string) bool {
	return candidate == "/workspace" ||
		strings.HasPrefix(candidate, "/workspace/") ||
		strings.HasPrefix(candidate, "/workspace%")
}

// decodePathPercentPass decodes every well-formed %HH byte while preserving
// malformed percent bytes. Unlike url.PathUnescape, one malformed inner escape
// cannot prevent valid neighboring escapes from revealing a retired workspace
// path on a later bounded pass.
func decodePathPercentPass(candidate string) (string, bool) {
	var decoded strings.Builder
	decoded.Grow(len(candidate))
	changed := false
	for index := 0; index < len(candidate); {
		if candidate[index] == '%' && index+2 < len(candidate) {
			high, highOK := hexNibble(candidate[index+1])
			low, lowOK := hexNibble(candidate[index+2])
			if highOK && lowOK {
				decoded.WriteByte(high<<4 | low)
				index += 3
				changed = true
				continue
			}
		}
		decoded.WriteByte(candidate[index])
		index++
	}
	return decoded.String(), changed
}

func hexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

// registerWorkspaceRoutes claims /workspace and /workspace/ so the retired
// prefix cannot fall through to the admin SPA. External workspace artifacts are
// verifier-only and are never mounted by a running server.
func registerWorkspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/workspace", workspaceNotFound)
	mux.HandleFunc("/workspace/", workspaceNotFound)
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
