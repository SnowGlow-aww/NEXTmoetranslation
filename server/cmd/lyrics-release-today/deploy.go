package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	deploymentReceiptSchemaVersion = 2
	deploymentReceiptKind          = "moesekai-lyrics-deployment-v2"
)

var deploymentReceiptForbiddenFields = map[string]struct{}{
	"lyrics": {}, "text": {}, "raw": {}, "translation": {}, "romaji": {},
	"romanization": {}, "romanized": {}, "performers": {}, "lines": {},
}

type deploymentReceipt struct {
	SchemaVersion           int    `json:"schemaVersion"`
	Kind                    string `json:"kind"`
	Environment             string `json:"environment"`
	ValidationReceiptSHA256 string `json:"validationReceiptSha256"`
	RootSHA256              string `json:"rootSha256"`
	ImportBatchSHA256       string `json:"importBatchSha256"`
	ImportReceiptSHA256     string `json:"importReceiptSha256"`
	ArtifactDigest          string `json:"artifactDigest"`
	BaseURL                 string `json:"baseUrl"`
	DeployedAt              string `json:"deployedAt"`
	VerifiedAt              string `json:"verifiedAt"`
	ReceiptSHA256           string `json:"receiptSha256"`
}

type deployResult struct {
	RootSHA256     string
	ArtifactDigest string
}

func runCheckDeploy(ctx context.Context, arguments []string) (deployResult, error) {
	var receiptPath, validationReceiptPath, rootPath, manifestPath, importReceiptPath, baseURL string
	flags := flag.NewFlagSet("check-deploy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&receiptPath, "receipt", "", "content-free deployment receipt")
	flags.StringVar(&validationReceiptPath, "validation-receipt", "", "exact fresh-validation receipt")
	flags.StringVar(&rootPath, "root-manifest", "", "validated final root manifest")
	flags.StringVar(&manifestPath, "import-manifest", "", "validated import manifest")
	flags.StringVar(&importReceiptPath, "import-receipt", "", "durable import receipt")
	flags.StringVar(&baseURL, "base-url", "", "expected production HTTPS origin")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return deployResult{}, errors.New("check-deploy requires -receipt, release bindings, -import-receipt, and -base-url")
	}
	for _, path := range []string{receiptPath, validationReceiptPath, rootPath, manifestPath, importReceiptPath} {
		if !canonicalAbsolutePath(path) {
			return deployResult{}, errors.New("check-deploy file paths must be explicit canonical absolute paths")
		}
	}
	parsedBase, err := validateHTTPSBaseURL(baseURL)
	if err != nil {
		return deployResult{}, err
	}
	bundle, err := loadValidatedReleaseBundle(validationReceiptPath, rootPath, manifestPath)
	if err != nil {
		return deployResult{}, err
	}
	_, _, importReceiptSHA256, err := loadBoundReleaseImportReceipt(importReceiptPath, bundle)
	if err != nil {
		return deployResult{}, err
	}
	body, _, err := readPinnedRegular(receiptPath, "deployment receipt", maxReceiptBytes, 0o600)
	if err != nil {
		return deployResult{}, err
	}
	if err := rejectJSONKeys(body, deploymentReceiptForbiddenFields, "deployment receipt"); err != nil {
		return deployResult{}, err
	}
	var receipt deploymentReceipt
	if err := decodeStrictJSON(body, &receipt, "deployment receipt"); err != nil {
		return deployResult{}, err
	}
	if err := validateDeploymentReceipt(receipt, bundle, importReceiptSHA256, baseURL); err != nil {
		return deployResult{}, err
	}
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if err := checkProbe(ctx, client, parsedBase, "/healthz", []byte(`{"status":"ok"}`)); err != nil {
		return deployResult{}, err
	}
	if err := checkProbe(ctx, client, parsedBase, "/readyz", []byte(`{"status":"ready"}`)); err != nil {
		return deployResult{}, err
	}
	return deployResult{RootSHA256: receipt.RootSHA256, ArtifactDigest: receipt.ArtifactDigest}, nil
}

func validateDeploymentReceipt(receipt deploymentReceipt, bundle validatedReleaseBundle, importReceiptSHA256, baseURL string) error {
	deployedAt, err := canonicalTimestamp(receipt.DeployedAt)
	if err != nil {
		return fmt.Errorf("deployment receipt deployedAt: %w", err)
	}
	verifiedAt, err := canonicalTimestamp(receipt.VerifiedAt)
	if err != nil || verifiedAt.Before(deployedAt) {
		return errors.New("deployment receipt verifiedAt must be canonical and no earlier than deployedAt")
	}
	if receipt.SchemaVersion != deploymentReceiptSchemaVersion || receipt.Kind != deploymentReceiptKind ||
		receipt.Environment != "production" || receipt.ValidationReceiptSHA256 != bundle.Validation.ReceiptSHA256 ||
		receipt.RootSHA256 != bundle.Bindings.Root.RootSHA256 ||
		receipt.ImportBatchSHA256 != bundle.Bindings.Manifest.BatchSHA256 ||
		receipt.ImportReceiptSHA256 != importReceiptSHA256 || !artifactDigestRE.MatchString(receipt.ArtifactDigest) ||
		receipt.BaseURL != baseURL || !lowerSHA256Pattern.MatchString(receipt.ReceiptSHA256) {
		return errors.New("deployment receipt does not match the exact production release inputs")
	}
	digest, err := deploymentReceiptDigest(receipt)
	if err != nil || digest != receipt.ReceiptSHA256 {
		return errors.New("deployment receipt digest does not match")
	}
	return nil
}

func deploymentReceiptDigest(receipt deploymentReceipt) (string, error) {
	receipt.ReceiptSHA256 = ""
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(deploymentReceiptKind + "\x00"))
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateHTTPSBaseURL(value string) (*url.URL, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.HasSuffix(value, "/") {
		return nil, errors.New("base URL must be an explicit canonical HTTPS origin without a trailing slash")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("base URL must be an explicit canonical HTTPS origin without path, query, userinfo, or fragment")
	}
	if parsed.String() != value {
		return nil, errors.New("base URL is not canonical")
	}
	return parsed, nil
}

func checkProbe(ctx context.Context, client *http.Client, base *url.URL, path string, expected []byte) error {
	requestURL := *base
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDeploymentProbeBody+1))
	if err != nil || len(body) > maxDeploymentProbeBody {
		return fmt.Errorf("GET %s returned an unreadable or oversized body", path)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, expected) ||
		response.Header.Get("Cache-Control") != "no-store" {
		return fmt.Errorf("GET %s did not return the exact healthy no-store response", path)
	}
	return nil
}
