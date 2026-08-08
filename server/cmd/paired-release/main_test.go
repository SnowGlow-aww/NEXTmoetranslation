package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"moesekai/server/internal/workspaceverify"
)

const (
	testTag    = "v5.9.6"
	testCommit = "2e223baeb8f55ea42e8f0d4702ef07f4566ef670"
)

type tarEntry struct {
	name     string
	typeflag byte
	data     []byte
}

func TestValidatePairInputsStrictly(t *testing.T) {
	if err := validatePairInputs(testTag, testCommit); err != nil {
		t.Fatal(err)
	}
	validPrerelease := "v1.2.3-rc.1+build.7"
	if err := validatePairInputs(validPrerelease, testCommit); err != nil {
		t.Fatalf("valid prerelease rejected: %v", err)
	}
	for _, test := range []struct {
		tag    string
		commit string
	}{
		{"5.9.6", testCommit},
		{"v05.9.6", testCommit},
		{"v1.2.3-01", testCommit},
		{"v1.2", testCommit},
		{testTag, strings.ToUpper(testCommit)},
		{testTag, testCommit[:39]},
	} {
		if err := validatePairInputs(test.tag, test.commit); err == nil {
			t.Fatalf("accepted malformed tag=%q commit=%q", test.tag, test.commit)
		}
	}
	if got := ociTag(validPrerelease); got != "v1.2.3-rc.1_build.7" {
		t.Fatalf("OCI tag = %q", got)
	}
}

func TestSelectAssetsRequiresExactWorkspaceSet(t *testing.T) {
	release := validReleaseMetadata()
	release.Assets = append(release.Assets, releaseAsset{ID: 999, Name: "SekaiText-Moe.dmg", State: "uploaded", Size: 10})
	metadata := filepath.Join(t.TempDir(), "release.json")
	writeJSONFile(t, metadata, release)
	plan := filepath.Join(t.TempDir(), "plan.tsv")
	if err := selectAssets(metadata, plan, testTag, testCommit); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(readString(t, plan)), "\n")
	if len(lines) != 5 {
		t.Fatalf("download plan has %d lines: %q", len(lines), lines)
	}
	for index, name := range expectedAssetNames(testCommit) {
		expected := fmt.Sprintf("%d\t%s\t%d", index+1, name, validAssetMetadataSize(name, testCommit))
		if lines[index] != expected {
			t.Fatalf("download plan line %d = %q", index, lines[index])
		}
	}
}

func TestSelectAssetsRejectsMissingExtraDuplicateAndMixedIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*githubRelease)
	}{
		{"missing", func(release *githubRelease) { release.Assets = release.Assets[:4] }},
		{"extra workspace", func(release *githubRelease) {
			release.Assets = append(release.Assets, releaseAsset{ID: 9, Name: workspaceAssetPrefix + strings.Repeat("f", 40) + ".tar.gz", State: "uploaded", Size: 1})
		}},
		{"duplicate", func(release *githubRelease) { release.Assets = append(release.Assets, release.Assets[0]) }},
		{"duplicate asset ID", func(release *githubRelease) { release.Assets[1].ID = release.Assets[0].ID }},
		{"mixed tag", func(release *githubRelease) { release.TagName = "v5.9.7" }},
		{"draft", func(release *githubRelease) { release.Draft = true }},
		{"mutable", func(release *githubRelease) { release.Immutable = false }},
		{"oversized archive", func(release *githubRelease) { release.Assets[0].Size = maxArchiveBytes + 1 }},
		{"oversized bundle", func(release *githubRelease) { release.Assets[4].Size = maxBundleBytes + 1 }},
		{"wrong commit sidecar size", func(release *githubRelease) { release.Assets[1].Size++ }},
		{"wrong manifest sidecar size", func(release *githubRelease) { release.Assets[2].Size++ }},
		{"wrong archive sidecar size", func(release *githubRelease) { release.Assets[3].Size++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			release := validReleaseMetadata()
			test.mutate(&release)
			metadata := filepath.Join(t.TempDir(), "release.json")
			writeJSONFile(t, metadata, release)
			if err := selectAssets(metadata, filepath.Join(t.TempDir(), "plan"), testTag, testCommit); err == nil {
				t.Fatal("malformed release metadata was accepted")
			}
		})
	}
}

