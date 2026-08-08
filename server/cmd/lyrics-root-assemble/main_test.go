package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"moesekai/server/internal/lyricsacquisition"
	"moesekai/server/internal/lyricsevidencepack"
	"moesekai/server/internal/lyricsrootmanifest"
)

const commandCurrentCatalogCount = 704

type emptyExactSource struct{}

func (emptyExactSource) ReplayByAcquisitionID(
	context.Context,
	lyricsacquisition.AcquisitionID,
) (lyricsacquisition.Acquisition, error) {
	return lyricsacquisition.Acquisition{}, lyricsacquisition.ErrAcquisitionNotFound
}

func commandDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func commandMusicIDs() []int {
	musicIDs := make([]int, commandCurrentCatalogCount)
	for index := range musicIDs {
		musicIDs[index] = index + 1
	}
	return musicIDs
}

func commandRequest(kind lyricsrootmanifest.ScopeKind, songMusicIDs []int) lyricsrootmanifest.AssemblyRequest {
	catalogMusicIDs := commandMusicIDs()
	musicIDsSHA256, err := lyricsrootmanifest.OrderedMusicIDsSHA256(catalogMusicIDs)
	if err != nil {
		panic(err)
	}
	rootID := "command-root-001"
	if kind != lyricsrootmanifest.ScopeFinal {
		rootID = "command-root-002"
	}
	request := lyricsrootmanifest.AssemblyRequest{
		RootID: rootID,
		Scope:  lyricsrootmanifest.ScopeBinding{Kind: kind, ScopeID: "command-catalog-scope"},
		Catalog: lyricsrootmanifest.CatalogBinding{
			SchemaVersion: 18, RuntimeSchemaVersion: 23, RecordCount: len(catalogMusicIDs),
			IdentityPolicyVersion: "lyrics-catalog-identity-v1",
			SourceSHA256:          strings.Repeat("a", 64), IdentitySHA256: strings.Repeat("b", 64),
			MusicIDsSHA256: musicIDsSHA256,
		},
		Plan:  lyricsrootmanifest.PlanBinding{PlanID: "command-plan-001", SHA256: strings.Repeat("c", 64)},
		Songs: make([]lyricsrootmanifest.SongResultRef, len(songMusicIDs)),
	}
	if kind != lyricsrootmanifest.ScopeFinal {
		request.Scope.SupersedesRootID = "command-root-001"
		request.Scope.SupersedesRootSHA256 = strings.Repeat("d", 64)
	}
	for index, musicID := range songMusicIDs {
		request.Songs[index] = lyricsrootmanifest.SongResultRef{
			MusicID: musicID, State: lyricsrootmanifest.CoverageMissing,
			ResultSHA256:     commandDigest(fmt.Sprintf("command-result-%d", musicID)),
			ProviderOutcomes: []lyricsrootmanifest.ProviderOutcomeRef{},
			SelectedEvidence: []lyricsrootmanifest.SelectedEvidenceRef{},
		}
	}
	return request
}

func buildEmptyCommandPack(t *testing.T, root string) string {
	t.Helper()
	packDir := filepath.Join(root, "pack")
	if _, err := lyricsevidencepack.Build(context.Background(), packDir, []lyricsevidencepack.EvidenceRef{}, emptyExactSource{}); err != nil {
		t.Fatal(err)
	}
	return packDir
}

func writePrivateJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func commandCanonicalTestRoot(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil || absolute != resolved {
		t.Fatalf("canonical command test directory path=%q err=%v", resolved, err)
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		t.Fatal(err)
	}
	return resolved
}

