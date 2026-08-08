package lyricsrecoveryimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/model"
)

const (
	ImportReceiptSchemaVersion        = 1
	ImportReceiptKind                 = "lyrics-recovery-import-receipt-v1"
	ImportReceiptRuntimeSchemaVersion = 27
	ImportReceiptAuditAction          = "lyrics.import_recovery"
	MaxImportReceiptBytes             = 4 << 20
)

type ImportReceiptItem struct {
	MusicID                    int                              `json:"musicId"`
	State                      lyricsrootmanifest.CoverageState `json:"state"`
	Revision                   int                              `json:"revision"`
	DocumentSHA256             string                           `json:"documentSha256,omitempty"`
	AvailabilityDocumentSHA256 string                           `json:"availabilityDocumentSha256,omitempty"`
}

type ImportStorageCounts struct {
	BatchItems             int `json:"batchItems"`
	EditableLyrics         int `json:"editableLyrics"`
	SourceDocuments        int `json:"sourceDocuments"`
	AvailabilityDocuments  int `json:"availabilityDocuments"`
	Artifacts              int `json:"artifacts"`
	EvidenceSelection      int `json:"evidenceSelection"`
	ArtifactEvidenceLinks  int `json:"artifactEvidenceLinks"`
	ComponentContributions int `json:"componentContributions"`
}

type ImportReceipt struct {
	SchemaVersion             int                         `json:"schemaVersion"`
	Kind                      string                      `json:"kind"`
	RuntimeSchemaVersion      int                         `json:"runtimeSchemaVersion"`
	DatabaseAuditAction       string                      `json:"databaseAuditAction"`
	RootManifestFileSHA256    string                      `json:"rootManifestFileSha256"`
	RootID                    string                      `json:"rootId"`
	RootSHA256                string                      `json:"rootSha256"`
	ImportManifestFileSHA256  string                      `json:"importManifestFileSha256"`
	BatchSHA256               string                      `json:"batchSha256"`
	EvidenceReceiptFileSHA256 string                      `json:"evidenceReceiptFileSha256"`
	EvidenceReceiptSHA256     string                      `json:"evidenceReceiptSha256"`
	PackSHA256                string                      `json:"packSha256"`
	SelectionSHA256           string                      `json:"selectionSha256"`
	CatalogCount              int                         `json:"catalogCount"`
	MusicIDsSHA256            string                      `json:"musicIdsSha256"`
	Coverage                  lyricsrootmanifest.Coverage `json:"coverage"`
	DatabasePath              string                      `json:"databasePath"`
	ReceiptPath               string                      `json:"receiptPath"`
	Actor                     string                      `json:"actor"`
	CommittedAt               string                      `json:"committedAt"`
	Counts                    ImportStorageCounts         `json:"counts"`
	Items                     []ImportReceiptItem         `json:"items"`
	ReceiptSHA256             string                      `json:"receiptSha256"`
}

type ImportReceiptBinding struct {
	RootManifestFileSHA256    string
	ImportManifestFileSHA256  string
	EvidenceReceiptFileSHA256 string
	DatabasePath              string
	ReceiptPath               string
	Actor                     string
	CommittedAt               string
	Counts                    ImportStorageCounts
	Items                     []ImportReceiptItem
}

func NewImportReceipt(
	root lyricsrootmanifest.Manifest,
	manifest Manifest,
	evidence EvidenceReceipt,
	binding ImportReceiptBinding,
) (ImportReceipt, error) {
	receipt := ImportReceipt{
		SchemaVersion:             ImportReceiptSchemaVersion,
		Kind:                      ImportReceiptKind,
		RuntimeSchemaVersion:      ImportReceiptRuntimeSchemaVersion,
		DatabaseAuditAction:       ImportReceiptAuditAction,
		RootManifestFileSHA256:    binding.RootManifestFileSHA256,
		RootID:                    root.RootID,
		RootSHA256:                root.RootSHA256,
		ImportManifestFileSHA256:  binding.ImportManifestFileSHA256,
		BatchSHA256:               manifest.BatchSHA256,
		EvidenceReceiptFileSHA256: binding.EvidenceReceiptFileSHA256,
		EvidenceReceiptSHA256:     evidence.ReceiptSHA256,
		PackSHA256:                evidence.PackSHA256,
		SelectionSHA256:           evidence.SelectionSHA256,
		CatalogCount:              manifest.Root.CatalogCount,
		MusicIDsSHA256:            manifest.Root.MusicIDsSHA256,
		Coverage:                  manifest.Root.Coverage,
		DatabasePath:              binding.DatabasePath,
		ReceiptPath:               binding.ReceiptPath,
		Actor:                     binding.Actor,
		CommittedAt:               binding.CommittedAt,
		Counts:                    binding.Counts,
		Items:                     append([]ImportReceiptItem(nil), binding.Items...),
	}
	digest, err := importReceiptDigest(receipt)
	if err != nil {
		return ImportReceipt{}, err
	}
	receipt.ReceiptSHA256 = digest
	if err := ValidateImportReceiptAgainst(receipt, root, manifest, evidence); err != nil {
		return ImportReceipt{}, err
	}
	return receipt, nil
}