func TestValidateDownloadedAssetsRequiresMetadataByteCounts(t *testing.T) {
	assets := createReleaseAssets(t, fixtureEntries(t))
	plan := filepath.Join(t.TempDir(), "plan.tsv")
	var contents strings.Builder
	for index, name := range expectedAssetNames(testCommit) {
		info, err := os.Stat(filepath.Join(assets, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&contents, "%d\t%s\t%d\n", index+1, name, info.Size())
	}
	mustWrite(t, plan, []byte(contents.String()))
	if err := validateDownloadedAssets(plan, assets, testCommit); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(assets, archiveName(testCommit))
	file, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateDownloadedAssets(plan, assets, testCommit); err == nil || !strings.Contains(err.Error(), "byte count") {
		t.Fatalf("downloaded size mismatch error = %v", err)
	}
}

func TestResolveProducerTagPeelsAnnotatedTagsAndRequiresCommit(t *testing.T) {
	annotatedA := strings.Repeat("a", 40)
	annotatedB := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer narrow-token" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/repos/SnowGlow-aww/SekaiText-Moe/git/ref/tags/" + testTag:
			fmt.Fprintf(response, `{"ref":"refs/tags/%s","object":{"type":"tag","sha":"%s"}}`, testTag, annotatedA)
		case "/repos/SnowGlow-aww/SekaiText-Moe/git/tags/" + annotatedA:
			fmt.Fprintf(response, `{"object":{"type":"tag","sha":"%s"}}`, annotatedB)
		case "/repos/SnowGlow-aww/SekaiText-Moe/git/tags/" + annotatedB:
			fmt.Fprintf(response, `{"object":{"type":"commit","sha":"%s"}}`, testCommit)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	if err := resolveProducerTag(context.Background(), server.Client(), server.URL, "narrow-token", testTag, testCommit); err != nil {
		t.Fatal(err)
	}
	if err := resolveProducerTag(context.Background(), server.Client(), server.URL, "narrow-token", testTag, strings.Repeat("f", 40)); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("wrong commit resolution error = %v", err)
	}
}

func TestResolveProducerTagAcceptsDirectCommitAndRejectsCycles(t *testing.T) {
	cycleSHA := strings.Repeat("c", 40)
	for _, test := range []struct {
		name      string
		object    gitObject
		tagObject gitObject
		wantError string
	}{
		{name: "direct commit", object: gitObject{Type: "commit", SHA: testCommit}},
		{name: "annotated cycle", object: gitObject{Type: "tag", SHA: cycleSHA}, tagObject: gitObject{Type: "tag", SHA: cycleSHA}, wantError: "cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				if strings.Contains(request.URL.Path, "/git/ref/tags/") {
					_ = json.NewEncoder(response).Encode(gitReference{Ref: "refs/tags/" + testTag, Object: test.object})
					return
				}
				_ = json.NewEncoder(response).Encode(gitTag{Object: test.tagObject})
			}))
			defer server.Close()
			err := resolveProducerTag(context.Background(), server.Client(), server.URL, "token", testTag, testCommit)
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("resolution error = %v", err)
			}
		})
	}
}

