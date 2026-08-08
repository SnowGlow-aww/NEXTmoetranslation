package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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

func TestValidateUpstreamURLRejectsUnsafeSchemesAddressesAndComponents(t *testing.T) {
	policy := UpstreamPolicy{}
	unsafe := []string{
		"http://93.184.216.34/data",
		"https://user:secret@example.com/data",
		"https://example.com/data?token=x",
		"https://example.com/data#fragment",
		"https://example.com:8443/data",
		"https://example.com/data%0aheader",
		"https://127.0.0.1/data",
		"https://10.0.0.1/data",
		"https://172.16.0.1/data",
		"https://192.168.1.1/data",
		"https://100.64.0.1/data",
		"https://169.254.169.254/data",
		"https://[::1]/data",
		"https://[fc00::1]/data",
		"https://[fe80::1]/data",
		"https://[ff02::1]/data",
	}
	for _, target := range unsafe {
		if err := ValidateUpstreamURL(target, policy); err == nil {
			t.Errorf("unsafe upstream URL accepted: %q", target)
		}
	}
	for _, target := range []string{
		"https://example.com/data",
		"https://example.com:443/data",
		"https://93.184.216.34/data",
		"https://[2606:4700:4700::1111]/data",
	} {
		if err := ValidateUpstreamURL(target, policy); err != nil {
			t.Errorf("public HTTPS URL %q rejected: %v", target, err)
		}
	}
}

func TestValidateUpstreamEnvironmentRejectsProductionOverride(t *testing.T) {
	t.Setenv(UpstreamAllowInsecureLocalEnv, "true")
	if err := ValidateUpstreamEnvironment(true); err == nil {
		t.Fatal("production accepted local upstream override")
	}
	if err := ValidateUpstreamEnvironment(false); err != nil {
		t.Fatalf("development local upstream override rejected: %v", err)
	}
	t.Setenv(UpstreamAllowInsecureLocalEnv, "not-a-bool")
	if err := ValidateUpstreamEnvironment(false); err == nil {
		t.Fatal("malformed local upstream override accepted")
	}
}

func TestExplicitDevelopmentPolicyAllowsOnlyLoopbackHTTP(t *testing.T) {
	policy := UpstreamPolicy{AllowLocal: true}
	for _, target := range []string{"http://127.0.0.1:43210/data", "http://[::1]:43210/data", "http://localhost:43210/data"} {
		if err := ValidateUpstreamURL(target, policy); err != nil {
			t.Fatalf("local development URL %q rejected: %v", target, err)
		}
	}
	for _, target := range []string{"http://10.0.0.1:8080/data", "http://example.com/data"} {
		if err := ValidateUpstreamURL(target, policy); err == nil {
			t.Fatalf("non-loopback HTTP URL %q accepted", target)
		}
	}
}

type staticResolver struct {
	mu        sync.Mutex
	addresses map[string][]netip.Addr
	hosts     []string
}

func (resolver *staticResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.hosts = append(resolver.hosts, host)
	addresses, ok := resolver.addresses[host]
	if !ok {
		return nil, fmt.Errorf("unexpected DNS lookup for %s", host)
	}
	return append([]netip.Addr(nil), addresses...), nil
}

func (resolver *staticResolver) lookedUpHosts() []string {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return append([]string(nil), resolver.hosts...)
}

type recordingDialer struct {
	mu        sync.Mutex
	addresses []string
}

func (dialer *recordingDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	dialer.mu.Lock()
	dialer.addresses = append(dialer.addresses, address)
	dialer.mu.Unlock()
	return nil, errors.New("test dial stopped")
}

func (dialer *recordingDialer) dialedAddresses() []string {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return append([]string(nil), dialer.addresses...)
}

func TestUpstreamClientRejectsPrivateDNSAnswersBeforeDial(t *testing.T) {
	for name, address := range map[string]string{
		"IPv4 private":   "10.1.2.3",
		"IPv4 CGNAT":     "100.64.10.20",
		"IPv6 ULA":       "fd00::1",
		"IPv6 linklocal": "fe80::1",
	} {
		t.Run(name, func(t *testing.T) {
			resolver := &staticResolver{addresses: map[string][]netip.Addr{"private.example": {netip.MustParseAddr(address)}}}
			dialer := &recordingDialer{}
			client := NewUpstreamClientWithOptions(UpstreamClientOptions{
				Timeout: time.Second, Resolver: resolver, DialContext: dialer.DialContext,
			})
			_, err := client.Get("https://private.example/data")
			if err == nil || !strings.Contains(err.Error(), "not public") {
				t.Fatalf("private DNS answer %s error = %v", address, err)
			}
			if dialed := dialer.dialedAddresses(); len(dialed) != 0 {
				t.Fatalf("private DNS answer was dialed: %v", dialed)
			}
		})
	}
}

