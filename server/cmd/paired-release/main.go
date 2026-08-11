package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/workspaceverify"
)

const (
	producerRepository   = "https://github.com/SnowGlow-aww/SekaiText-Moe"
	producerWorkflow     = ".github/workflows/release.yml"
	certificateIssuer    = "https://token.actions.githubusercontent.com"
	workspaceArchiveRoot = "dist-web-workspace"
	workspaceAssetPrefix = "sekaitext-moe-web-workspace-"
	maxArchiveBytes      = 256 << 20
	maxBundleBytes       = 16 << 20
	maxGitHubJSONBytes   = 1 << 20
	maxTarPaddingBytes   = 1 << 20
	intotoPayloadType    = "application/vnd.in-toto+json"
	intotoStatementType  = "https://in-toto.io/Statement/v0.1"
)

var (
	canonicalCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
	canonicalDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	canonicalTag    = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	githubSourceURL = regexp.MustCompile(`^https://github\.com/[0-9A-Za-z_.-]+/[0-9A-Za-z_.-]+$`)
	ghcrImageName   = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9._/-]*$`)
	ghcrTaggedImage = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

type releaseAsset struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Size  int64  `json:"size"`
}

type githubRelease struct {
	TagName   string         `json:"tag_name"`
	Draft     bool           `json:"draft"`
	Immutable bool           `json:"immutable"`
	Assets    []releaseAsset `json:"assets"`
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitReference struct {
	Ref    string    `json:"ref"`
	Object gitObject `json:"object"`
}

type gitTag struct {
	Object gitObject `json:"object"`
}

type consumeOptions struct {
	assetsDirectory string
	workspace       string
	tag             string
	commit          string
	githubOutput    string
	cosign          string
}

type consumeResult struct {
	archiveDigest  string
	manifestDigest string
	workspace      string
	ociTag         string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "paired release: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: paired-release <validate-inputs|resolve-tag|select-assets|validate-downloads|consume|predicate|validate-attestation|validate-tags>")
	}
	switch arguments[0] {
	case "validate-inputs":
		flags := newFlagSet("validate-inputs")
		tag := flags.String("moe-tag", "", "producer release tag")
		commit := flags.String("moe-commit", "", "producer commit")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		return validatePairInputs(*tag, *commit)
	case "resolve-tag":
		flags := newFlagSet("resolve-tag")
		tag := flags.String("moe-tag", "", "producer release tag")
		commit := flags.String("moe-commit", "", "expected producer commit")
		apiURL := flags.String("api-url", os.Getenv("GITHUB_API_URL"), "GitHub API base URL")
		tokenEnvironment := flags.String("token-env", "GH_TOKEN", "environment variable containing the GitHub token")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		if *tokenEnvironment == "" || os.Getenv(*tokenEnvironment) == "" {
			return errors.New("GitHub token environment variable is required")
		}
		client := &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		return resolveProducerTag(context.Background(), client, *apiURL, os.Getenv(*tokenEnvironment), *tag, *commit)
	case "select-assets":
		flags := newFlagSet("select-assets")
		releaseJSON := flags.String("release-json", "", "GitHub release metadata")
		output := flags.String("output", "", "validated download plan")
		tag := flags.String("moe-tag", "", "producer release tag")
		commit := flags.String("moe-commit", "", "producer commit")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		return selectAssets(*releaseJSON, *output, *tag, *commit)
	case "validate-downloads":
		flags := newFlagSet("validate-downloads")
		plan := flags.String("download-plan", "", "validated download plan")
		assets := flags.String("assets-dir", "", "downloaded release assets")
		commit := flags.String("moe-commit", "", "producer commit")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		return validateDownloadedAssets(*plan, *assets, *commit)
	case "consume":
		flags := newFlagSet("consume")
		assets := flags.String("assets-dir", "", "downloaded workspace release assets")
		workspace := flags.String("workspace-dir", "", "new extraction destination")
		tag := flags.String("moe-tag", "", "producer release tag")
		commit := flags.String("moe-commit", "", "producer commit")
		githubOutput := flags.String("github-output", "", "GitHub Actions output file")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		result, err := consumeRelease(consumeOptions{
			assetsDirectory: *assets,
			workspace:       *workspace,
			tag:             *tag,
			commit:          *commit,
			githubOutput:    *githubOutput,
			cosign:          "cosign",
		})
		if err != nil {
			return err
		}
		fmt.Printf("verified paired workspace archive sha256:%s and manifest sha256:%s\n", result.archiveDigest, result.manifestDigest)
		return nil
	case "predicate":
		flags := newFlagSet("predicate")
		output := flags.String("output", "", "predicate output path")
		nextRepository := flags.String("next-repository", "", "NEXT source repository URL")
		nextCommit := flags.String("next-commit", "", "NEXT source commit")
		moeTag := flags.String("moe-tag", "", "producer release tag")
		moeCommit := flags.String("moe-commit", "", "producer commit")
		archiveDigest := flags.String("archive-sha256", "", "workspace archive digest")
		manifestDigest := flags.String("manifest-sha256", "", "workspace manifest digest")
		image := flags.String("image", "", "final OCI image name")
		imageDigest := flags.String("image-digest", "", "final OCI image digest")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		return writePredicate(*output, *nextRepository, *nextCommit, *moeTag, *moeCommit, *archiveDigest, *manifestDigest, *image, *imageDigest)
	case "validate-attestation":
		flags := newFlagSet("validate-attestation")
		verificationJSON := flags.String("verification-json", "", "Cosign verification output")
		predicate := flags.String("predicate", "", "expected paired predicate")
		predicateType := flags.String("predicate-type", "", "expected predicate type")
		image := flags.String("image", "", "expected image name")
		imageDigest := flags.String("image-digest", "", "expected image digest")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		return validateAttestationFiles(*verificationJSON, *predicate, *predicateType, *image, *imageDigest)
	case "validate-tags":
		flags := newFlagSet("validate-tags")
		state := flags.String("state", "", "inspected tag digest state")
		missingOutput := flags.String("missing-output", "", "new file for missing tags")
		expectedDigest := flags.String("expected-image-digest", "", "expected image digest")
		tagSemver := flags.String("tag-semver", "", "final pair SemVer tag")
		tagCommit := flags.String("tag-commit", "", "final pair commit tag")
		requirePresent := flags.Bool("require-present", false, "reject missing tags")
		if err := parseFlags(flags, arguments[1:]); err != nil {
			return err
		}
		return validateFinalTagState(*state, *missingOutput, *expectedDigest, []string{*tagSemver, *tagCommit}, *requirePresent)
	default:
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parseFlags(flags *flag.FlagSet, arguments []string) error {
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return nil
}

func validatePairInputs(tag, commit string) error {
	if len(tag) > 64 || !canonicalTag.MatchString(tag) {
		return errors.New("moe_tag must be v followed by strict SemVer and at most 64 characters")
	}
	if !canonicalCommit.MatchString(commit) {
		return errors.New("moe_commit must be exactly 40 lowercase hexadecimal characters")
	}
	return nil
}

func ociTag(tag string) string {
	return strings.ReplaceAll(tag, "+", "_")
}

func archiveName(commit string) string {
	return workspaceAssetPrefix + commit + ".tar.gz"
}

func expectedAssetNames(commit string) []string {
	archive := archiveName(commit)
	return []string{
		archive,
		archive + ".commit",
		archive + ".manifest.sha256",
		archive + ".sha256",
		archive + ".sigstore.json",
	}
}

func certificateIdentity(tag string) string {
	return producerRepository + "/" + producerWorkflow + "@refs/tags/" + tag
}

func resolveProducerTag(ctx context.Context, client *http.Client, apiURL, token, tag, expectedCommit string) error {
	if err := validatePairInputs(tag, expectedCommit); err != nil {
		return err
	}
	if client == nil || token == "" {
		return errors.New("GitHub client and token are required")
	}
	base, err := url.Parse(apiURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("GitHub API URL is invalid")
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/repos/SnowGlow-aww/SekaiText-Moe/git/ref/tags/" + url.PathEscape(tag)
	var reference gitReference
	if err := getGitHubJSON(ctx, client, endpoint, token, &reference); err != nil {
		return fmt.Errorf("resolve official producer tag ref: %w", err)
	}
	if reference.Ref != "refs/tags/"+tag {
		return errors.New("GitHub tag response does not identify the exact requested ref")
	}
	object := reference.Object
	seen := make(map[string]struct{})
	for depth := 0; depth < 16; depth++ {
		if !canonicalCommit.MatchString(object.SHA) {
			return errors.New("GitHub tag object has a noncanonical SHA")
		}
		switch object.Type {
		case "commit":
			if object.SHA != expectedCommit {
				return errors.New("official producer tag does not resolve to moe_commit")
			}
			return nil
		case "tag":
			if _, duplicate := seen[object.SHA]; duplicate {
				return errors.New("annotated producer tag chain contains a cycle")
			}
			seen[object.SHA] = struct{}{}
			var annotated gitTag
			endpoint = strings.TrimRight(base.String(), "/") + "/repos/SnowGlow-aww/SekaiText-Moe/git/tags/" + object.SHA
			if err := getGitHubJSON(ctx, client, endpoint, token, &annotated); err != nil {
				return fmt.Errorf("peel annotated producer tag: %w", err)
			}
			object = annotated.Object
		default:
			return fmt.Errorf("producer tag points to unsupported Git object type %q", object.Type)
		}
	}
	return errors.New("annotated producer tag chain exceeds the maximum peel depth")
}

func getGitHubJSON(ctx context.Context, client *http.Client, endpoint, token string, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubJSONBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > maxGitHubJSONBytes {
		return errors.New("GitHub API response exceeds the size limit")
	}
	if err := json.Unmarshal(contents, output); err != nil {
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}

func selectAssets(releaseJSON, output, tag, commit string) error {
	if err := validatePairInputs(tag, commit); err != nil {
		return err
	}
	if releaseJSON == "" || output == "" {
		return errors.New("release-json and output are required")
	}
	contents, err := readSmallRegularFile(releaseJSON, 16<<20)
	if err != nil {
		return fmt.Errorf("read release metadata: %w", err)
	}
	var release githubRelease
	if err := json.Unmarshal(contents, &release); err != nil {
		return fmt.Errorf("decode release metadata: %w", err)
	}
	if release.TagName != tag || release.Draft || !release.Immutable {
		return errors.New("release metadata does not identify the requested immutable published tag")
	}
	expected := expectedAssetNames(commit)
	expectedSet := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		expectedSet[name] = struct{}{}
	}
	selected := make(map[string]releaseAsset, len(expected))
	selectedIDs := make(map[int64]string, len(expected))
	for _, asset := range release.Assets {
		if !strings.HasPrefix(asset.Name, workspaceAssetPrefix) {
			continue
		}
		if _, ok := expectedSet[asset.Name]; !ok {
			return errors.New("release contains an unexpected or mixed-identity workspace asset")
		}
		if _, duplicate := selected[asset.Name]; duplicate {
			return fmt.Errorf("release contains duplicate workspace asset %q", asset.Name)
		}
		if asset.ID <= 0 || asset.State != "uploaded" || asset.Size <= 0 {
			return fmt.Errorf("workspace asset %q is not a complete uploaded asset", asset.Name)
		}
		if err := validateAssetMetadataSize(asset.Name, archiveName(commit), asset.Size); err != nil {
			return err
		}
		if other, duplicate := selectedIDs[asset.ID]; duplicate {
			return fmt.Errorf("workspace assets %q and %q share an asset ID", other, asset.Name)
		}
		selected[asset.Name] = asset
		selectedIDs[asset.ID] = asset.Name
	}
	if len(selected) != len(expected) {
		return errors.New("release does not contain exactly the five required workspace assets")
	}
	var plan strings.Builder
	for _, name := range expected {
		asset, ok := selected[name]
		if !ok {
			return fmt.Errorf("release is missing workspace asset %q", name)
		}
		fmt.Fprintf(&plan, "%d\t%s\t%d\n", asset.ID, asset.Name, asset.Size)
	}
	return writeNewFile(output, []byte(plan.String()), 0o600)
}

func validateAssetMetadataSize(name, archive string, size int64) error {
	switch name {
	case archive:
		if size > 0 && size <= maxArchiveBytes {
			return nil
		}
	case archive + ".sigstore.json":
		if size > 0 && size <= maxBundleBytes {
			return nil
		}
	case archive + ".commit":
		if size == 41 {
			return nil
		}
	case archive + ".manifest.sha256":
		if size == 65 {
			return nil
		}
	case archive + ".sha256":
		if size == int64(64+2+len(archive)+1) {
			return nil
		}
	}
	return fmt.Errorf("workspace asset %q has an invalid release metadata size", name)
}

func validateDownloadedAssets(downloadPlan, assetsDirectory, commit string) error {
	if !canonicalCommit.MatchString(commit) {
		return errors.New("moe_commit must be exactly 40 lowercase hexadecimal characters")
	}
	if downloadPlan == "" || assetsDirectory == "" {
		return errors.New("download plan and assets directory are required")
	}
	contents, err := readSmallRegularFile(downloadPlan, 64<<10)
	if err != nil {
		return fmt.Errorf("read download plan: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	expected := expectedAssetNames(commit)
	if len(lines) != len(expected) {
		return errors.New("download plan does not contain exactly five assets")
	}
	expectedSizes := make(map[string]int64, len(expected))
	seenIDs := make(map[int64]struct{}, len(expected))
	for index, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[1] != expected[index] {
			return errors.New("download plan has an invalid asset row or order")
		}
		id, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || id <= 0 {
			return errors.New("download plan has an invalid asset ID")
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return errors.New("download plan contains a duplicate asset ID")
		}
		seenIDs[id] = struct{}{}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || validateAssetMetadataSize(fields[1], archiveName(commit), size) != nil {
			return errors.New("download plan has an invalid asset size")
		}
		expectedSizes[fields[1]] = size
	}
	if err := requireExactFiles(assetsDirectory, expected); err != nil {
		return err
	}
	for _, name := range expected {
		info, err := os.Lstat(filepath.Join(assetsDirectory, name))
		if err != nil {
			return fmt.Errorf("inspect downloaded asset %q: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Size() != expectedSizes[name] {
			return fmt.Errorf("downloaded asset %q byte count does not match release metadata", name)
		}
	}
	return nil
}

func consumeRelease(options consumeOptions) (consumeResult, error) {
	var result consumeResult
	if err := validatePairInputs(options.tag, options.commit); err != nil {
		return result, err
	}
	if options.assetsDirectory == "" || options.workspace == "" || options.cosign == "" {
		return result, errors.New("assets directory, workspace directory, and cosign executable are required")
	}
	assets, err := filepath.Abs(options.assetsDirectory)
	if err != nil {
		return result, fmt.Errorf("resolve assets directory: %w", err)
	}
	workspace, err := filepath.Abs(options.workspace)
	if err != nil {
		return result, fmt.Errorf("resolve workspace directory: %w", err)
	}
	archive := archiveName(options.commit)
	if err := requireExactFiles(assets, expectedAssetNames(options.commit)); err != nil {
		return result, err
	}
	archivePath := filepath.Join(assets, archive)
	archiveDigest, err := digestRegularFile(archivePath, maxArchiveBytes)
	if err != nil {
		return result, err
	}
	archiveSidecar, err := readSmallRegularFile(archivePath+".sha256", 256)
	if err != nil {
		return result, err
	}
	if string(archiveSidecar) != archiveDigest+"  "+archive+"\n" {
		return result, errors.New("workspace archive SHA-256 sidecar does not match the archive")
	}
	commitSidecar, err := readSmallRegularFile(archivePath+".commit", 128)
	if err != nil {
		return result, err
	}
	if string(commitSidecar) != options.commit+"\n" {
		return result, errors.New("workspace commit sidecar does not match moe_commit")
	}
	manifestSidecar, err := readSmallRegularFile(archivePath+".manifest.sha256", 128)
	if err != nil {
		return result, err
	}
	manifestDigest := strings.TrimSuffix(string(manifestSidecar), "\n")
	if string(manifestSidecar) != manifestDigest+"\n" || !canonicalDigest.MatchString(manifestDigest) {
		return result, errors.New("workspace manifest SHA-256 sidecar is not canonical")
	}
	if _, err := digestRegularFile(archivePath+".sigstore.json", maxBundleBytes); err != nil {
		return result, fmt.Errorf("inspect Sigstore bundle: %w", err)
	}
	if err := verifySigstore(options.cosign, archivePath, options.tag, options.commit); err != nil {
		return result, err
	}
	if err := extractArchive(archivePath, workspace); err != nil {
		return result, err
	}
	verified := false
	defer func() {
		if !verified {
			_ = os.RemoveAll(workspace)
		}
	}()
	manifest, err := workspaceverify.Verify(workspaceverify.Config{
		Root:                     workspace,
		ManifestSHA256:           manifestDigest,
		Production:               true,
		RootConfigured:           true,
		ManifestSHA256Configured: true,
	})
	if err != nil {
		return result, fmt.Errorf("NEXT workspace verification: %w", err)
	}
	if manifest == nil || manifest.Producer.Repository != producerRepository || manifest.Producer.SourceRevision != options.commit || manifest.Producer.SourceDirty || !manifest.Producer.SourceProduction {
		return result, errors.New("workspace manifest does not identify the requested clean official production commit")
	}
	result = consumeResult{
		archiveDigest:  archiveDigest,
		manifestDigest: manifestDigest,
		workspace:      workspace,
		ociTag:         ociTag(options.tag),
	}
	if options.githubOutput != "" {
		output := fmt.Sprintf("archive_digest=%s\nmanifest_digest=%s\nworkspace_dir=%s\nmoe_oci_tag=%s\n", result.archiveDigest, result.manifestDigest, result.workspace, result.ociTag)
		file, err := os.OpenFile(options.githubOutput, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return consumeResult{}, fmt.Errorf("open GitHub output: %w", err)
		}
		_, writeErr := io.WriteString(file, output)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return consumeResult{}, fmt.Errorf("write GitHub output: %w", err)
		}
	}
	verified = true
	return result, nil
}

func requireExactFiles(directory string, expected []string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read workspace assets: %w", err)
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
		info, err := os.Lstat(filepath.Join(directory, entry.Name()))
		if err != nil {
			return fmt.Errorf("inspect workspace asset %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace asset %q is not a regular file", entry.Name())
		}
	}
	sort.Strings(actual)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	if strings.Join(actual, "\x00") != strings.Join(want, "\x00") {
		return fmt.Errorf("workspace asset filenames do not match the exact five-file set: %s", strings.Join(actual, ", "))
	}
	return nil
}

func readSmallRegularFile(name string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", filepath.Base(name), err)
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("%s is not a bounded regular file", filepath.Base(name))
	}
	contents, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(name), err)
	}
	return contents, nil
}

func digestRegularFile(name string, maximum int64) (string, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", filepath.Base(name), err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return "", fmt.Errorf("%s is not a non-empty bounded regular file", filepath.Base(name))
	}
	file, err := os.Open(name)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filepath.Base(name), err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", fmt.Errorf("hash %s: %w", filepath.Base(name), err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifySigstore(cosign, archivePath, tag, expectedWorkflowSHA string) error {
	arguments := []string{
		"verify-blob",
		"--bundle", archivePath + ".sigstore.json",
		"--certificate-identity", certificateIdentity(tag),
		"--certificate-oidc-issuer", certificateIssuer,
		"--certificate-github-workflow-sha", expectedWorkflowSHA,
		archivePath,
	}
	command := exec.Command(cosign, arguments...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("verify mandatory workspace Sigstore bundle: %w", err)
	}
	return nil
}

func extractArchive(archivePath, destination string) (resultErr error) {
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("workspace extraction destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect workspace extraction destination: %w", err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("create workspace extraction destination: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(destination)
		}
	}()

	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open workspace archive: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	buffered := bufio.NewReader(file)
	gzipReader, err := gzip.NewReader(buffered)
	if err != nil {
		return fmt.Errorf("open workspace gzip stream: %w", err)
	}
	gzipReader.Multistream(false)
	tarReader := tar.NewReader(gzipReader)
	seenEntries := make(map[string]struct{})
	casePaths := make(map[string]string)
	rootSeen := false
	manifestSeen := false
	files := 0
	entries := 0
	var total int64

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("inspect workspace tar: %w", err)
		}
		entries++
		if entries > workspaceverify.MaxFiles*2+2 {
			return errors.New("workspace archive contains too many entries")
		}
		cleaned, relative, err := validateArchivePath(header.Name, header.Typeflag == tar.TypeDir)
		if err != nil {
			return err
		}
		if _, duplicate := seenEntries[cleaned]; duplicate {
			return fmt.Errorf("workspace archive contains duplicate path %q", cleaned)
		}
		seenEntries[cleaned] = struct{}{}
		if err := recordCasePaths(casePaths, cleaned); err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return fmt.Errorf("workspace archive directory %q has data", cleaned)
			}
			if relative == "" {
				rootSeen = true
				continue
			}
			if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(relative)), 0o700); err != nil {
				return fmt.Errorf("create workspace directory %q: %w", relative, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if relative == "" {
				return errors.New("workspace archive top-level path must be a directory")
			}
			files++
			if files > workspaceverify.MaxFiles+1 {
				return errors.New("workspace archive contains too many files")
			}
			maximum := int64(workspaceverify.MaxFileBytes)
			if relative == workspaceverify.ManifestFilename {
				maximum = workspaceverify.MaxManifestBytes
				manifestSeen = true
			}
			if header.Size < 0 || header.Size > maximum || total > int64(workspaceverify.MaxTotalBytes+workspaceverify.MaxManifestBytes)-header.Size {
				return fmt.Errorf("workspace archive entry %q exceeds resource bounds", cleaned)
			}
			total += header.Size
			target := filepath.Join(destination, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("create workspace file parent %q: %w", relative, err)
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return fmt.Errorf("create workspace file %q: %w", relative, err)
			}
			written, copyErr := io.Copy(output, io.LimitReader(tarReader, maximum+1))
			closeErr := output.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return fmt.Errorf("extract workspace file %q: %w", relative, err)
			}
			if written != header.Size {
				return fmt.Errorf("workspace archive entry %q ended early", cleaned)
			}
		default:
			return fmt.Errorf("workspace archive contains link, device, or nonregular entry %q", cleaned)
		}
	}
	if !rootSeen || !manifestSeen {
		return errors.New("workspace archive is missing its exact top-level directory or manifest")
	}
	if err := requireZeroTarPadding(gzipReader); err != nil {
		return err
	}
	if err := gzipReader.Close(); err != nil {
		return fmt.Errorf("close workspace gzip stream: %w", err)
	}
	if _, err := buffered.Peek(1); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workspace archive contains a trailing gzip member or bytes")
		}
		return fmt.Errorf("inspect trailing workspace archive bytes: %w", err)
	}
	complete = true
	return nil
}

func validateArchivePath(name string, directory bool) (string, string, error) {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) {
		return "", "", fmt.Errorf("workspace archive path %q is unsafe", name)
	}
	trimmed := name
	if directory {
		trimmed = strings.TrimSuffix(trimmed, "/")
	} else if strings.HasSuffix(trimmed, "/") {
		return "", "", fmt.Errorf("workspace archive regular path %q ends with a slash", name)
	}
	if trimmed == "" || path.Clean(trimmed) != trimmed {
		return "", "", fmt.Errorf("workspace archive path %q is noncanonical or traverses", name)
	}
	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", fmt.Errorf("workspace archive path %q is noncanonical or traverses", name)
		}
	}
	if parts[0] != workspaceArchiveRoot {
		return "", "", fmt.Errorf("workspace archive path %q has an unexpected top-level path", name)
	}
	if trimmed == workspaceArchiveRoot {
		return trimmed, "", nil
	}
	return trimmed, strings.TrimPrefix(trimmed, workspaceArchiveRoot+"/"), nil
}

func recordCasePaths(casePaths map[string]string, name string) error {
	parts := strings.Split(name, "/")
	for index := range parts {
		prefix := strings.Join(parts[:index+1], "/")
		folded := strings.ToLower(prefix)
		if existing, ok := casePaths[folded]; ok && existing != prefix {
			return fmt.Errorf("workspace archive paths %q and %q collide by case", existing, prefix)
		}
		casePaths[folded] = prefix
	}
	return nil
}

func requireZeroTarPadding(reader io.Reader) error {
	buffer := make([]byte, 32<<10)
	remaining := int64(maxTarPaddingBytes)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			remaining -= int64(read)
			if remaining < 0 {
				return errors.New("workspace archive has excessive tar padding")
			}
			for _, value := range buffer[:read] {
				if value != 0 {
					return errors.New("workspace archive contains data after the tar end marker")
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("validate workspace tar padding: %w", err)
		}
	}
}

type pairedPredicate struct {
	SchemaVersion int `json:"schemaVersion"`
	NEXT          struct {
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
	} `json:"next"`
	Moe struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
		Revision   string `json:"revision"`
	} `json:"moe"`
	Workspace struct {
		ArchiveDigest  string `json:"archiveDigest"`
		ManifestDigest string `json:"manifestDigest"`
	} `json:"workspace"`
	Image struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"image"`
}