func ValidateImportReceiptAgainst(
	receipt ImportReceipt,
	root lyricsrootmanifest.Manifest,
	manifest Manifest,
	evidence EvidenceReceipt,
) error {
	if err := ValidateImportReceipt(receipt); err != nil {
		return err
	}
	if err := lyricsrootmanifest.Validate(root); err != nil {
		return err
	}
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if err := ValidateEvidenceReceipt(evidence); err != nil {
		return err
	}
	if evidence.RootID != root.RootID || evidence.RootSHA256 != root.RootSHA256 ||
		manifest.Root.RootID != root.RootID || manifest.Root.RootSHA256 != root.RootSHA256 ||
		receipt.RootID != root.RootID || receipt.RootSHA256 != root.RootSHA256 ||
		receipt.BatchSHA256 != manifest.BatchSHA256 || receipt.EvidenceReceiptSHA256 != evidence.ReceiptSHA256 ||
		receipt.PackSHA256 != evidence.PackSHA256 || receipt.SelectionSHA256 != evidence.SelectionSHA256 ||
		receipt.CatalogCount != manifest.Root.CatalogCount || receipt.MusicIDsSHA256 != manifest.Root.MusicIDsSHA256 ||
		receipt.Coverage != manifest.Root.Coverage || len(receipt.Items) != len(manifest.Items) {
		return errors.New("recovery import receipt does not match its root, manifest, or evidence receipt")
	}
	expectedCounts := ExpectedImportStorageCounts(manifest, evidence)
	if receipt.Counts != expectedCounts {
		return errors.New("recovery import receipt storage counts do not match the import manifest")
	}
	for index, item := range receipt.Items {
		manifestItem := manifest.Items[index]
		if item.MusicID != manifestItem.MusicID || item.State != manifestItem.State ||
			item.DocumentSHA256 != recoveryItemDocumentSHA256(manifestItem) ||
			item.AvailabilityDocumentSHA256 != manifestItem.AvailabilityDocumentSHA256 {
			return fmt.Errorf("recovery import receipt item %d drifted from the import manifest", index)
		}
		requiresPositiveRevision := importReceiptItemOwnsEditableLyrics(manifestItem)
		if requiresPositiveRevision && item.Revision <= 0 || !requiresPositiveRevision && item.Revision != 0 {
			return fmt.Errorf("recovery import receipt item %d revision ownership drifted from the import manifest", index)
		}
	}
	return nil
}

