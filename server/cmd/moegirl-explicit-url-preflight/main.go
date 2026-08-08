package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"moesekai/server/internal/lyricsprovidercoord"
	"moesekai/server/internal/lyricsproviderpolicy"
	"moesekai/server/internal/lyricssource"
)

const authorization = "MOESEKAI_MOEGIRL_EXPLICIT_URL_PREFLIGHT_AUTHORIZED"

var canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type targetInput struct {
	SchemaVersion int    `json:"schemaVersion"`
	MusicID       int    `json:"musicId"`
	PageURL       string `json:"pageUrl"`
}

type report struct {
	SchemaVersion     int                                   `json:"schemaVersion"`
	Provider          string                                `json:"provider"`
	TargetInputSHA256 string                                `json:"targetInputSha256"`
	MusicID           int                                   `json:"musicId"`
	CheckedAt         string                                `json:"checkedAt"`
	URLExists         bool                                  `json:"urlExists"`
	Failure           string                                `json:"failure,omitempty"`
	Status            lyricssource.MoegirlPageURLStatus     `json:"status"`
	Batch             lyricssource.MoegirlURLPreflightBatch `json:"batch"`
}

type options struct {
	targetPath, expectedTargetSHA, outputRoot, authorization, proxyURL string
	expectedMusicID                                                    int
	crawlDelay, requestTimeout, retryDelay                             time.Duration
	maxAttempts                                                        int
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) (resultErr error) {
	parsed, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	if parsed.authorization != authorization {
		_, writeErr := fmt.Fprintln(output, "HOLD mode=moegirl-explicit-url-preflight authorization=missing network=HOLD")
		return writeErr
	}
	body, err := readPinnedFile(parsed.targetPath, parsed.expectedTargetSHA, 1<<20)
	if err != nil {
		return err
	}
	var input targetInput
	if err := json.Unmarshal(body, &input); err != nil || input.SchemaVersion != 1 ||
		input.MusicID != parsed.expectedMusicID || input.PageURL == "" {
		return errors.New("explicit Moegirl target input is invalid")
	}
	target, err := lyricssource.MoegirlPageURLTargetForURL(input.PageURL)
	if err != nil {
		return err
	}
	if err := createPrivateOutputRoot(parsed.outputRoot); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(parsed.outputRoot)
		}
	}()
	owner, err := lyricsprovidercoord.AcquireDefault()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, owner.Close()) }()
	client, err := newExactProxyClient(parsed.proxyURL, parsed.requestTimeout)
	if err != nil {
		return err
	}
	wrapped, err := owner.WrapExactPublicPage(
		lyricsproviderpolicy.ProviderMoegirl, input.PageURL, client.Transport,
	)
	if err != nil {
		return err
	}
	client.Transport = wrapped
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	status, batch, preflightErr := lyricssource.PreflightMoegirlPageURL(
		ctx, target, client, parsed.maxAttempts, parsed.retryDelay,
	)
	resolveErr := owner.ResolveProvider(lyricsproviderpolicy.ProviderMoegirl)
	if len(batch.Raw) > 0 {
		if err := writePrivateExclusive(filepath.Join(parsed.outputRoot, "response.html"), batch.Raw); err != nil {
			return err
		}
		batch.Raw = nil
	}
	combinedErr := errors.Join(preflightErr, resolveErr)
	result := report{
		SchemaVersion: 1, Provider: "moegirl", TargetInputSHA256: parsed.expectedTargetSHA,
		MusicID: input.MusicID, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
		URLExists: combinedErr == nil, Status: status, Batch: batch,
	}
	if combinedErr != nil {
		result.Failure = combinedErr.Error()
	}
	reportBody, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := writePrivateExclusive(filepath.Join(parsed.outputRoot, "report.json"), append(reportBody, '\n')); err != nil {
		return err
	}
	if err := syncDirectory(parsed.outputRoot); err != nil {
		return err
	}
	cleanup = false
	if combinedErr != nil {
		return combinedErr
	}
	_, err = fmt.Fprintf(output,
		"PASS mode=moegirl-explicit-url-preflight musicID=%d status=%d redirected=%t output=%s\n",
		input.MusicID, status.StatusCode, status.Redirected, parsed.outputRoot,
	)
	return err
}