func commandParent(t *testing.T, packDir string) lyricsrootmanifest.Manifest {
	t.Helper()
	resolver, err := lyricsevidencepack.OpenResolver(packDir)
	if err != nil {
		t.Fatal(err)
	}
	musicIDs := commandMusicIDs()
	parent, err := lyricsrootmanifest.Assemble(commandRequest(lyricsrootmanifest.ScopeFinal, musicIDs), resolver)
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func writeParentRoot(t *testing.T, path string, parent lyricsrootmanifest.Manifest, mode os.FileMode) {
	t.Helper()
	body, err := lyricsrootmanifest.MarshalCanonical(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func TestRunAssemblesPrivateFinalRootOffline(t *testing.T) {
	root := commandCanonicalTestRoot(t)
	packDir := buildEmptyCommandPack(t, root)
	requestPath := filepath.Join(root, "request.json")
	writePrivateJSON(t, requestPath, commandRequest(lyricsrootmanifest.ScopeFinal, commandMusicIDs()))
	outputPath := filepath.Join(root, "root.json")
	args := []string{"-request", requestPath, "-evidence-pack-dir", packDir, "-output", outputPath}
	var output bytes.Buffer
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "for 704 songs with 0 unique evidence items") {
		t.Fatalf("command output=%q", output.String())
	}
	body, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := lyricsrootmanifest.DecodeCanonical(body)
	if err != nil || manifest.Coverage.Total != commandCurrentCatalogCount {
		t.Fatalf("root manifest=%+v err=%v", manifest.Coverage, err)
	}
	info, err := os.Lstat(outputPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("root output info=%v err=%v", info, err)
	}
	if err := run(args, &bytes.Buffer{}); err == nil {
		t.Fatal("command overwrote an existing root manifest")
	}
}

func TestRunConcurrentIdenticalPublicationRemainsCreateExclusive(t *testing.T) {
	root := commandCanonicalTestRoot(t)
	packDir := buildEmptyCommandPack(t, root)
	requestPath := filepath.Join(root, "request.json")
	writePrivateJSON(t, requestPath, commandRequest(lyricsrootmanifest.ScopeFinal, commandMusicIDs()))
	outputPath := filepath.Join(root, "root.json")
	args := []string{"-request", requestPath, "-evidence-pack-dir", packDir, "-output", outputPath}

	const publishers = 8
	errorsByPublisher := make([]error, publishers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(publishers)
	for index := range errorsByPublisher {
		go func(index int) {
			defer wait.Done()
			<-start
			errorsByPublisher[index] = run(args, &bytes.Buffer{})
		}(index)
	}
	close(start)
	wait.Wait()

	successes := 0
	alreadyPublished := 0
	for _, err := range errorsByPublisher {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, lyricsrootmanifest.ErrAlreadyPublished):
			alreadyPublished++
		default:
			t.Fatalf("concurrent command publication error=%v", err)
		}
	}
	if successes != 1 || alreadyPublished != publishers-1 {
		t.Fatalf("concurrent command publication successes=%d alreadyPublished=%d", successes, alreadyPublished)
	}
}

func TestRunConcurrentConflictingPublicationHasOneWinner(t *testing.T) {
	root := commandCanonicalTestRoot(t)
	packDir := buildEmptyCommandPack(t, root)
	firstRequest := commandRequest(lyricsrootmanifest.ScopeFinal, commandMusicIDs())
	firstRequest.RootID = "command-root-first"
	secondRequest := commandRequest(lyricsrootmanifest.ScopeFinal, commandMusicIDs())
	secondRequest.RootID = "command-root-second"
	firstPath := filepath.Join(root, "first-request.json")
	secondPath := filepath.Join(root, "second-request.json")
	writePrivateJSON(t, firstPath, firstRequest)
	writePrivateJSON(t, secondPath, secondRequest)
	outputPath := filepath.Join(root, "root.json")
	arguments := [][]string{
		{"-request", firstPath, "-evidence-pack-dir", packDir, "-output", outputPath},
		{"-request", secondPath, "-evidence-pack-dir", packDir, "-output", outputPath},
	}

	errorsByPublisher := make([]error, len(arguments))
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(len(arguments))
	for index := range arguments {
		go func(index int) {
			defer wait.Done()
			<-start
			errorsByPublisher[index] = run(arguments[index], &bytes.Buffer{})
		}(index)
	}
	close(start)
	wait.Wait()

	successes := 0
	alreadyPublished := 0
	for _, err := range errorsByPublisher {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, lyricsrootmanifest.ErrAlreadyPublished):
			alreadyPublished++
		default:
			t.Fatalf("conflicting command publication error=%v", err)
		}
	}
	if successes != 1 || alreadyPublished != 1 {
		t.Fatalf("conflicting command publication successes=%d alreadyPublished=%d", successes, alreadyPublished)
	}
	body, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := lyricsrootmanifest.DecodeCanonical(body)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RootID != firstRequest.RootID && manifest.RootID != secondRequest.RootID {
		t.Fatalf("conflicting publication emitted unexpected root ID %q", manifest.RootID)
	}
}

