package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
		limiter.allow("login", "203.0.113."+strconv.Itoa(attempt)+":1234", "user-"+strconv.Itoa(attempt))
	}
	if len(limiter.attempts) > limiter.maxEntries {
		t.Fatalf("tracked keys = %d, max = %d", len(limiter.attempts), limiter.maxEntries)
	}
}

func TestPublicAuthAttemptLimiterPreservesCaseSensitiveAccountIdentity(t *testing.T) {
	limiter := newAuthAttemptLimiter(1, time.Minute, 8)
	if !limiter.allow("login", "203.0.113.1:1234", "Alice") {
		t.Fatal("first Alice attempt was blocked")
	}
	if !limiter.allow("login", "203.0.113.2:1234", "alice") {
		t.Fatal("distinct case-sensitive alice account shared Alice's bucket")
	}
	if limiter.allow("login", "203.0.113.3:1234", " Alice ") {
		t.Fatal("trim-equivalent Alice spelling bypassed its account limit")
	}
	if limiter.allow("login", "203.0.113.4:1234", "alice") {
		t.Fatal("alice account limit was not retained")
	}
	if len(limiter.attempts) != 4 {
		t.Fatalf("tracked keys = %d, want two IP and two account buckets", len(limiter.attempts))
	}
}

func TestPublicAuthAttemptLimiterDoesNotEvictActiveDenialsUnderChurn(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newAuthAttemptLimiter(2, time.Minute, 4)
	limiter.now = func() time.Time { return now }
	if !limiter.allow("login", "203.0.113.1:1234", "target") ||
		!limiter.allow("login", "203.0.113.1:1234", "target") {
		t.Fatal("target attempts were unexpectedly blocked before the limit")
	}
	if !limiter.allow("login", "203.0.113.2:1234", "filler") {
		t.Fatal("filler attempt was unexpectedly blocked")
	}
	for attempt := 0; attempt < 1000; attempt++ {
		limiter.allow("login", "198.51.100."+strconv.Itoa(attempt)+":4321", "churn-"+strconv.Itoa(attempt))
	}
	if limiter.allow("login", "203.0.113.1:9999", "target") {
		t.Fatal("capacity churn reset the denied target account")
	}
	if limiter.allow("login", "203.0.113.1:9999", "different-account") {
		t.Fatal("capacity churn reset the denied target IP")
	}
	if len(limiter.attempts) != limiter.maxEntries {
		t.Fatalf("tracked keys = %d, want %d", len(limiter.attempts), limiter.maxEntries)
	}
	now = now.Add(time.Minute)
	if !limiter.allow("login", "192.0.2.1:1234", "fresh") {
		t.Fatal("expired heap entries did not release capacity")
	}
}

func TestPublicAuthAttemptLimiterHashesLargeAccounts(t *testing.T) {
	limiter := newAuthAttemptLimiter(2, time.Minute, 4)
	largeAccount := strings.Repeat("a", maxJSONBodyBytes)
	if !limiter.allow("login", "203.0.113.1:1234", largeAccount) {
		t.Fatal("first large-account attempt was blocked")
	}
	if len(limiter.attempts) != 2 {
		t.Fatalf("large account created %d keys", len(limiter.attempts))
	}
	for key := range limiter.attempts {
		if len(key) != sha256.Size {
			t.Fatalf("attempt key size = %d", len(key))
		}
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
