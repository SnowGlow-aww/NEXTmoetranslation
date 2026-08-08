// Package httpx provides consistently tuned HTTP clients for upstream data
// sources. The defaults fail over quickly enough for background jobs while
// still allowing large JSON responses to finish downloading.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const UpstreamAllowInsecureLocalEnv = "UPSTREAM_ALLOW_INSECURE_LOCAL"

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// UpstreamPolicy controls the narrow development exception for local HTTP test
// servers. Production always ignores the exception and requires public HTTPS.
type UpstreamPolicy struct {
	AllowLocal bool
}

// NetIPResolver is the DNS surface used by the pinned upstream dialer.
type NetIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// DialContextFunc is injectable so callers can verify the exact validated IP
// address selected by the upstream transport without making network requests.
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

// UpstreamClientOptions configures a fail-closed client for public data URLs.
type UpstreamClientOptions struct {
	Timeout               time.Duration
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	Policy                UpstreamPolicy
	AllowQuery            bool
	Resolver              NetIPResolver
	DialContext           DialContextFunc
}

// UpstreamPolicyFromEnvironment returns the runtime upstream policy. Local HTTP
// is enabled only by an explicit development/test override; a true or malformed
// production flag always fails closed.
func UpstreamPolicyFromEnvironment() UpstreamPolicy {
	productionValue := strings.TrimSpace(os.Getenv("MOESEKAI_PRODUCTION"))
	production := productionValue != ""
	if parsed, err := strconv.ParseBool(productionValue); err == nil {
		production = parsed
	}
	allowLocal := strings.TrimSpace(os.Getenv(UpstreamAllowInsecureLocalEnv)) == "true"
	return UpstreamPolicy{AllowLocal: allowLocal && !production}
}

// ValidateUpstreamEnvironment rejects a malformed or production-enabled local
// override during startup rather than silently weakening the configured mode.
func ValidateUpstreamEnvironment(production bool) error {
	value := strings.TrimSpace(os.Getenv(UpstreamAllowInsecureLocalEnv))
	if value == "" {
		return nil
	}
	if value != "true" && value != "false" {
		return fmt.Errorf("%s must be true or false", UpstreamAllowInsecureLocalEnv)
	}
	if production && value == "true" {
		return fmt.Errorf("%s cannot be enabled in production", UpstreamAllowInsecureLocalEnv)
	}
	return nil
}

// ValidateUpstreamURL enforces the syntax and literal-address portion of the
// upstream network policy. DNS answers are validated and pinned by the client at
// dial time so a hostname cannot bypass these checks through resolution.
func ValidateUpstreamURL(raw string, policy UpstreamPolicy) error {
	return validateUpstreamURL(raw, policy, false)
}

func validateUpstreamURL(raw string, policy UpstreamPolicy, allowQuery bool) error {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return errors.New("must be a non-empty absolute URL without surrounding whitespace")
	}
	if hasControlCharacters(raw) {
		return errors.New("must not contain control characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return fmt.Errorf("must be a valid absolute URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return errors.New("must be an absolute URL with a host")
	}
	if parsed.User != nil {
		return errors.New("must not contain userinfo")
	}
	if !allowQuery && (parsed.RawQuery != "" || parsed.ForceQuery) {
		return errors.New("must not contain a query")
	}
	if parsed.Fragment != "" {
		return errors.New("must not contain a fragment")
	}
	if hasControlCharacters(parsed.Path) || hasControlCharacters(parsed.RawPath) {
		return errors.New("must not contain encoded control characters")
	}

	scheme := strings.ToLower(parsed.Scheme)
	host := parsed.Hostname()
	localHost := isLoopbackHostname(host)
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		localHost = address.IsLoopback()
		if err := validateResolvedAddress(address, policy); err != nil {
			return err
		}
	} else if localHost && !policy.AllowLocal {
		return errors.New("loopback upstream hosts are not allowed")
	}

	switch scheme {
	case "https":
		if port := parsed.Port(); port != "" && port != "443" && !(policy.AllowLocal && localHost) {
			return fmt.Errorf("unexpected HTTPS port %s", port)
		}
	case "http":
		if !policy.AllowLocal || !localHost {
			return errors.New("upstream URLs must use HTTPS; HTTP is limited to explicitly enabled local development servers")
		}
	default:
		return errors.New("upstream URLs must use HTTPS")
	}
	return nil
}

func hasControlCharacters(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func isLoopbackHostname(host string) bool {
	return strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(host), "."), "localhost")
}

func validateResolvedAddress(address netip.Addr, policy UpstreamPolicy) error {
	if !address.IsValid() {
		return errors.New("upstream address is invalid")
	}
	if address.Is4In6() {
		address = address.Unmap()
	}
	if policy.AllowLocal && address.IsLoopback() {
		return nil
	}
	if address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() || cgnatPrefix.Contains(address) {
		return fmt.Errorf("upstream address %s is not public", address)
	}
	if !address.IsGlobalUnicast() {
		return fmt.Errorf("upstream address %s is not a public unicast address", address)
	}
	return nil
}