func TestConsumeReleaseVerifiesExactCosignIdentityAndWorkspace(t *testing.T) {
	assets := createReleaseAssets(t, fixtureEntries(t))
	cosign, log := fakeCosign(t, 0)
	output := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(output, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	result, err := consumeRelease(consumeOptions{
		assetsDirectory: assets,
		workspace:       workspace,
		tag:             testTag,
		commit:          testCommit,
		githubOutput:    output,
		cosign:          cosign,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.workspace != workspace || !canonicalDigest.MatchString(result.archiveDigest) || !canonicalDigest.MatchString(result.manifestDigest) {
		t.Fatalf("result = %+v", result)
	}
	archive := filepath.Join(assets, archiveName(testCommit))
	wantArguments := []string{
		"verify-blob",
		"--bundle", archive + ".sigstore.json",
		"--certificate-identity", "https://github.com/SnowGlow-aww/SekaiText-Moe/.github/workflows/release.yml@refs/tags/" + testTag,
		"--certificate-oidc-issuer", certificateIssuer,
		"--certificate-github-workflow-sha", testCommit,
		archive,
	}
	if got := strings.Split(strings.TrimSpace(readString(t, log)), "\n"); !reflect.DeepEqual(got, wantArguments) {
		t.Fatalf("cosign arguments = %#v, want %#v", got, wantArguments)
	}
	if !strings.Contains(readString(t, output), "workspace_dir="+workspace+"\n") {
		t.Fatalf("GitHub output = %q", readString(t, output))
	}
	manifest, err := workspaceverify.Verify(workspaceverify.Config{
		Root: workspace, ManifestSHA256: result.manifestDigest, Production: true,
	})
	if err != nil || manifest.Producer.SourceRevision != testCommit {
		t.Fatalf("extracted workspace manifest=%v err=%v", manifest, err)
	}
}

func TestConsumeReleaseRejectsMixedSidecarsDigestAndSignature(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		exit   int
	}{
		{"mixed commit", func(t *testing.T, assets string) {
			mustWrite(t, filepath.Join(assets, archiveName(testCommit)+".commit"), []byte(strings.Repeat("f", 40)+"\n"))
		}, 0},
		{"archive digest", func(t *testing.T, assets string) {
			mustWrite(t, filepath.Join(assets, archiveName(testCommit)+".sha256"), []byte(strings.Repeat("0", 64)+"  "+archiveName(testCommit)+"\n"))
		}, 0},
		{"manifest digest", func(t *testing.T, assets string) {
			mustWrite(t, filepath.Join(assets, archiveName(testCommit)+".manifest.sha256"), []byte("ABC\n"))
		}, 0},
		{"bad signature", func(*testing.T, string) {}, 1},
		{"extra local asset", func(t *testing.T, assets string) { mustWrite(t, filepath.Join(assets, "extra"), []byte("x")) }, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := createReleaseAssets(t, fixtureEntries(t))
			test.mutate(t, assets)
			cosign, _ := fakeCosign(t, test.exit)
			workspace := filepath.Join(t.TempDir(), "workspace")
			if _, err := consumeRelease(consumeOptions{assetsDirectory: assets, workspace: workspace, tag: testTag, commit: testCommit, cosign: cosign}); err == nil {
				t.Fatal("invalid release inputs were accepted")
			}
			if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
				t.Fatalf("failed preflight retained workspace: %v", err)
			}
		})
	}
}

func TestArchiveInspectionRejectsUnsafeEntriesBeforeWorkspaceUse(t *testing.T) {
	manifest := fixtureManifest(t)
	tests := []struct {
		name    string
		entries func() []tarEntry
	}{
		{"absolute", func() []tarEntry { return minimalEntries(manifest, tarEntry{name: "/tmp/escape", data: []byte("x")}) }},
		{"traversal", func() []tarEntry {
			return minimalEntries(manifest, tarEntry{name: "dist-web-workspace/../escape", data: []byte("x")})
		}},
		{"backslash", func() []tarEntry {
			return minimalEntries(manifest, tarEntry{name: "dist-web-workspace/assets\\escape", data: []byte("x")})
		}},
		{"unexpected top level", func() []tarEntry { return minimalEntries(manifest, tarEntry{name: "other/file", data: []byte("x")}) }},
		{"link", func() []tarEntry {
			return minimalEntries(manifest, tarEntry{name: "dist-web-workspace/link", typeflag: tar.TypeSymlink})
		}},
		{"device", func() []tarEntry {
			return minimalEntries(manifest, tarEntry{name: "dist-web-workspace/device", typeflag: tar.TypeChar})
		}},
		{"duplicate", func() []tarEntry {
			entries := fixtureEntries(t)
			return append(entries, tarEntry{name: "dist-web-workspace/index.html", data: []byte("duplicate")})
		}},
		{"case collision", func() []tarEntry {
			entries := fixtureEntries(t)
			return append(entries, tarEntry{name: "dist-web-workspace/Assets/app.js", data: []byte("collision")})
		}},
		{"bounds", func() []tarEntry {
			return []tarEntry{
				{name: workspaceArchiveRoot, typeflag: tar.TypeDir},
				{name: workspaceArchiveRoot + "/" + workspaceverify.ManifestFilename, data: make([]byte, workspaceverify.MaxManifestBytes+1)},
			}
		}},
		{"manifest extra", func() []tarEntry {
			entries := fixtureEntries(t)
			return append(entries, tarEntry{name: "dist-web-workspace/extra.txt", data: []byte("extra")})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := createReleaseAssets(t, test.entries())
			cosign, _ := fakeCosign(t, 0)
			workspace := filepath.Join(t.TempDir(), "workspace")
			if _, err := consumeRelease(consumeOptions{assetsDirectory: assets, workspace: workspace, tag: testTag, commit: testCommit, cosign: cosign}); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
			if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
				t.Fatalf("unsafe archive retained extraction directory: %v", err)
			}
		})
	}
}

