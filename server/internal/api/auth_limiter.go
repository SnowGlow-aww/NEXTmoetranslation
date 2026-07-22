package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type attemptWindow struct {
	started time.Time
	count   int
}

type authAttemptLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxEntries int
	now        func() time.Time
	attempts   map[string]attemptWindow
}

func newAuthAttemptLimiter(limit int, window time.Duration, maxEntries int) *authAttemptLimiter {
	if maxEntries < 2 {
		maxEntries = 2
	}
	return &authAttemptLimiter{
		limit: limit, window: window, maxEntries: maxEntries,
		now: time.Now, attempts: map[string]attemptWindow{},
	}
}

func (l *authAttemptLimiter) allow(action, remoteAddr, account string) bool {
	now := l.now()
	keys := []string{
		action + "\x00ip\x00" + remoteIP(remoteAddr),
		action + "\x00account\x00" + strings.ToLower(strings.TrimSpace(account)),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, attempt := range l.attempts {
		if now.Sub(attempt.started) >= l.window {
			delete(l.attempts, key)
		}
	}
	for _, key := range keys {
		if attempt, ok := l.attempts[key]; ok && attempt.count >= l.limit {
			return false
		}
	}
	for len(l.attempts)+2 > l.maxEntries {
		var oldestKey string
		var oldest time.Time
		for key, attempt := range l.attempts {
			if oldestKey == "" || attempt.started.Before(oldest) {
				oldestKey, oldest = key, attempt.started
			}
		}
		if oldestKey == "" {
			break
		}
		delete(l.attempts, oldestKey)
	}
	for _, key := range keys {
		attempt, ok := l.attempts[key]
		if !ok {
			attempt.started = now
		}
		attempt.count++
		l.attempts[key] = attempt
	}
	return true
}

func (l *authAttemptLimiter) retryAfterSeconds() string {
	seconds := int64((l.window + time.Second - 1) / time.Second)
	return strconv.FormatInt(seconds, 10)
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddr)
}

func (s *Server) allowPublicAuthAttempt(w http.ResponseWriter, r *http.Request, action, account string) bool {
	if s.authAttempts.allow(action, r.RemoteAddr, account) {
		return true
	}
	w.Header().Set("Retry-After", s.authAttempts.retryAfterSeconds())
	writeErr(w, http.StatusTooManyRequests, "too many authentication attempts")
	return false
}
