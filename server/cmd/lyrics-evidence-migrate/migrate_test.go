package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationImportsRereadsAndPublishesCountsAndDigestsOnly(t *testing.T) {
	fixture := createCheckpointFixture(t, 2)
	destination := filepath.Join(t.TempDir(), "new-ledger")
	result, err := executeMigration(t.Context(), commandOptions{
		checkpointPath: fixture.path, destinationRoot: destination,
		expectedCheckpointSHA: fixture.sha256, expectedEvidenceCount: fixture.evidenceCount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ImportedAcquisitionCount != int64(fixture.evidenceCount) ||
		!canonicalDigest.MatchString(result.AcquisitionIDsSHA256) ||
		!canonicalDigest.MatchString(result.MigrationManifestSHA256) ||
		result.Checkpoint.EvidenceCount != int64(fixture.evidenceCount) {
		t.Fatalf("migration counts/digests are invalid: %+v", result)
	}
	assertPrivateLedgerModes(t, destination)
	manifestPath := filepath.Join(destination, migrationManifestFilename)
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(manifestBody) != result.MigrationManifestSHA256 {
		t.Fatal("migration manifest digest does not bind exact bytes")
	}
	var manifest migrationManifest
	if err := decodeCanonicalJSON(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ImportedAcquisitionCount != int64(fixture.evidenceCount) ||
		manifest.AcquisitionIDsSHA256 != result.AcquisitionIDsSHA256 || manifest.Checkpoint != result.Checkpoint {
		t.Fatalf("migration manifest does not bind the verified import: %+v", manifest)
	}
	resultBody, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range fixture.privateTokens {
		if bytes.Contains(manifestBody, []byte(token)) || bytes.Contains(resultBody, []byte(token)) {
			t.Fatalf("private checkpoint content leaked into counts/digests output")
		}
	}

	beforeInfo, err := os.Lstat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeBody := append([]byte(nil), manifestBody...)
	if _, err := executeMigration(t.Context(), commandOptions{
		checkpointPath: fixture.path, destinationRoot: destination,
		expectedCheckpointSHA: fixture.sha256, expectedEvidenceCount: fixture.evidenceCount,
	}); err == nil {
		t.Fatal("migration reused an existing destination root")
	}
	afterBody, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Lstat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(beforeInfo, afterInfo) || !bytes.Equal(beforeBody, afterBody) {
		t.Fatal("failed repeated migration overwrote the create-exclusive manifest")
	}
}

func TestMigrationManifestPublicationIsCreateExclusive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := migrationManifest{
		SchemaVersion: 1,
		Checkpoint: checkpointSummary{
			CheckpointSHA256: strings.Repeat("a", 64), CheckpointBytes: 4096,
			CatalogCount: 1, ResultCount: 1, EvidenceCount: 1,
			EvidenceRawBytes: 1, EvidenceJSONBytes: 1, EvidenceRowsSHA256: strings.Repeat("b", 64),
		},
		ImportedAcquisitionCount: 1, AcquisitionIDsSHA256: strings.Repeat("c", 64),
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishMigrationManifest(root, body); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, migrationManifestFilename)
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishMigrationManifest(root, body); err == nil {
		t.Fatal("migration manifest publication overwrote an existing path")
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !bytes.Equal(got, body) {
		t.Fatal("failed create-exclusive publication changed the existing manifest")
	}
}

func TestMigrationCLIEmitsNoPrivateEvidenceContent(t *testing.T) {
	fixture := createCheckpointFixture(t, 1)
	destination := filepath.Join(t.TempDir(), "new-ledger")
	var stdout, stderr bytes.Buffer
	err := runCLI(t.Context(), []string{
		"-checkpoint", fixture.path,
		"-destination", destination,
		"-expected-checkpoint-sha256", fixture.sha256,
		"-expected-evidence-count", strconv.Itoa(fixture.evidenceCount),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("synthetic CLI migration error=%v stderr=%q", err, stderr.String())
	}
	var result migrationResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode CLI summary: %v", err)
	}
	if result.ImportedAcquisitionCount != int64(fixture.evidenceCount) || result.Checkpoint.EvidenceCount != int64(fixture.evidenceCount) {
		t.Fatalf("CLI summary counts changed: %+v", result)
	}
	for _, token := range fixture.privateTokens {
		if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
			t.Fatal("CLI output disclosed private evidence content")
		}
	}
	assertPrivateLedgerModes(t, destination)
}

func TestMigrationCLIRejectsInvalidExpectationsBeforeDestinationCreation(t *testing.T) {
	fixture := createCheckpointFixture(t, 1)
	tests := []struct {
		name         string
		expectations []string
	}{
		{name: "omitted"},
		{name: "omitted checkpoint digest", expectations: []string{"-expected-evidence-count", "1"}},
		{name: "omitted evidence count", expectations: []string{"-expected-checkpoint-sha256", fixture.sha256}},
		{name: "malformed checkpoint digest", expectations: []string{
			"-expected-checkpoint-sha256", strings.Repeat("A", 64), "-expected-evidence-count", "1",
		}},
		{name: "malformed evidence count", expectations: []string{
			"-expected-checkpoint-sha256", fixture.sha256, "-expected-evidence-count", "not-a-count",
		}},
		{name: "zero evidence count", expectations: []string{
			"-expected-checkpoint-sha256", fixture.sha256, "-expected-evidence-count", "0",
		}},
		{name: "negative evidence count", expectations: []string{
			"-expected-checkpoint-sha256", fixture.sha256, "-expected-evidence-count", "-1",
		}},
		{name: "excessive evidence count", expectations: []string{
			"-expected-checkpoint-sha256", fixture.sha256,
			"-expected-evidence-count", strconv.Itoa(maxEvidenceItems + 1),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "must-not-exist")
			args := []string{"-checkpoint", fixture.path, "-destination", destination}
			args = append(args, test.expectations...)
			var stdout, stderr bytes.Buffer
			if err := runCLI(t.Context(), args, &stdout, &stderr); err == nil {
				t.Fatal("CLI accepted an invalid or omitted expectation")
			}
			if stdout.Len() != 0 {
				t.Fatalf("rejected CLI wrote a summary: %q", stdout.String())
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected CLI created destination: %v", err)
			}
		})
	}
}

func assertPrivateLedgerModes(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("ledger contains symlink %s", filepath.Base(path))
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0o700 {
				t.Fatalf("ledger directory %s mode=%o", filepath.Base(path), info.Mode().Perm())
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("ledger file %s mode=%v", filepath.Base(path), info.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