func TestWritePredicateBindsBothSourcesAndFinalDigest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "predicate.json")
	archiveDigest := strings.Repeat("a", 64)
	manifestDigest := strings.Repeat("b", 64)
	imageDigest := "sha256:" + strings.Repeat("c", 64)
	if err := writePredicate(output, "https://github.com/StarMoe-org/NEXTmoetrabslation", strings.Repeat("d", 40), testTag, testCommit, archiveDigest, manifestDigest, "ghcr.io/starmoe-org/nextmoetrabslation", imageDigest); err != nil {
		t.Fatal(err)
	}
	var predicate pairedPredicate
	if err := json.Unmarshal([]byte(readString(t, output)), &predicate); err != nil {
		t.Fatal(err)
	}
	if predicate.NEXT.Revision != strings.Repeat("d", 40) || predicate.Moe.Revision != testCommit || predicate.Moe.Tag != testTag || predicate.Workspace.ArchiveDigest != "sha256:"+archiveDigest || predicate.Workspace.ManifestDigest != "sha256:"+manifestDigest || predicate.Image.Digest != imageDigest {
		t.Fatalf("predicate = %+v", predicate)
	}
}

func TestValidateAttestationRequiresExactPredicateContentsAndSubject(t *testing.T) {
	directory := t.TempDir()
	predicatePath := filepath.Join(directory, "predicate.json")
	archiveDigest := strings.Repeat("a", 64)
	manifestDigest := strings.Repeat("b", 64)
	imageDigest := "sha256:" + strings.Repeat("c", 64)
	image := "ghcr.io/starmoe-org/nextmoetrabslation"
	nextCommit := strings.Repeat("d", 40)
	if err := writePredicate(predicatePath, "https://github.com/StarMoe-org/NEXTmoetrabslation", nextCommit, testTag, testCommit, archiveDigest, manifestDigest, image, imageDigest); err != nil {
		t.Fatal(err)
	}
	var predicate any
	if err := json.Unmarshal([]byte(readString(t, predicatePath)), &predicate); err != nil {
		t.Fatal(err)
	}
	verification := filepath.Join(directory, "verification.json")
	writeAttestationVerification(t, verification, predicate, image, imageDigest)
	if err := validateAttestationFiles(verification, predicatePath, "https://github.com/StarMoe-org/NEXTmoetrabslation/attestations/paired-image/v1", image, imageDigest); err != nil {
		t.Fatal(err)
	}
	altered := map[string]any{"schemaVersion": float64(2)}
	writeAttestationVerification(t, verification, altered, image, imageDigest)
	if err := validateAttestationFiles(verification, predicatePath, "https://github.com/StarMoe-org/NEXTmoetrabslation/attestations/paired-image/v1", image, imageDigest); err == nil || !strings.Contains(err.Error(), "contents") {
		t.Fatalf("altered predicate error = %v", err)
	}
}

func TestValidateFinalTagStateIsIdempotentOnlyForSameDigest(t *testing.T) {
	tags := []string{
		"ghcr.io/starmoe-org/nextmoetrabslation:next-" + strings.Repeat("d", 40) + "-moe-v5.9.6",
		"ghcr.io/starmoe-org/nextmoetrabslation:next-" + strings.Repeat("d", 40) + "-moe-" + testCommit,
	}
	digest := "sha256:" + strings.Repeat("e", 64)
	directory := t.TempDir()
	missingState := filepath.Join(directory, "missing.tsv")
	missingOutput := filepath.Join(directory, "missing.txt")
	mustWrite(t, missingState, []byte(tags[0]+"\t-\n"+tags[1]+"\t-\n"))
	if err := validateFinalTagState(missingState, missingOutput, digest, tags, false); err != nil {
		t.Fatal(err)
	}
	wantMissing := append([]string(nil), tags...)
	sort.Strings(wantMissing)
	if got := strings.Split(strings.TrimSpace(readString(t, missingOutput)), "\n"); !reflect.DeepEqual(got, wantMissing) {
		t.Fatalf("missing tags = %#v", got)
	}
	sameState := filepath.Join(directory, "same.tsv")
	mustWrite(t, sameState, []byte(tags[0]+"\t"+digest+"\n"+tags[1]+"\t"+digest+"\n"))
	if err := validateFinalTagState(sameState, "", digest, tags, true); err != nil {
		t.Fatal(err)
	}
	differentState := filepath.Join(directory, "different.tsv")
	mustWrite(t, differentState, []byte(tags[0]+"\t"+digest+"\n"+tags[1]+"\tsha256:"+strings.Repeat("f", 64)+"\n"))
	if err := validateFinalTagState(differentState, "", digest, tags, true); err == nil || !strings.Contains(err.Error(), "different digest") {
		t.Fatalf("different digest error = %v", err)
	}
}

