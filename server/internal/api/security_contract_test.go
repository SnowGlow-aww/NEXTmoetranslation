package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"moesekai/server/internal/auth"
)

func TestEditorsCannotTriggerAdministrativeOperations(t *testing.T) {
	h := setupLegacyAPI(t)
	editor, err := h.api.auth.CreateUser("editor", "password", auth.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.api.auth.IssueToken(editor)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/event-story/promote-human",
		"/api/event-story/retry",
		"/api/event-story/reorder",
		"/api/translate/cn-sync",
		"/api/translate/ai",
		"/api/translate/ai-all",
		"/api/translate/ai-story",
		"/api/backup/push",
	} {
		response := doJSON(t, http.MethodPost, h.server.URL+path, token, map[string]any{})
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("editor %s status = %d", path, response.StatusCode)
		}
	}
}

func TestStaleAdminTokensFailAuthentication(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*legacyAPIHarness) error
	}{
		{name: "role", revoke: func(h *legacyAPIHarness) error { return h.api.auth.SetRole("alice", auth.RoleEditor) }},
		{name: "password", revoke: func(h *legacyAPIHarness) error { return h.api.auth.SetPassword("alice", "new-password") }},
		{name: "delete", revoke: func(h *legacyAPIHarness) error { return h.api.auth.DeleteUser("alice") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := setupLegacyAPI(t)
			if _, err := h.api.auth.CreateUser("backup-admin", "password", auth.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			if err := test.revoke(h); err != nil {
				t.Fatal(err)
			}
			response := doJSON(t, http.MethodGet, h.server.URL+"/api/admin/users", h.token, nil)
			response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("stale %s token status = %d", test.name, response.StatusCode)
			}
		})
	}
}

func TestPublicAuthAttemptLimitsUseRemoteAddrAndAccount(t *testing.T) {
	t.Run("account across addresses", func(t *testing.T) {
		h := setupLegacyAPI(t)
		h.api.authAttempts = newAuthAttemptLimiter(2, time.Minute, 128)
		for attempt := 0; attempt < 3; attempt++ {
			response := invokePublicAuth(t, h.api.handleLogin, "203.0.113."+strconv.Itoa(attempt+1)+":1234", "198.51.100."+strconv.Itoa(attempt+1), map[string]string{
				"username": "alice", "password": "wrong",
			})
			want := http.StatusUnauthorized
			if attempt == 2 {
				want = http.StatusTooManyRequests
			}
			if response.Code != want {
				t.Fatalf("account attempt %d status = %d", attempt, response.Code)
			}
		}
	})

	t.Run("address ignores forwarding headers", func(t *testing.T) {
		h := setupLegacyAPI(t)
		h.api.authAttempts = newAuthAttemptLimiter(2, time.Minute, 128)
		for attempt, username := range []string{"missing-1", "missing-2", "missing-3"} {
			response := invokePublicAuth(t, h.api.handleLogin, "203.0.113.10:4321", "198.51.100."+strconv.Itoa(attempt+1), map[string]string{
				"username": username, "password": "wrong",
			})
			want := http.StatusUnauthorized
			if attempt == 2 {
				want = http.StatusTooManyRequests
			}
			if response.Code != want {
				t.Fatalf("IP attempt %d status = %d", attempt, response.Code)
			}
		}
	})

	t.Run("setup", func(t *testing.T) {
		h := setupLegacyAPI(t)
		h.api.authAttempts = newAuthAttemptLimiter(2, time.Minute, 128)
		for attempt := 0; attempt < 3; attempt++ {
			response := invokePublicAuth(t, h.api.handleSetup, "203.0.113.20:4321", "198.51.100.20", map[string]string{
				"username": "candidate", "password": "password",
			})
			want := http.StatusConflict
			if attempt == 2 {
				want = http.StatusTooManyRequests
			}
			if response.Code != want {
				t.Fatalf("setup attempt %d status = %d", attempt, response.Code)
			}
		}
	})
}

func TestPublicAuthAttemptLimiterBoundsTrackedKeys(t *testing.T) {
	limiter := newAuthAttemptLimiter(100, time.Hour, 4)
	for attempt := 0; attempt < 20; attempt++ {
		if !limiter.allow("login", "203.0.113."+strconv.Itoa(attempt)+":1234", "user-"+strconv.Itoa(attempt)) {
			t.Fatalf("unique attempt %d unexpectedly blocked", attempt)
		}
	}
	if len(limiter.attempts) > limiter.maxEntries {
		t.Fatalf("tracked keys = %d, max = %d", len(limiter.attempts), limiter.maxEntries)
	}
}

func invokePublicAuth(t *testing.T, handler http.HandlerFunc, remoteAddr, forwardedFor string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	request.RemoteAddr = remoteAddr
	request.Header.Set("X-Forwarded-For", forwardedFor)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}