func parseOptions(arguments []string) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("moegirl-explicit-url-preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.targetPath, "target", "", "exact user-authorized page URL input")
	flags.StringVar(&parsed.expectedTargetSHA, "expected-target-sha256", "", "exact target input SHA-256")
	flags.IntVar(&parsed.expectedMusicID, "expected-music-id", 0, "exact catalog music ID")
	flags.StringVar(&parsed.outputRoot, "output", "", "create-exclusive private output root")
	flags.StringVar(&parsed.authorization, "authorization", "", "explicit live URL authorization")
	flags.StringVar(&parsed.proxyURL, "proxy-url", "", "exact approved local HTTP proxy URL")
	flags.DurationVar(&parsed.crawlDelay, "crawl-delay", 10*time.Second, "minimum request start interval")
	flags.DurationVar(&parsed.requestTimeout, "request-timeout", 2*time.Minute, "per-request timeout")
	flags.IntVar(&parsed.maxAttempts, "max-attempts", 5, "bounded retry attempts")
	flags.DurationVar(&parsed.retryDelay, "retry-delay", 30*time.Second, "additional retry delay")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return options{}, errors.New("explicit Moegirl URL preflight accepts only named flags")
	}
	for _, path := range []string{parsed.targetPath, parsed.outputRoot} {
		if path == "" || strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return options{}, errors.New("explicit Moegirl URL preflight paths must be canonical and absolute")
		}
	}
	if !canonicalSHA256.MatchString(parsed.expectedTargetSHA) || parsed.expectedMusicID <= 0 ||
		parsed.crawlDelay < 10*time.Second || parsed.requestTimeout <= 0 ||
		parsed.maxAttempts < 1 || parsed.maxAttempts > 5 || parsed.retryDelay < 0 {
		return options{}, errors.New("explicit Moegirl URL preflight bounds are invalid")
	}
	if _, err := parseExactProxyURL(parsed.proxyURL); err != nil {
		return options{}, err
	}
	return parsed, nil
}

func parseExactProxyURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.String() != value || parsed.Scheme != "http" ||
		parsed.User != nil || parsed.Host == "" || parsed.Port() == "" || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("explicit Moegirl proxy must be a canonical loopback HTTP URL with an explicit port")
	}
	address := net.ParseIP(parsed.Hostname())
	if address == nil || !address.IsLoopback() {
		return nil, errors.New("explicit Moegirl proxy must be a canonical loopback HTTP URL with an explicit port")
	}
	return parsed, nil
}

func newExactProxyClient(proxyValue string, timeout time.Duration) (*http.Client, error) {
	proxy, err := parseExactProxyURL(proxyValue)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxy)
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 20 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.MaxIdleConns = 4
	transport.MaxIdleConnsPerHost = 1
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func readPinnedFile(path, expectedSHA string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("explicit Moegirl target input is not a bounded regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	if actual := hex.EncodeToString(digest[:]); actual != expectedSHA {
		return nil, fmt.Errorf("explicit Moegirl target SHA-256=%s, want %s", actual, expectedSHA)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("explicit Moegirl target input changed while being read")
	}
	return body, nil
}

func createPrivateOutputRoot(path string) error {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0o700 {
		return errors.New("explicit Moegirl URL output parent must exist with mode 0700")
	}
	return os.Mkdir(path, 0o700)
}

func writePrivateExclusive(path string, body []byte) error {
	if len(body) == 0 {
		return errors.New("refusing to publish an empty explicit Moegirl URL artifact")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(body)
	if writeErr == nil && written != len(body) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