func ValidateImportReceipt(receipt ImportReceipt) error {
	if receipt.SchemaVersion != ImportReceiptSchemaVersion || receipt.Kind != ImportReceiptKind ||
		receipt.RuntimeSchemaVersion != ImportReceiptRuntimeSchemaVersion ||
		receipt.DatabaseAuditAction != ImportReceiptAuditAction ||
		!canonicalSHA256.MatchString(receipt.RootManifestFileSHA256) || !compactImportReceiptID(receipt.RootID) ||
		!canonicalSHA256.MatchString(receipt.RootSHA256) || !canonicalSHA256.MatchString(receipt.ImportManifestFileSHA256) ||
		!canonicalSHA256.MatchString(receipt.BatchSHA256) || !canonicalSHA256.MatchString(receipt.EvidenceReceiptFileSHA256) ||
		!canonicalSHA256.MatchString(receipt.EvidenceReceiptSHA256) || !canonicalSHA256.MatchString(receipt.PackSHA256) ||
		!canonicalSHA256.MatchString(receipt.SelectionSHA256) || receipt.CatalogCount <= 0 ||
		!canonicalSHA256.MatchString(receipt.MusicIDsSHA256) || !canonicalImportReceiptPath(receipt.DatabasePath) ||
		!canonicalImportReceiptPath(receipt.ReceiptPath) || receipt.DatabasePath == receipt.ReceiptPath ||
		receipt.Actor == "" || receipt.Actor != strings.TrimSpace(receipt.Actor) || len(receipt.Actor) > 128 ||
		!utf8.ValidString(receipt.Actor) || strings.ContainsAny(receipt.Actor, "\x00\r\n") ||
		receipt.Items == nil || len(receipt.Items) != receipt.CatalogCount || len(receipt.Items) > MaxManifestItems ||
		!canonicalSHA256.MatchString(receipt.ReceiptSHA256) {
		return errors.New("recovery import receipt envelope is invalid")
	}
	if _, err := canonicalImportReceiptTimestamp(receipt.CommittedAt); err != nil {
		return err
	}
	if !validImportStorageCounts(receipt.Counts, receipt.CatalogCount) {
		return errors.New("recovery import receipt storage counts are invalid")
	}
	states := map[lyricsrootmanifest.CoverageState]int{}
	lastMusicID := 0
	for index, item := range receipt.Items {
		if item.MusicID <= lastMusicID {
			return errors.New("recovery import receipt items are not strictly ordered")
		}
		lastMusicID = item.MusicID
		switch item.State {
		case lyricsrootmanifest.CoverageComplete:
			// Source-v3 peer documents that are not losslessly editable through the
			// legacy song_lyrics projection intentionally own revision 0. Exact
			// positive-vs-zero ownership is checked against the import manifest.
			if item.Revision < 0 || !canonicalSHA256.MatchString(item.DocumentSHA256) || item.AvailabilityDocumentSHA256 != "" {
				return fmt.Errorf("recovery import receipt complete item %d is invalid", index)
			}
		case lyricsrootmanifest.CoverageGameOnly:
			if item.DocumentSHA256 != "" && item.AvailabilityDocumentSHA256 != "" ||
				item.DocumentSHA256 == "" && item.AvailabilityDocumentSHA256 == "" ||
				item.DocumentSHA256 != "" && !canonicalSHA256.MatchString(item.DocumentSHA256) ||
				item.AvailabilityDocumentSHA256 != "" && !canonicalSHA256.MatchString(item.AvailabilityDocumentSHA256) || item.Revision < 0 {
				return fmt.Errorf("recovery import receipt Game-only item %d is invalid", index)
			}
		case lyricsrootmanifest.CoverageSatisfiedNoLyrics, lyricsrootmanifest.CoverageAmbiguous,
			lyricsrootmanifest.CoverageMissing, lyricsrootmanifest.CoverageIncomplete, lyricsrootmanifest.CoverageFailed:
			if item.Revision != 0 || item.DocumentSHA256 != "" ||
				!canonicalSHA256.MatchString(item.AvailabilityDocumentSHA256) {
				return fmt.Errorf("recovery import receipt availability item %d is invalid", index)
			}
		default:
			return fmt.Errorf("recovery import receipt item %d has an unsupported state", index)
		}
		states[item.State]++
	}
	if !coverageCountsMatch(receipt.Coverage, states, len(receipt.Items)) {
		return errors.New("recovery import receipt coverage does not match its items")
	}
	digest, err := importReceiptDigest(receipt)
	if err != nil || digest != receipt.ReceiptSHA256 {
		return errors.New("recovery import receipt digest does not match")
	}
	body, err := json.Marshal(receipt)
	if err != nil || len(body) > MaxImportReceiptBytes {
		return errors.New("recovery import receipt exceeds its byte boundary")
	}
	return nil
}

func importReceiptItemOwnsEditableLyrics(item Item) bool {
	if item.State != lyricsrootmanifest.CoverageComplete && item.State != lyricsrootmanifest.CoverageGameOnly {
		return false
	}
	if item.Draft == nil {
		return item.State == lyricsrootmanifest.CoverageComplete
	}
	// Every source-v3 draft is owned by the plural rendition editor and keeps
	// revision 0 in the legacy import receipt even when its rendition set is
	// losslessly representable as a v2 document. Only legacy/v2 drafts own the
	// singular SongLyrics projection and a positive editable revision.
	return item.Draft.Document.SchemaVersion != model.LyricsSourceDocumentSchemaVersionV3
}

