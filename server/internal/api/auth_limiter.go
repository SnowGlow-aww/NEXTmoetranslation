package api

import (
	"container/heap"
	"crypto/sha256"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type attemptWindow struct {
	key     attemptKey
	expires time.Time
	count   int
}

type attemptKey [sha256.Size]byte

type expiryQueue []*attemptWindow

func (q expiryQueue) Len() int           { return len(q) }
func (q expiryQueue) Less(i, j int) bool { return q[i].expires.Before(q[j].expires) }
func (q expiryQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *expiryQueue) Push(value any)    { *q = append(*q, value.(*attemptWindow)) }
func (q *expiryQueue) Pop() any {
	old := *q
	last := old[len(old)-1]
	*q = old[:len(old)-1]
	return last
}

type authAttemptLimiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	maxEntries  int
	now         func() time.Time
	attempts    map[attemptKey]*attemptWindow
	expirations expiryQueue
}

func newAuthAttemptLimiter(limit int, window time.Duration, maxEntries int) *authAttemptLimiter {
	if maxEntries < 2 {
		maxEntries = 2
	}
	return &authAttemptLimiter{
		limit: limit, window: window, maxEntries: maxEntries,
		now: time.Now, attempts: map[attemptKey]*attemptWindow{},
	}
}

func (l *authAttemptLimiter) allow(action, remoteAddr, account string) bool {
	now := l.now()
	keys := []attemptKey{
		hashAttemptKey(action, "ip", remoteIP(remoteAddr)),
		hashAttemptKey(action, "account", strings.TrimSpace(account)),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneExpired(now)
	for _, key := range keys {
		if attempt := l.attempts[key]; attempt != nil && attempt.count >= l.limit {
			return false
		}
	}
	missing := 0
	for _, key := range keys {
		if l.attempts[key] == nil {
			missing++
		}
	}
	if len(l.attempts)+missing > l.maxEntries {
		for _, key := range keys {
			if attempt := l.attempts[key]; attempt != nil {
				attempt.count++
			}
		}
		return false
	}
	for _, key := range keys {
		attempt := l.attempts[key]
		if attempt == nil {
			attempt = &attemptWindow{key: key, expires: now.Add(l.window)}
			l.attempts[key] = attempt
			heap.Push(&l.expirations, attempt)
		}
		attempt.count++
	}
	return true
}

func (l *authAttemptLimiter) pruneExpired(now time.Time) {
	for l.expirations.Len() > 0 && !l.expirations[0].expires.After(now) {
		attempt := heap.Pop(&l.expirations).(*attemptWindow)
		if l.attempts[attempt.key] == attempt {
			delete(l.attempts, attempt.key)
		}
	}
}

func hashAttemptKey(action, kind, value string) attemptKey {
	return sha256.Sum256([]byte(action + "\x00" + kind + "\x00" + value))
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
