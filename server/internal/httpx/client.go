// Package httpx provides consistently tuned HTTP clients for upstream data
// sources. The defaults fail over quickly enough for background jobs while
// still allowing large JSON responses to finish downloading.
package httpx

import (
	"net"
	"net/http"
	"strings"
	"time"
)

// NewClient returns an HTTP client with explicit connection, TLS, and response
// header timeouts. It clones http.DefaultTransport so proxy environment
// variables and HTTP/2 behavior remain compatible with the standard library.
func NewClient(timeout time.Duration) *http.Client {
	return NewClientWithTimeouts(timeout, 10*time.Second, 12*time.Second, 20*time.Second)
}

// NewClientWithTimeouts returns a client whose connection phases can be tuned
// for the workload. A zero timeout disables that phase's independent deadline;
// callers can still bound the whole request with a context.
func NewClientWithTimeouts(timeout, dialTimeout, tlsTimeout, responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = tlsTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

// NewHTTPSCredentialClient is for requests carrying API keys or signed
// authorization. Redirects remain allowed only within the exact HTTPS origin;
// a downgrade, userinfo, or cross-origin hop fails before credentials can be
// forwarded.
func NewHTTPSCredentialClient(timeout, dialTimeout, tlsTimeout, responseHeaderTimeout time.Duration) *http.Client {
	client := NewClientWithTimeouts(timeout, dialTimeout, tlsTimeout, responseHeaderTimeout)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || len(via) == 0 || req.URL.User != nil {
			return http.ErrUseLastResponse
		}
		original := via[0].URL
		if req.URL.Scheme != original.Scheme || !equalHost(req.URL.Host, original.Host) ||
			(req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && isLoopbackHost(req.URL.Hostname()))) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return client
}

func equalHost(left, right string) bool {
	return strings.EqualFold(left, right)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