func ExpectedImportStorageCounts(manifest Manifest, evidence EvidenceReceipt) ImportStorageCounts {
	counts := ImportStorageCounts{BatchItems: len(manifest.Items), EvidenceSelection: evidence.EvidenceCount}
	for _, item := range manifest.Items {
		artifacts := item.Artifacts
		if item.Draft != nil {
			counts.SourceDocuments++
			artifacts = item.Draft.Artifacts
			// Source-v3 documents never materialize the legacy SongLyrics
			// projection, even when their rendition set is losslessly v2-shaped.
			if item.Draft.Document.SchemaVersion != model.LyricsSourceDocumentSchemaVersionV3 {
				counts.EditableLyrics++
			}
			if item.Draft.Document.SchemaVersion == model.LyricsSourceDocumentSchemaVersionV3 {
				bindings, _ := model.EnumerateLyricsSourceRenditionComponents(item.Draft.Document.Renditions)
				counts.ComponentContributions += len(bindings)
			} else {
				counts.ComponentContributions += sourceProvenanceComponentCount(item.Draft.Document.Provenance)
			}
		} else if item.Availability != nil && item.State == lyricsrootmanifest.CoverageGameOnly {
			counts.ComponentContributions += availabilityProvenanceComponentCount(item.Availability.Provenance)
		} else if item.Availability != nil {
			counts.AvailabilityDocuments++
		}
		if item.Draft == nil && item.Availability != nil && item.State == lyricsrootmanifest.CoverageGameOnly {
			counts.AvailabilityDocuments++
		}
		counts.Artifacts += len(artifacts)
		for _, artifact := range artifacts {
			counts.ArtifactEvidenceLinks += len(artifact.Identity.IndexEvidenceRefs)
		}
	}
	return counts
}

func sourceProvenanceComponentCount(provenance model.LyricsSourceComponentProvenance) int {
	count := 2 // Full text and version evidence are mandatory.
	if provenance.PerformerSegmentation != nil {
		count++
	}
	if provenance.Ruby != nil {
		count++
	}
	if provenance.GameText != nil {
		count++
	}
	if provenance.GameProjection != nil {
		count++
	}
	return count
}

func availabilityProvenanceComponentCount(provenance model.LyricsAvailabilityComponentProvenance) int {
	count := 2 // Game text and version evidence are mandatory.
	if provenance.PerformerSegmentation != nil {
		count++
	}
	if provenance.Ruby != nil {
		count++
	}
	return count
}

func recoveryItemDocumentSHA256(item Item) string {
	if item.Draft == nil {
		return ""
	}
	return item.Draft.DocumentSHA256
}

func MarshalImportReceiptCanonical(receipt ImportReceipt) ([]byte, error) {
	if err := ValidateImportReceipt(receipt); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if len(body) > MaxImportReceiptBytes {
		return nil, errors.New("recovery import receipt exceeds its canonical byte boundary")
	}
	return body, nil
}

func DecodeImportReceiptCanonical(body []byte) (ImportReceipt, error) {
	if len(body) == 0 || len(body) > MaxImportReceiptBytes || !utf8.Valid(body) {
		return ImportReceipt{}, errors.New("recovery import receipt bytes are invalid")
	}
	if err := legacy.ValidateUniqueJSON(body); err != nil {
		return ImportReceipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var receipt ImportReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return ImportReceipt{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ImportReceipt{}, errors.New("recovery import receipt contains trailing JSON")
	}
	if err := ValidateImportReceipt(receipt); err != nil {
		return ImportReceipt{}, err
	}
	canonical, err := MarshalImportReceiptCanonical(receipt)
	if err != nil || !bytes.Equal(canonical, body) {
		return ImportReceipt{}, errors.New("recovery import receipt is not canonical JSON")
	}
	return receipt, nil
}

func importReceiptDigest(receipt ImportReceipt) (string, error) {
	receipt.ReceiptSHA256 = ""
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalImportReceiptPath(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		filepath.IsAbs(value) && filepath.Clean(value) == value
}

func canonicalImportReceiptTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !strings.HasSuffix(value, "Z") || parsed.UTC().Format(time.RFC3339Nano) != value || parsed.Unix() <= 0 {
		return time.Time{}, errors.New("recovery import receipt committedAt is not canonical UTC")
	}
	return parsed, nil
}

func compactImportReceiptID(value string) bool {
	return value != "" && len(value) <= 256 && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00\r\n ")
}

func validImportStorageCounts(counts ImportStorageCounts, total int) bool {
	if counts.BatchItems != total || counts.EditableLyrics < 0 || counts.EditableLyrics > counts.SourceDocuments ||
		counts.SourceDocuments < 0 || counts.SourceDocuments > total || counts.AvailabilityDocuments != total-counts.SourceDocuments ||
		counts.Artifacts < 0 || counts.Artifacts > total*64 || counts.EvidenceSelection < 0 ||
		counts.ArtifactEvidenceLinks < 0 || counts.ComponentContributions < 0 {
		return false
	}
	if counts.Artifacts == 0 {
		return counts.ArtifactEvidenceLinks == 0 && counts.ComponentContributions == 0
	}
	return counts.EvidenceSelection > 0 && counts.ArtifactEvidenceLinks > 0 && counts.ComponentContributions > 0
}