func writePredicate(output, nextRepository, nextCommit, moeTag, moeCommit, archiveDigest, manifestDigest, image, imageDigest string) error {
	if output == "" {
		return errors.New("predicate output is required")
	}
	if err := validatePairInputs(moeTag, moeCommit); err != nil {
		return err
	}
	if !githubSourceURL.MatchString(nextRepository) || !canonicalCommit.MatchString(nextCommit) {
		return errors.New("NEXT repository or revision is not canonical")
	}
	if !canonicalDigest.MatchString(archiveDigest) || !canonicalDigest.MatchString(manifestDigest) {
		return errors.New("workspace digests must be 64 lowercase hexadecimal characters")
	}
	if !ghcrImageName.MatchString(image) || !strings.HasPrefix(imageDigest, "sha256:") || !canonicalDigest.MatchString(strings.TrimPrefix(imageDigest, "sha256:")) {
		return errors.New("final image name or digest is not canonical")
	}
	predicate := pairedPredicate{SchemaVersion: 1}
	predicate.NEXT.Repository = nextRepository
	predicate.NEXT.Revision = nextCommit
	predicate.Moe.Repository = producerRepository
	predicate.Moe.Tag = moeTag
	predicate.Moe.Revision = moeCommit
	predicate.Workspace.ArchiveDigest = "sha256:" + archiveDigest
	predicate.Workspace.ManifestDigest = "sha256:" + manifestDigest
	predicate.Image.Name = image
	predicate.Image.Digest = imageDigest
	contents, err := json.MarshalIndent(predicate, "", "  ")
	if err != nil {
		return fmt.Errorf("encode paired predicate: %w", err)
	}
	return writeNewFile(output, append(contents, '\n'), 0o600)
}