func validReleaseMetadata() githubRelease {
	release := githubRelease{TagName: testTag, Immutable: true}
	for index, name := range expectedAssetNames(testCommit) {
		release.Assets = append(release.Assets, releaseAsset{ID: int64(index + 1), Name: name, State: "uploaded", Size: validAssetMetadataSize(name, testCommit)})
	}
	return release
}

func validAssetMetadataSize(name, commit string) int64 {
	archive := archiveName(commit)
	switch name {
	case archive:
		return 1024
	case archive + ".commit":
		return 41
	case archive + ".manifest.sha256":
		return 65
	case archive + ".sha256":
		return int64(64 + 2 + len(archive) + 1)
	case archive + ".sigstore.json":
		return 1024
	default:
		panic("unexpected asset name")
	}
}

func fixtureEntries(t *testing.T) []tarEntry {
	t.Helper()
	root := filepath.Join("..", "..", "internal", "workspaceverify", "testdata", "valid")
	entries := []tarEntry{{name: workspaceArchiveRoot, typeflag: tar.TypeDir}}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == root {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		archivePath := workspaceArchiveRoot + "/" + filepath.ToSlash(relative)
		if entry.IsDir() {
			entries = append(entries, tarEntry{name: archivePath, typeflag: tar.TypeDir})
			return nil
		}
		contents, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		entries = append(entries, tarEntry{name: archivePath, data: contents})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func fixtureManifest(t *testing.T) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "internal", "workspaceverify", "testdata", "valid", workspaceverify.ManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func minimalEntries(manifest []byte, extra tarEntry) []tarEntry {
	return []tarEntry{
		{name: workspaceArchiveRoot, typeflag: tar.TypeDir},
		{name: workspaceArchiveRoot + "/" + workspaceverify.ManifestFilename, data: manifest},
		extra,
	}
}

func createReleaseAssets(t *testing.T, entries []tarEntry) string {
	t.Helper()
	assets := t.TempDir()
	archive := filepath.Join(assets, archiveName(testCommit))
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Typeflag: typeflag, Mode: 0o644, Size: int64(len(entry.data))}
		if typeflag == tar.TypeDir {
			header.Mode = 0o755
			header.Size = 0
		}
		if typeflag == tar.TypeSymlink {
			header.Linkname = "index.html"
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := tarWriter.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveSHA := sha256.Sum256(archiveBytes)
	mustWrite(t, archive+".sha256", []byte(hex.EncodeToString(archiveSHA[:])+"  "+archiveName(testCommit)+"\n"))
	mustWrite(t, archive+".commit", []byte(testCommit+"\n"))
	manifest := fixtureManifest(t)
	manifestSHA := sha256.Sum256(manifest)
	mustWrite(t, archive+".manifest.sha256", []byte(hex.EncodeToString(manifestSHA[:])+"\n"))
	mustWrite(t, archive+".sigstore.json", []byte("{}\n"))
	return assets
}

func fakeCosign(t *testing.T, exit int) (string, string) {
	t.Helper()
	directory := t.TempDir()
	executable := filepath.Join(directory, "cosign")
	log := filepath.Join(directory, "arguments")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(log) + "\nexit " + string(rune('0'+exit)) + "\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable, log
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeAttestationVerification(t *testing.T, name string, predicate any, image, imageDigest string) {
	t.Helper()
	statement, err := json.Marshal(map[string]any{
		"_type": intotoStatementType,
		"subject": []any{map[string]any{
			"name": image,
			"digest": map[string]string{
				"sha256": strings.TrimPrefix(imageDigest, "sha256:"),
			},
		}},
		"predicateType": "https://github.com/StarMoe-org/NEXTmoetrabslation/attestations/paired-image/v1",
		"predicate":     predicate,
	})
	if err != nil {
		t.Fatal(err)
	}
	verification, err := json.Marshal([]dsseEnvelope{{
		PayloadType: intotoPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(statement),
	}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, name, append(verification, '\n'))
}

func writeJSONFile(t *testing.T, name string, value any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, name, contents)
}

func mustWrite(t *testing.T, name string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(name, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readString(t *testing.T, name string) string {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err := errorsJoin(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func errorsJoin(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