// NewClient returns a fail-closed upstream data client with explicit connection,
// TLS, and response-header timeouts. Environment proxy variables are disabled.
func NewClient(timeout time.Duration) *http.Client {
	return NewClientWithTimeouts(timeout, 10*time.Second, 12*time.Second, 20*time.Second)
}

// NewClientWithTimeouts returns a fail-closed upstream client whose connection
// phases can be tuned for the workload. A zero timeout disables that phase's
// independent deadline; callers can still bound the request with a context.
func NewClientWithTimeouts(timeout, dialTimeout, tlsTimeout, responseHeaderTimeout time.Duration) *http.Client {
	return NewUpstreamClientWithOptions(UpstreamClientOptions{
		Timeout:               timeout,
		DialTimeout:           dialTimeout,
		TLSHandshakeTimeout:   tlsTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		Policy:                UpstreamPolicyFromEnvironment(),
	})
}

// NewUpstreamClientWithOptions builds a secure client with injectable DNS and
// dialing. Every hostname is resolved, every answer must satisfy the policy,
// and the transport dials one of those validated IPs directly.
func NewUpstreamClientWithOptions(options UpstreamClientOptions) *http.Client {
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialContext := options.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{Timeout: options.DialTimeout, KeepAlive: 30 * time.Second}
		dialContext = dialer.DialContext
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = pinnedDialContext(resolver, dialContext, options.Policy)
	transport.TLSHandshakeTimeout = options.TLSHandshakeTimeout
	transport.ResponseHeaderTimeout = options.ResponseHeaderTimeout
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16

	return &http.Client{
		Transport: &upstreamRoundTripper{transport: transport, policy: options.Policy, allowQuery: options.AllowQuery},
		Timeout:   options.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 || len(via) >= 5 {
				return errors.New("upstream redirect limit exceeded")
			}
			if err := validateUpstreamURL(req.URL.String(), options.Policy, options.AllowQuery); err != nil {
				return fmt.Errorf("unsafe upstream redirect: %w", err)
			}
			original := via[0].URL
			if !strings.EqualFold(req.URL.Scheme, original.Scheme) || !strings.EqualFold(req.URL.Host, original.Host) {
				return errors.New("upstream redirects must remain on the exact original origin")
			}
			return nil
		},
	}
}

type upstreamRoundTripper struct {
	transport  *http.Transport
	policy     UpstreamPolicy
	allowQuery bool
}

func (roundTripper *upstreamRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("upstream request URL is required")
	}
	if err := validateUpstreamURL(request.URL.String(), roundTripper.policy, roundTripper.allowQuery); err != nil {
		return nil, fmt.Errorf("unsafe upstream URL: %w", err)
	}
	return roundTripper.transport.RoundTrip(request)
}

func pinnedDialContext(resolver NetIPResolver, dial DialContextFunc, policy UpstreamPolicy) DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split upstream address %q: %w", address, err)
		}
		addresses, err := resolveAddresses(ctx, resolver, network, host)
		if err != nil {
			return nil, err
		}
		for _, resolved := range addresses {
			if err := validateResolvedAddress(resolved, policy); err != nil {
				return nil, fmt.Errorf("resolve upstream host %s: %w", host, err)
			}
		}

		var failures []string
		for _, resolved := range addresses {
			if network == "tcp4" && !resolved.Is4() {
				continue
			}
			if network == "tcp6" && resolved.Is4() {
				continue
			}
			pinned := net.JoinHostPort(resolved.String(), port)
			connection, dialErr := dial(ctx, network, pinned)
			if dialErr == nil {
				return connection, nil
			}
			failures = append(failures, dialErr.Error())
		}
		if len(failures) == 0 {
			return nil, fmt.Errorf("resolve upstream host %s: no address compatible with %s", host, network)
		}
		return nil, fmt.Errorf("dial validated upstream host %s: %s", host, strings.Join(failures, "; "))
	}
}

func resolveAddresses(ctx context.Context, resolver NetIPResolver, network, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address}, nil
	}
	lookupNetwork := "ip"
	if network == "tcp4" {
		lookupNetwork = "ip4"
	} else if network == "tcp6" {
		lookupNetwork = "ip6"
	}
	addresses, err := resolver.LookupNetIP(ctx, lookupNetwork, host)
	if err != nil {
		return nil, fmt.Errorf("resolve upstream host %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("resolve upstream host %s: no addresses", host)
	}
	return addresses, nil
}

// NewHTTPSCredentialClient is for requests carrying API keys or signed
// authorization. Redirects remain allowed only within the exact HTTPS origin;
// a downgrade, userinfo, or cross-origin hop fails before credentials can be
// forwarded. Environment proxy variables are disabled.
func NewHTTPSCredentialClient(timeout, dialTimeout, tlsTimeout, responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = tlsTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16
	client := &http.Client{Transport: transport, Timeout: timeout}
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
