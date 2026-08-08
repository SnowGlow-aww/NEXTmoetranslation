package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"moesekai/server/internal/lyricsacquisition"
	"moesekai/server/internal/lyricssource"
)

type migrationResult struct {
	SchemaVersion            int               `json:"schemaVersion"`
	Checkpoint               checkpointSummary `json:"checkpoint"`
	ImportedAcquisitionCount int64             `json:"importedAcquisitionCount"`
	AcquisitionIDsSHA256     string            `json:"acquisitionIdsSha256"`
	MigrationManifestSHA256  string            `json:"migrationManifestSha256,omitempty"`
}

type migrationManifest struct {
	SchemaVersion            int               `json:"schemaVersion"`
	Checkpoint               checkpointSummary `json:"checkpoint"`
	ImportedAcquisitionCount int64             `json:"importedAcquisitionCount"`
	AcquisitionIDsSHA256     string            `json:"acquisitionIdsSha256"`
}

func executeMigration(ctx context.Context, options commandOptions) (migrationResult, error) {
	var result migrationResult
	if ctx == nil {
		return result, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !canonicalDigest.MatchString(options.expectedCheckpointSHA) {
		return result, errors.New("expected checkpoint SHA-256 must be exactly 64 lowercase hexadecimal characters")
	}
	if options.expectedEvidenceCount <= 0 || options.expectedEvidenceCount > maxEvidenceItems {
		return result, fmt.Errorf("expected evidence count must be between 1 and %d", maxEvidenceItems)
	}
	checkpoint, err := openSourceCheckpoint(ctx, options.checkpointPath)
	if err != nil {
		return result, err
	}
	defer checkpoint.Close()
	if checkpoint.sha256 != options.expectedCheckpointSHA {
		return result, errors.New("checkpoint SHA-256 does not match the explicit immutable-source expectation")
	}
	summary, err := checkpoint.validate(ctx)
	if err != nil {
		return result, err
	}
	if summary.EvidenceCount != int64(options.expectedEvidenceCount) {
		return result, errors.New("checkpoint evidence count does not match the explicit complete-set expectation")
	}
	result = migrationResult{SchemaVersion: 1, Checkpoint: summary, AcquisitionIDsSHA256: sha256Hex(nil)}
	if options.dryRun {
		return result, checkpoint.verifyDigest("after read-only dry run")
	}
	if err := checkpoint.rejectDestinationAlias(options.destinationRoot); err != nil {
		return result, err
	}
	ledger, err := lyricsacquisition.CreateLedger(ctx, options.destinationRoot)
	if err != nil {
		return result, fmt.Errorf("create new private acquisition ledger: %w", err)
	}
	ledgerClosed := false
	defer func() {
		if !ledgerClosed {
			_ = ledger.Close()
		}
	}()

	acquisitionIDs := make([]lyricsacquisition.AcquisitionID, 0, options.expectedEvidenceCount)
	acquisitionDigest := sha256.New()
	err = checkpoint.forEachEvidence(ctx, func(item storedEvidence) error {
		record, err := migrationRecord(item)
		if err != nil {
			return err
		}
		committed, err := ledger.Commit(ctx, record)
		if err != nil {
			return fmt.Errorf("commit exact checkpoint evidence acquisition: %w", err)
		}
		acquisitionIDs = append(acquisitionIDs, committed.AcquisitionID)
		hashFrame(acquisitionDigest, []byte(committed.AcquisitionID))
		return nil
	})
	if err != nil {
		return result, err
	}
	if len(acquisitionIDs) != options.expectedEvidenceCount {
		return result, errors.New("imported acquisition count changed during migration")
	}

	index := 0
	err = checkpoint.forEachEvidence(ctx, func(item storedEvidence) error {
		if index >= len(acquisitionIDs) {
			return errors.New("checkpoint reread produced an unexpected acquisition")
		}
		record, err := migrationRecord(item)
		if err != nil {
			return err
		}
		replayed, err := ledger.ReplayByAcquisitionID(ctx, acquisitionIDs[index])
		if err != nil {
			return fmt.Errorf("reread exact imported acquisition: %w", err)
		}
		if !sameMigratedRecord(record, replayed) {
			return errors.New("reread imported acquisition does not match the exact checkpoint evidence row")
		}
		index++
		return nil
	})
	if err != nil {
		return result, err
	}
	if index != len(acquisitionIDs) {
		return result, errors.New("checkpoint reread omitted an imported acquisition")
	}
	if err := checkpoint.verifyDigest("after import and reread"); err != nil {
		return result, err
	}
	if err := ledger.Close(); err != nil {
		return result, fmt.Errorf("close imported acquisition ledger: %w", err)
	}
	ledgerClosed = true

	result.ImportedAcquisitionCount = int64(len(acquisitionIDs))
	result.AcquisitionIDsSHA256 = hex.EncodeToString(acquisitionDigest.Sum(nil))
	manifest := migrationManifest{
		SchemaVersion: 1, Checkpoint: summary,
		ImportedAcquisitionCount: result.ImportedAcquisitionCount,
		AcquisitionIDsSHA256:     result.AcquisitionIDsSHA256,
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return result, fmt.Errorf("encode migration manifest: %w", err)
	}
	manifestSHA, err := publishMigrationManifest(options.destinationRoot, manifestBody)
	if err != nil {
		return result, err
	}
	result.MigrationManifestSHA256 = manifestSHA

	reopened, err := lyricsacquisition.OpenLedger(ctx, options.destinationRoot)
	if err != nil {
		return result, fmt.Errorf("reopen imported acquisition ledger after manifest publication: %w", err)
	}
	if err := reopened.Close(); err != nil {
		return result, fmt.Errorf("close revalidated acquisition ledger: %w", err)
	}
	return result, checkpoint.verifyDigest("after migration manifest publication")
}

func migrationRecord(item storedEvidence) (lyricsacquisition.RecordInput, error) {
	envelope := item.envelope
	request := lyricsacquisition.Request{Provider: string(envelope.Provider)}
	switch envelope.Kind {
	case lyricssource.IndexEvidenceKindMediaWikiSearchResponse:
		request.Kind = lyricsacquisition.RequestKindSearch
		request.CanonicalRequestIdentity = envelope.CanonicalRequestURL
	case lyricssource.IndexEvidenceKindMediaWikiRevision:
		request.Kind = lyricsacquisition.RequestKindRevision
		if envelope.Provider == lyricssource.ProviderMoegirl && strings.HasPrefix(envelope.EvidenceID, "search:moegirl:") {
			request.Kind = lyricsacquisition.RequestKindFixedIndex
		}
		request.CanonicalRequestIdentity = envelope.CanonicalURL
		request.RevisionSelector = "oldid:" + strconv.Itoa(envelope.RevisionID)
	default:
		return lyricsacquisition.RecordInput{}, errors.New("checkpoint evidence kind cannot be mapped to an acquisition request")
	}
	observed := []lyricsacquisition.ObservedRevision{}
	if envelope.Kind == lyricssource.IndexEvidenceKindMediaWikiRevision {
		observed = append(observed, lyricsacquisition.ObservedRevision{
			Selector: request.RevisionSelector, RevisionID: int64(envelope.RevisionID),
			Timestamp: envelope.RevisionTimestamp, SHA1: envelope.MediaWikiSHA1,
		})
	}
	return lyricsacquisition.RecordInput{
		Request: request, FetchedAt: envelope.FetchedAt,
		RawResponse: append([]byte(nil), envelope.Raw...), RawResponseSHA256: envelope.RawSHA256,
		Evidence: lyricsacquisition.EvidenceProjection{
			EvidenceID: envelope.EvidenceID, Raw: append([]byte(nil), envelope.Raw...), RawSHA256: envelope.RawSHA256,
		},
		EvidenceEnvelope: append([]byte(nil), item.body...), EvidenceEnvelopeSHA256: item.bodySHA256,
		ObservedRevisions: observed,
	}, nil
}

func sameMigratedRecord(record lyricsacquisition.RecordInput, replayed lyricsacquisition.Acquisition) bool {
	return replayed.ReplayOnly && reflect.DeepEqual(replayed.Request, record.Request) && replayed.FetchedAt == record.FetchedAt &&
		bytesEqual(replayed.RawResponse, record.RawResponse) && replayed.RawResponseSHA256 == record.RawResponseSHA256 &&
		reflect.DeepEqual(replayed.Evidence, record.Evidence) && bytesEqual(replayed.EvidenceEnvelope, record.EvidenceEnvelope) &&
		replayed.EvidenceEnvelopeSHA256 == record.EvidenceEnvelopeSHA256 && reflect.DeepEqual(replayed.ObservedRevisions, record.ObservedRevisions)
}

func bytesEqual(left, right []byte) bool {
	return bytes.Equal(left, right)
}