type dsseEnvelope struct {
	PayloadType string `json:"payloadType"`
	Payload     string `json:"payload"`
}

type intotoStatement struct {
	Type          string          `json:"_type"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
	Subject       []struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
}

func validateAttestationFiles(verificationJSON, predicatePath, predicateType, image, imageDigest string) error {
	if verificationJSON == "" || predicatePath == "" {
		return errors.New("verification JSON and expected predicate are required")
	}
	if predicateType == "" || !ghcrImageName.MatchString(image) || !strings.HasPrefix(imageDigest, "sha256:") || !canonicalDigest.MatchString(strings.TrimPrefix(imageDigest, "sha256:")) {
		return errors.New("expected predicate type, image, or digest is invalid")
	}
	verification, err := readSmallRegularFile(verificationJSON, maxBundleBytes)
	if err != nil {
		return err
	}
	expectedPredicate, err := readSmallRegularFile(predicatePath, maxGitHubJSONBytes)
	if err != nil {
		return err
	}
	var expected any
	if err := json.Unmarshal(expectedPredicate, &expected); err != nil {
		return fmt.Errorf("decode expected predicate: %w", err)
	}
	values, err := decodeJSONValues(verification)
	if err != nil {
		return fmt.Errorf("decode Cosign attestation verification output: %w", err)
	}
	if len(values) == 0 {
		return errors.New("Cosign attestation verification output is empty")
	}
	expectedDigest := strings.TrimPrefix(imageDigest, "sha256:")
	for _, value := range values {
		var envelope dsseEnvelope
		if err := json.Unmarshal(value, &envelope); err != nil || envelope.PayloadType != intotoPayloadType || envelope.Payload == "" {
			return errors.New("verified attestation is not an in-toto DSSE envelope")
		}
		payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
		if err != nil {
			return errors.New("verified attestation payload is not canonical base64")
		}
		var statement intotoStatement
		if err := json.Unmarshal(payload, &statement); err != nil {
			return fmt.Errorf("decode verified in-toto statement: %w", err)
		}
		if statement.Type != intotoStatementType || statement.PredicateType != predicateType || len(statement.Subject) != 1 || statement.Subject[0].Name != image || len(statement.Subject[0].Digest) != 1 || statement.Subject[0].Digest["sha256"] != expectedDigest {
			return errors.New("verified attestation statement does not bind the exact image, digest, and predicate type")
		}
		var actual any
		if err := json.Unmarshal(statement.Predicate, &actual); err != nil {
			return fmt.Errorf("decode verified predicate contents: %w", err)
		}
		if !reflect.DeepEqual(actual, expected) {
			return errors.New("verified attestation predicate contents do not match the expected paired predicate")
		}
	}
	return nil
}

func decodeJSONValues(contents []byte) ([]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	var values []json.RawMessage
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if len(raw) > 0 && raw[0] == '[' {
			var array []json.RawMessage
			if err := json.Unmarshal(raw, &array); err != nil {
				return nil, err
			}
			values = append(values, array...)
		} else {
			values = append(values, raw)
		}
	}
	return values, nil
}

func validateFinalTagState(statePath, missingOutput, expectedDigest string, expectedTags []string, requirePresent bool) error {
	if statePath == "" || !strings.HasPrefix(expectedDigest, "sha256:") || !canonicalDigest.MatchString(strings.TrimPrefix(expectedDigest, "sha256:")) || len(expectedTags) != 2 {
		return errors.New("tag state, expected digest, and two final tags are required")
	}
	expected := make(map[string]struct{}, len(expectedTags))
	for _, tag := range expectedTags {
		if !ghcrTaggedImage.MatchString(tag) {
			return errors.New("final image tag is not canonical")
		}
		if _, duplicate := expected[tag]; duplicate {
			return errors.New("final image tags must be distinct")
		}
		expected[tag] = struct{}{}
	}
	contents, err := readSmallRegularFile(statePath, 64<<10)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines) != len(expectedTags) {
		return errors.New("tag state does not contain exactly the two final tags")
	}
	seen := make(map[string]struct{}, len(expectedTags))
	var missing []string
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			return errors.New("tag state row is malformed")
		}
		if _, ok := expected[fields[0]]; !ok {
			return errors.New("tag state contains an unexpected final tag")
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			return errors.New("tag state contains a duplicate final tag")
		}
		seen[fields[0]] = struct{}{}
		switch fields[1] {
		case "-":
			missing = append(missing, fields[0])
		case expectedDigest:
		default:
			return fmt.Errorf("final tag %q already points to a different digest", fields[0])
		}
	}
	if requirePresent && len(missing) > 0 {
		return errors.New("final tag promotion did not publish both exact digests")
	}
	if !requirePresent {
		if missingOutput == "" {
			return errors.New("missing tag output is required")
		}
		sort.Strings(missing)
		contents := []byte(strings.Join(missing, "\n"))
		if len(contents) > 0 {
			contents = append(contents, '\n')
		}
		return writeNewFile(missingOutput, contents, 0o600)
	}
	return nil
}

func writeNewFile(name string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(name), err)
	}
	_, writeErr := file.Write(contents)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(name), err)
	}
	return nil
}