func TestRunValidPartialAndRetryParentBinding(t *testing.T) {
	root := commandCanonicalTestRoot(t)
	packDir := buildEmptyCommandPack(t, root)
	parent := commandParent(t, packDir)
	parentPath := filepath.Join(root, "parent.json")
	writeParentRoot(t, parentPath, parent, 0o600)

	for _, kind := range []lyricsrootmanifest.ScopeKind{lyricsrootmanifest.ScopePartial, lyricsrootmanifest.ScopeRetry} {
		t.Run(string(kind), func(t *testing.T) {
			request := commandRequest(kind, []int{1, 17, commandCurrentCatalogCount})
			request.Scope.SupersedesRootID = parent.RootID
			request.Scope.SupersedesRootSHA256 = parent.RootSHA256
			requestPath := filepath.Join(root, string(kind)+"-request.json")
			writePrivateJSON(t, requestPath, request)
			outputPath := filepath.Join(root, string(kind)+"-root.json")
			args := []string{
				"-request", requestPath, "-parent-root", parentPath,
				"-evidence-pack-dir", packDir, "-output", outputPath,
			}
			if err := run(args, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := lyricsrootmanifest.DecodeCanonicalAgainstParent(body, parent)
			if err != nil {
				t.Fatalf("published %s parent binding: %v", kind, err)
			}
			if manifest.Scope.Kind != kind {
				t.Fatalf("published root kind=%q want %q", manifest.Scope.Kind, kind)
			}
		})
	}
}

func TestRunRejectsFinalWithParentAndRequiresPrivateParentForRetry(t *testing.T) {
	root := commandCanonicalTestRoot(t)
	packDir := buildEmptyCommandPack(t, root)
	parent := commandParent(t, packDir)
	parentPath := filepath.Join(root, "parent.json")
	writeParentRoot(t, parentPath, parent, 0o600)

	finalRequestPath := filepath.Join(root, "final-request.json")
	writePrivateJSON(t, finalRequestPath, commandRequest(lyricsrootmanifest.ScopeFinal, commandMusicIDs()))
	finalArgs := []string{
		"-request", finalRequestPath, "-parent-root", parentPath,
		"-evidence-pack-dir", packDir, "-output", filepath.Join(root, "final-root.json"),
	}
	if err := run(finalArgs, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "reject -parent-root") {
		t.Fatalf("final-with-parent error=%v", err)
	}

	retry := commandRequest(lyricsrootmanifest.ScopeRetry, []int{2, 3})
	retry.Scope.SupersedesRootID = parent.RootID
	retry.Scope.SupersedesRootSHA256 = parent.RootSHA256
	retryRequestPath := filepath.Join(root, "retry-request.json")
	writePrivateJSON(t, retryRequestPath, retry)
	retryArgs := []string{
		"-request", retryRequestPath, "-evidence-pack-dir", packDir,
		"-output", filepath.Join(root, "retry-root.json"),
	}
	if err := run(retryArgs, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires -parent-root") {
		t.Fatalf("retry-without-parent error=%v", err)
	}
	if err := os.Chmod(parentPath, 0o644); err != nil {
		t.Fatal(err)
	}
	retryArgs = append(retryArgs, "-parent-root", parentPath)
	if err := run(retryArgs, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "mode-0600") {
		t.Fatalf("non-private parent error=%v", err)
	}
}

func TestRunRejectsNonPrivateRequestMode(t *testing.T) {
	requestBody, err := json.Marshal(commandRequest(lyricsrootmanifest.ScopeFinal, commandMusicIDs()))
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(requestPath, requestBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(requestPath, 0o644); err != nil {
		t.Fatal(err)
	}
	err = run([]string{
		"-request", requestPath, "-evidence-pack-dir", t.TempDir(),
		"-output", filepath.Join(t.TempDir(), "root.json"),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "mode-0600") {
		t.Fatalf("request mode error=%v", err)
	}
}