func TestUpstreamClientRejectsMixedPublicAndPrivateDNSAnswers(t *testing.T) {
	resolver := &staticResolver{addresses: map[string][]netip.Addr{
		"mixed.example": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("192.168.1.10")},
	}}
	dialer := &recordingDialer{}
	client := NewUpstreamClientWithOptions(UpstreamClientOptions{
		Timeout: time.Second, Resolver: resolver, DialContext: dialer.DialContext,
	})
	_, err := client.Get("https://mixed.example/data")
	if err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("mixed DNS answer error = %v", err)
	}
	if dialed := dialer.dialedAddresses(); len(dialed) != 0 {
		t.Fatalf("mixed DNS answer was partially dialed: %v", dialed)
	}
}

func TestUpstreamClientPinsValidatedPublicDNSAnswerAtDial(t *testing.T) {
	resolver := &staticResolver{addresses: map[string][]netip.Addr{
		"public.example": {netip.MustParseAddr("93.184.216.34")},
	}}
	dialer := &recordingDialer{}
	client := NewUpstreamClientWithOptions(UpstreamClientOptions{
		Timeout: time.Second, Resolver: resolver, DialContext: dialer.DialContext,
	})
	_, err := client.Get("https://public.example/data")
	if err == nil {
		t.Fatal("test dial unexpectedly succeeded")
	}
	if dialed := dialer.dialedAddresses(); len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("dialed addresses = %v, want pinned public IP", dialed)
	}
}

func TestUpstreamClientRevalidatesAndRestrictsRedirects(t *testing.T) {
	var crossOriginCalls atomic.Int32
	crossOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		crossOriginCalls.Add(1)
		_, _ = w.Write([]byte("unsafe"))
	}))
	defer crossOrigin.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/done", http.StatusFound)
		case "/done":
			_, _ = w.Write([]byte("ok"))
		case "/cross":
			http.Redirect(w, r, crossOrigin.URL+"/target", http.StatusFound)
		case "/query":
			http.Redirect(w, r, "/done?token=x", http.StatusFound)
		}
	}))
	defer origin.Close()

	client := NewUpstreamClientWithOptions(UpstreamClientOptions{
		Timeout: time.Second, DialTimeout: time.Second, Policy: UpstreamPolicy{AllowLocal: true},
	})
	response, err := client.Get(origin.URL + "/same")
	if err != nil {
		t.Fatalf("same-origin redirect rejected: %v", err)
	}
	response.Body.Close()
	for _, path := range []string{"/cross", "/query"} {
		response, err := client.Get(origin.URL + path)
		if response != nil {
			response.Body.Close()
		}
		if err == nil {
			t.Fatalf("unsafe redirect %s accepted", path)
		}
	}
	if calls := crossOriginCalls.Load(); calls != 0 {
		t.Fatalf("cross-origin redirect target received %d requests", calls)
	}

	queryClient := NewUpstreamClientWithOptions(UpstreamClientOptions{
		Timeout: time.Second, DialTimeout: time.Second, Policy: UpstreamPolicy{AllowLocal: true}, AllowQuery: true,
	})
	response, err = queryClient.Get(origin.URL + "/query?action=query")
	if err != nil {
		t.Fatalf("query-enabled same-origin redirect rejected: %v", err)
	}
	response.Body.Close()
}

func TestUpstreamClientIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")
	t.Setenv("ALL_PROXY", "http://proxy.invalid:8080")
	t.Setenv("NO_PROXY", "")
	resolver := &staticResolver{addresses: map[string][]netip.Addr{
		"service.example": {netip.MustParseAddr("93.184.216.34")},
	}}
	dialer := &recordingDialer{}
	client := NewUpstreamClientWithOptions(UpstreamClientOptions{
		Timeout: time.Second, Resolver: resolver, DialContext: dialer.DialContext,
	})
	_, _ = client.Get("https://service.example/data")
	if hosts := resolver.lookedUpHosts(); len(hosts) != 1 || hosts[0] != "service.example" {
		t.Fatalf("environment proxy influenced DNS target: %v", hosts)
	}
	if dialed := dialer.dialedAddresses(); len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("environment proxy influenced dial target: %v", dialed)
	}
}

func TestQueryEnabledUpstreamClientStillPinsDNSAndIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")
	t.Setenv("ALL_PROXY", "http://proxy.invalid:8080")
	t.Setenv("NO_PROXY", "")
	resolver := &staticResolver{addresses: map[string][]netip.Addr{
		"vocaloid.fandom.com": {netip.MustParseAddr("93.184.216.34")},
	}}
	dialer := &recordingDialer{}
	client := NewUpstreamClientWithOptions(UpstreamClientOptions{
		Timeout: time.Second, AllowQuery: true, Resolver: resolver, DialContext: dialer.DialContext,
	})
	_, err := client.Get("https://vocaloid.fandom.com/api.php?action=query&format=json")
	if err == nil || strings.Contains(err.Error(), "must not contain a query") {
		t.Fatalf("query-enabled request error = %v", err)
	}
	if hosts := resolver.lookedUpHosts(); len(hosts) != 1 || hosts[0] != "vocaloid.fandom.com" {
		t.Fatalf("query request did not use pinned origin DNS: %v", hosts)
	}
	if dialed := dialer.dialedAddresses(); len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("query request was influenced by proxy or unpinned DNS: %v", dialed)
	}
}
