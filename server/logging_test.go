package main

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type deadlineResponseWriter struct {
	http.ResponseWriter
	deadlineSet bool
}

func (w *deadlineResponseWriter) SetWriteDeadline(time.Time) error {
	w.deadlineSet = true
	return nil
}

func TestLoggingMiddlewareAddsSafeRequestIDAndCountsFailures(t *testing.T) {
	httpRequestTotal.Store(0)
	httpClientErrors.Store(0)
	httpServerErrors.Store(0)
	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusServiceUnavailable)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	request.Header.Set("X-Request-ID", "invalid request id")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	requestID := recorder.Header().Get("X-Request-ID")
	if !validRequestID(requestID) || requestID == "invalid request id" {
		t.Fatalf("request ID = %q", requestID)
	}
	if httpRequestTotal.Load() != 1 || httpServerErrors.Load() != 1 || httpClientErrors.Load() != 0 {
		t.Fatalf("counters total=%d client=%d server=%d", httpRequestTotal.Load(), httpClientErrors.Load(), httpServerErrors.Load())
	}
}

func TestLoggingWriterExposesTransportDeadlines(t *testing.T) {
	underlying := &deadlineResponseWriter{ResponseWriter: httptest.NewRecorder()}
	wrapped := &loggingResponseWriter{ResponseWriter: underlying}
	if err := http.NewResponseController(wrapped).SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	if !underlying.deadlineSet {
		t.Fatal("logging writer hid the transport write deadline")
	}
}

type hijackerResponseWriter struct {
	http.ResponseWriter
}

func (w *hijackerResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}

func TestLoggingWriterImplementsHijacker(t *testing.T) {
	wrapped := &loggingResponseWriter{ResponseWriter: &hijackerResponseWriter{ResponseWriter: httptest.NewRecorder()}}
	if _, ok := any(wrapped).(http.Hijacker); !ok {
		t.Fatal("loggingResponseWriter does not implement http.Hijacker interface")
	}
}
