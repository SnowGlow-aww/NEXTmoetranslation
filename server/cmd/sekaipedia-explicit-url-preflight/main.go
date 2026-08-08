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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"moesekai/server/internal/httpx"
	"moesekai/server/internal/lyricsprovidercoord"
	"moesekai/server/internal/lyricsproviderpolicy"
	"moesekai/server/internal/lyricssource"
)

const authorization = "MOESEKAI_SEKAIPEDIA_EXPLICIT_URL_PREFLIGHT_AUTHORIZED"

var canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type targetInput struct {
	SchemaVersion int      `json:"schemaVersion"`
	PageTitles    []string `json:"pageTitles"`
}

type report struct {
	SchemaVersion     int                                        `json:"schemaVersion"`
	Provider          string                                     `json:"provider"`
	TargetInputSHA256 string                                     `json:"targetInputSha256"`
	TargetCount       int                                        `json:"targetCount"`
	CheckedAt         string                                     `json:"checkedAt"`
	AllURLsExist      bool                                       `json:"allUrlsExist"`
	Failure           string                                     `json:"failure,omitempty"`
	Statuses          []lyricssource.SekaipediaPageURLStatus     `json:"statuses"`
	Batches           []lyricssource.SekaipediaURLPreflightBatch `json:"batches"`
}

type options struct {
	targetsPath, expectedTargetsSHA, outputRoot, authorization string
	crawlDelay, requestTimeout, retryDelay                     time.Duration
	maxAttempts                                                int
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
		_, writeErr := fmt.Fprintln(output, "HOLD mode=sekaipedia-explicit-url-preflight authorization=missing network=HOLD")
		return writeErr
	}
	body, err := readPinnedFile(parsed.targetsPath, parsed.expectedTargetsSHA, 1<<20)
	if err != nil {
		return err
	}
	var input targetInput
	if err := json.Unmarshal(body, &input); err != nil || input.SchemaVersion != 1 || len(input.PageTitles) == 0 || len(input.PageTitles) > 50 {
		return errors.New("explicit Sekaipedia target input is invalid")
	}
	targets := make([]lyricssource.SekaipediaPageURLTarget, len(input.PageTitles))
	for index, title := range input.PageTitles {
		target, err := lyricssource.SekaipediaPageURLTargetForTitle(title)
		if err != nil {
			return err
		}
		targets[index] = target
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
	client := httpx.NewUpstreamClientWithOptions(httpx.UpstreamClientOptions{
		Timeout: parsed.requestTimeout, DialTimeout: 10 * time.Second,
		TLSHandshakeTimeout: 12 * time.Second, ResponseHeaderTimeout: 20 * time.Second,
		Policy: httpx.UpstreamPolicyFromEnvironment(), AllowQuery: true,
	})
	wrapped, err := owner.Wrap(lyricsproviderpolicy.ProviderSekaipedia, client.Transport)
	if err != nil {
		return err
	}
	client.Transport = wrapped
	statuses, batches, preflightErr := lyricssource.PreflightSekaipediaPageURLs(
		ctx, targets, client, parsed.crawlDelay, 10*time.Second, parsed.maxAttempts, parsed.retryDelay,
	)
	resolveErr := owner.ResolveProvider(lyricsproviderpolicy.ProviderSekaipedia)
	responsesRoot := filepath.Join(parsed.outputRoot, "responses")
	if err := os.Mkdir(responsesRoot, 0o700); err != nil {
		return err
	}
	for index := range batches {
		if err := writePrivateExclusive(filepath.Join(responsesRoot, fmt.Sprintf("batch-%04d.json", index+1)), batches[index].Raw); err != nil {
			return err
		}
		batches[index].Raw = nil
	}
	combinedErr := errors.Join(preflightErr, resolveErr)
	result := report{
		SchemaVersion: 1, Provider: "sekaipedia", TargetInputSHA256: parsed.expectedTargetsSHA,
		TargetCount: len(statuses), CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
		AllURLsExist: combinedErr == nil, Statuses: statuses, Batches: batches,
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
	_, err = fmt.Fprintf(output, "PASS mode=sekaipedia-explicit-url-preflight targets=%d batches=%d allURLsExist=true output=%s\n",
		len(statuses), len(batches), parsed.outputRoot)
	return err
}

func parseOptions(arguments []string) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("sekaipedia-explicit-url-preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.targetsPath, "targets", "", "exact explicit page-title input")
	flags.StringVar(&parsed.expectedTargetsSHA, "expected-targets-sha256", "", "exact target input SHA-256")
	flags.StringVar(&parsed.outputRoot, "output", "", "create-exclusive private output root")
	flags.StringVar(&parsed.authorization, "authorization", "", "explicit live URL authorization")
	flags.DurationVar(&parsed.crawlDelay, "crawl-delay", 10*time.Second, "minimum request start interval")
	flags.DurationVar(&parsed.requestTimeout, "request-timeout", 2*time.Minute, "per-request timeout")
	flags.IntVar(&parsed.maxAttempts, "max-attempts", 5, "bounded retry attempts")
	flags.DurationVar(&parsed.retryDelay, "retry-delay", 30*time.Second, "additional retry delay")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return options{}, errors.New("explicit URL preflight accepts only named flags")
	}
	for _, path := range []string{parsed.targetsPath, parsed.outputRoot} {
		if path == "" || strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return options{}, errors.New("explicit URL preflight paths must be canonical and absolute")
		}
	}
	if !canonicalSHA256.MatchString(parsed.expectedTargetsSHA) || parsed.crawlDelay < 10*time.Second ||
		parsed.requestTimeout <= 0 || parsed.maxAttempts < 1 || parsed.maxAttempts > 5 || parsed.retryDelay < 0 {
		return options{}, errors.New("explicit URL preflight bounds are invalid")
	}
	return parsed, nil
}

func readPinnedFile(path, expectedSHA string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("explicit target input is not a bounded regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	if actual := hex.EncodeToString(digest[:]); actual != expectedSHA {
		return nil, fmt.Errorf("explicit target SHA-256=%s, want %s", actual, expectedSHA)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("explicit target input changed while being read")
	}
	return body, nil
}

func createPrivateOutputRoot(path string) error {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0o700 {
		return errors.New("explicit URL output parent must exist with mode 0700")
	}
	return os.Mkdir(path, 0o700)
}

func writePrivateExclusive(path string, body []byte) error {
	if len(body) == 0 {
		return errors.New("refusing to publish an empty explicit URL artifact")
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
