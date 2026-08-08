package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const (
	approvedCheckpointPath   = "/private/tmp/moesekai-704-extraction-runbook/runs/run-20260801T121020Z-35875/lyrics-preflight-704-three-20260801T121020Z-35875.checkpoint.sqlite"
	approvedCheckpointSHA256 = "af4bbfaa2b0dd984d84cde73d9e7e343a30d65ec10819cb74548b62ff9a57f92"
	approvedEvidenceCount    = 126
)

func TestApprovedRealCheckpointReadOnlyDryRunPreservesSource(t *testing.T) {
	beforeInfo, err := os.Lstat(approvedCheckpointPath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("approved immutable checkpoint is not present on this host")
	}
	if err != nil {
		t.Fatal(err)
	}
	beforeFile, err := os.Open(approvedCheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeSHA256, hashErr := hashPinnedCheckpoint(beforeFile, beforeInfo.Size())
	closeErr := beforeFile.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatal(errors.Join(hashErr, closeErr))
	}
	if beforeSHA256 != approvedCheckpointSHA256 || beforeInfo.Mode().Perm() != 0o600 || sourceLinkCount(beforeInfo) != 1 {
		t.Fatal("approved checkpoint precondition changed")
	}
	for _, companion := range sourcePathFamily(approvedCheckpointPath)[1:] {
		if _, err := os.Lstat(companion); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("approved checkpoint has a preexisting protected companion: %v", err)
		}
	}
	destination := filepath.Join(t.TempDir(), "must-not-be-created")
	var stdout, stderr bytes.Buffer
	err = runCLI(t.Context(), []string{
		"-checkpoint", approvedCheckpointPath,
		"-destination", destination,
		"-dry-run",
		"-expected-checkpoint-sha256", approvedCheckpointSHA256,
		"-expected-evidence-count", strconv.Itoa(approvedEvidenceCount),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("approved checkpoint dry run: %v stderr=%q", err, stderr.String())
	}
	var result migrationResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode approved checkpoint dry-run summary: %v", err)
	}
	if result.ImportedAcquisitionCount != 0 || result.MigrationManifestSHA256 != "" ||
		result.Checkpoint.CheckpointSHA256 != approvedCheckpointSHA256 || result.Checkpoint.CheckpointBytes != 46_071_808 ||
		result.Checkpoint.CatalogCount != 704 || result.Checkpoint.ResultCount != 658 || result.Checkpoint.EvidenceCount != 126 ||
		result.Checkpoint.EvidenceRawBytes != 33_431_610 || result.Checkpoint.EvidenceJSONBytes != 44_671_916 ||
		result.Checkpoint.EvidenceRowsSHA256 != "41fae940b1e3a41b3c39f5ea8254210d1fe4b208fe7aac2f6cd4137cc63baa2a" {
		t.Fatalf("approved checkpoint dry-run summary changed: %+v", result)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only dry run created a destination: %v", err)
	}
	afterInfo, err := os.Lstat(approvedCheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	afterFile, err := os.Open(approvedCheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	afterSHA256, hashErr := hashPinnedCheckpoint(afterFile, afterInfo.Size())
	closeErr = afterFile.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatal(errors.Join(hashErr, closeErr))
	}
	if !os.SameFile(beforeInfo, afterInfo) || afterInfo.Mode().Perm() != 0o600 || sourceLinkCount(afterInfo) != 1 ||
		afterInfo.Size() != beforeInfo.Size() || afterSHA256 != approvedCheckpointSHA256 {
		t.Fatal("read-only dry run changed the approved checkpoint inode, mode, links, size, or bytes")
	}
	for _, companion := range sourcePathFamily(approvedCheckpointPath)[1:] {
		if _, err := os.Lstat(companion); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only dry run created a checkpoint anchor or sidecar: %v", err)
		}
	}
}
