package httpx

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestHTTPSCredentialClientRedirectPolicy(t *testing.T) {
	client := NewHTTPSCredentialClient(time.Second, time.Second, time.Second, time.Second)
	original, _ := url.Parse("https://service.example/v1")
	for name, target := range map[string]string{
		"same origin":  "https://service.example/v2",
		"changed port": "https://service.example:443/v2",
		"cross origin": "https://other.example/v2",
		"downgrade":    "http://service.example/v2",
		"userinfo":     "https://user:secret@service.example/v2",
	} {
		t.Run(name, func(t *testing.T) {
			next, _ := url.Parse(target)
			err := client.CheckRedirect(&http.Request{URL: next}, []*http.Request{{URL: original}})
			wantAllowed := name == "same origin"
			if wantAllowed && err != nil {
				t.Fatalf("same-origin HTTPS redirect rejected: %v", err)
			}
			if !wantAllowed && err == nil {
				t.Fatalf("unsafe redirect %q accepted", target)
			}
		})
	}
}
