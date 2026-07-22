package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
