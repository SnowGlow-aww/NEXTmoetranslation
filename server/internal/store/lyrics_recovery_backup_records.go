package store

import (
	"context"
	"database/sql"
)

type LyricsRecoveryBatchBackupRecord struct {
	BatchSHA256           string `json:"batchSha256"`
	SchemaVersion         int    `json:"schemaVersion"`
	RootSchemaVersion     int    `json:"rootSchemaVersion"`
	RootID                string `json:"rootId"`
	RootSHA256            string `json:"rootSha256"`
	CatalogCount          int    `json:"catalogCount"`
	MusicIDsSHA256        string `json:"musicIdsSha256"`
	CoverageJSON          string `json:"coverageJson"`
	EvidenceReceiptSHA256 string `json:"evidenceReceiptSha256"`
	PackSHA256            string `json:"packSha256"`
	SelectionSHA256       string `json:"selectionSha256"`
	EvidenceCount         int    `json:"evidenceCount"`
	ShardCount            int    `json:"shardCount"`
	RawByteCount          int64  `json:"rawByteCount"`
	EncodedByteCount      int64  `json:"encodedByteCount"`
	Actor                 string `json:"actor"`
	CreatedAt             int64  `json:"createdAt"`
}

type LyricsRecoveryItemBackupRecord struct {
	BatchSHA256                string `json:"batchSha256"`
	MusicID                    int    `json:"musicId"`
	JapaneseTitle              string `json:"japaneseTitle"`
	CatalogFingerprint         string `json:"catalogFingerprint"`
	TargetMusicID              int    `json:"targetMusicId"`
	AssociationMusicIDsJSON    string `json:"associationMusicIdsJson"`
	State                      string `json:"state"`
	ResultSHA256               string `json:"resultSha256"`
	DraftSHA256                string `json:"draftSha256"`
	DocumentSHA256             string `json:"documentSha256"`
	AvailabilityDocumentSHA256 string `json:"availabilityDocumentSha256"`
	CreatedAt                  int64  `json:"createdAt"`
}

type LyricsRecoverySourceEvidenceBackupRecord struct {
	Provider             string `json:"provider"`
	EvidenceID           string `json:"evidenceId"`
	SHA256               string `json:"sha256"`
	AcquisitionID        string `json:"acquisitionId"`
	EnvelopeSHA256       string `json:"envelopeSha256"`
	Kind                 string `json:"kind"`
	Origin               string `json:"origin"`
	PageID               int    `json:"pageId,omitempty"`
	RevisionID           int    `json:"revisionId,omitempty"`
	RevisionTimestamp    string `json:"revisionTimestamp,omitempty"`
	MediaWikiSHA1        string `json:"mediawikiSha1,omitempty"`
	PageTitle            string `json:"pageTitle,omitempty"`
	CanonicalRevisionURL string `json:"canonicalRevisionUrl,omitempty"`
	CategoriesJSON       string `json:"categoriesJson"`
	CanonicalRequestURL  string `json:"canonicalRequestUrl,omitempty"`
	FetchedAt            string `json:"fetchedAt"`
	RawBytes             []byte `json:"rawBytes"`
	RawByteCount         int    `json:"rawByteCount"`
	RawSHA256            string `json:"rawSha256"`
	CreatedAt            int64  `json:"createdAt"`
}

type LyricsRecoveryArtifactBackupRecord struct {
	BatchSHA256             string `json:"batchSha256"`
	MusicID                 int    `json:"musicId"`
	Provider                string `json:"provider"`
	RenditionKey            string `json:"renditionKey"`
	Origin                  string `json:"origin"`
	PageID                  int    `json:"pageId"`
	RevisionID              int    `json:"revisionId"`
	RevisionTimestamp       string `json:"revisionTimestamp,omitempty"`
	MediaWikiSHA1           string `json:"mediawikiSha1"`
	PageTitle               string `json:"pageTitle"`
	CanonicalRevisionURL    string `json:"canonicalRevisionUrl"`
	FetchedAt               string `json:"fetchedAt"`
	CategoriesJSON          string `json:"categoriesJson"`
	Section                 string `json:"section"`
	CompositionRenditionKey string `json:"compositionRenditionKey,omitempty"`
	VersionReason           string `json:"versionReason,omitempty"`
	IndexEvidenceRefsJSON   string `json:"indexEvidenceRefsJson"`
	FixedIdentityJSON       string `json:"fixedIdentityJson"`
	FixedIdentitySHA256     string `json:"fixedIdentitySha256"`
	RawByteCount            int    `json:"rawByteCount"`
	RawWikitextSHA256       string `json:"rawWikitextSha256"`
	ArtifactSHA256          string `json:"artifactSha256"`
	CreatedAt               int64  `json:"createdAt"`
}

type LyricsRecoveryArtifactEvidenceBackupRecord struct {
	BatchSHA256  string `json:"batchSha256"`
	MusicID      int    `json:"musicId"`
	RenditionKey string `json:"renditionKey"`
	Position     int    `json:"position"`
	Provider     string `json:"provider"`
	EvidenceID   string `json:"evidenceId"`
	SHA256       string `json:"sha256"`
}

type LyricsRecoveryContributionBackupRecord struct {
	BatchSHA256        string `json:"batchSha256"`
	MusicID            int    `json:"musicId"`
	Component          string `json:"component"`
	RenditionKey       string `json:"renditionKey"`
	ContributionSHA256 string `json:"contributionSha256"`
}

type LyricsAvailabilityDocumentBackupRecord struct {
	AvailabilityDocumentID int64  `json:"availabilityDocumentId"`
	BatchSHA256            string `json:"batchSha256"`
	MusicID                int    `json:"musicId"`
	SchemaVersion          int    `json:"schemaVersion"`
	State                  string `json:"state"`
	ReasonCode             string `json:"reasonCode"`
	NoLyricsReason         string `json:"noLyricsReason"`
	DocumentJSON           string `json:"documentJson"`
	DocumentSHA256         string `json:"documentSha256"`
	ResultSHA256           string `json:"resultSha256"`
	CreatedAt              int64  `json:"createdAt"`
}

func exportRecoveryLyricsContentTx(ctx context.Context, tx *sql.Tx, result *LyricsContentExport) error {
	queries := []struct {
		query string
		scan  func(*sql.Rows) error
	}{
		{`SELECT batch_sha256,schema_version,root_schema_version,root_id,root_sha256,catalog_count,
			music_ids_sha256,coverage_json,evidence_receipt_sha256,pack_sha256,selection_sha256,evidence_count,
			shard_count,raw_byte_count,encoded_byte_count,actor,created_at
			FROM lyrics_recovery_import_batches ORDER BY batch_sha256`, func(rows *sql.Rows) error {
			var record LyricsRecoveryBatchBackupRecord
			if err := rows.Scan(&record.BatchSHA256, &record.SchemaVersion, &record.RootSchemaVersion, &record.RootID,
				&record.RootSHA256, &record.CatalogCount, &record.MusicIDsSHA256, &record.CoverageJSON,
				&record.EvidenceReceiptSHA256, &record.PackSHA256, &record.SelectionSHA256, &record.EvidenceCount,
				&record.ShardCount, &record.RawByteCount, &record.EncodedByteCount, &record.Actor, &record.CreatedAt); err != nil {
				return err
			}
			result.RecoveryBatches = append(result.RecoveryBatches, record)
			return nil
		}},
		{`SELECT batch_sha256,music_id,japanese_title,catalog_fingerprint,target_music_id,
			association_music_ids_json,state,result_sha256,draft_sha256,document_sha256,
			availability_document_sha256,created_at
			FROM lyrics_recovery_import_items ORDER BY batch_sha256,music_id`, func(rows *sql.Rows) error {
			var record LyricsRecoveryItemBackupRecord
			if err := rows.Scan(&record.BatchSHA256, &record.MusicID, &record.JapaneseTitle,
				&record.CatalogFingerprint, &record.TargetMusicID, &record.AssociationMusicIDsJSON,
				&record.State, &record.ResultSHA256, &record.DraftSHA256, &record.DocumentSHA256,
				&record.AvailabilityDocumentSHA256, &record.CreatedAt); err != nil {
				return err
			}
			result.RecoveryItems = append(result.RecoveryItems, record)
			return nil
		}},
		{`SELECT evidence.provider,evidence.evidence_id,evidence.sha256,evidence.acquisition_id,evidence.envelope_sha256,
			evidence.kind,evidence.origin,evidence.page_id,evidence.revision_id,evidence.revision_timestamp,
			evidence.mediawiki_sha1,evidence.page_title,evidence.canonical_revision_url,evidence.categories_json,
			evidence.canonical_request_url,evidence.fetched_at,evidence.raw_bytes,evidence.raw_byte_count,
			evidence.raw_sha256,evidence.created_at
			FROM lyrics_recovery_source_evidence AS evidence
			WHERE EXISTS (SELECT 1 FROM lyrics_recovery_import_artifact_evidence AS link
			 WHERE link.provider=evidence.provider AND link.evidence_id=evidence.evidence_id AND link.sha256=evidence.sha256)
			ORDER BY evidence.provider,evidence.evidence_id`, func(rows *sql.Rows) error {
			var record LyricsRecoverySourceEvidenceBackupRecord
			var pageID, revisionID sql.NullInt64
			if err := rows.Scan(&record.Provider, &record.EvidenceID, &record.SHA256, &record.AcquisitionID,
				&record.EnvelopeSHA256, &record.Kind, &record.Origin, &pageID, &revisionID,
				&record.RevisionTimestamp, &record.MediaWikiSHA1, &record.PageTitle, &record.CanonicalRevisionURL,
				&record.CategoriesJSON, &record.CanonicalRequestURL, &record.FetchedAt, &record.RawBytes,
				&record.RawByteCount, &record.RawSHA256, &record.CreatedAt); err != nil {
				return err
			}
			if pageID.Valid {
				record.PageID = int(pageID.Int64)
			}
			if revisionID.Valid {
				record.RevisionID = int(revisionID.Int64)
			}
			record.RawBytes = append([]byte(nil), record.RawBytes...)
			result.RecoverySourceEvidence = append(result.RecoverySourceEvidence, record)
			return nil
		}},
		{`SELECT batch_sha256,music_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,
			mediawiki_sha1,page_title,canonical_revision_url,fetched_at,categories_json,section,
			composition_rendition_key,version_reason,index_evidence_refs_json,fixed_identity_json,
			fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256,created_at
			FROM lyrics_recovery_import_artifacts ORDER BY batch_sha256,music_id,rendition_key`, func(rows *sql.Rows) error {
			var record LyricsRecoveryArtifactBackupRecord
			if err := rows.Scan(&record.BatchSHA256, &record.MusicID, &record.Provider, &record.RenditionKey,
				&record.Origin, &record.PageID, &record.RevisionID, &record.RevisionTimestamp, &record.MediaWikiSHA1,
				&record.PageTitle, &record.CanonicalRevisionURL, &record.FetchedAt, &record.CategoriesJSON,
				&record.Section, &record.CompositionRenditionKey, &record.VersionReason, &record.IndexEvidenceRefsJSON,
				&record.FixedIdentityJSON, &record.FixedIdentitySHA256, &record.RawByteCount,
				&record.RawWikitextSHA256, &record.ArtifactSHA256, &record.CreatedAt); err != nil {
				return err
			}
			result.RecoveryArtifacts = append(result.RecoveryArtifacts, record)
			return nil
		}},
		{`SELECT batch_sha256,music_id,rendition_key,position,provider,evidence_id,sha256
			FROM lyrics_recovery_import_artifact_evidence ORDER BY batch_sha256,music_id,rendition_key,position`, func(rows *sql.Rows) error {
			var record LyricsRecoveryArtifactEvidenceBackupRecord
			if err := rows.Scan(&record.BatchSHA256, &record.MusicID, &record.RenditionKey, &record.Position,
				&record.Provider, &record.EvidenceID, &record.SHA256); err != nil {
				return err
			}
			result.RecoveryArtifactEvidence = append(result.RecoveryArtifactEvidence, record)
			return nil
		}},
		{`SELECT batch_sha256,music_id,component,rendition_key,contribution_sha256
			FROM lyrics_recovery_import_component_contributions ORDER BY batch_sha256,music_id,component`, func(rows *sql.Rows) error {
			var record LyricsRecoveryContributionBackupRecord
			if err := rows.Scan(&record.BatchSHA256, &record.MusicID, &record.Component,
				&record.RenditionKey, &record.ContributionSHA256); err != nil {
				return err
			}
			result.RecoveryContributions = append(result.RecoveryContributions, record)
			return nil
		}},
		{`SELECT availability_document_id,batch_sha256,music_id,schema_version,state,reason_code,
			no_lyrics_reason,document_json,document_sha256,result_sha256,created_at
			FROM song_lyrics_availability_documents ORDER BY batch_sha256,music_id`, func(rows *sql.Rows) error {
			var record LyricsAvailabilityDocumentBackupRecord
			if err := rows.Scan(&record.AvailabilityDocumentID, &record.BatchSHA256, &record.MusicID,
				&record.SchemaVersion, &record.State, &record.ReasonCode, &record.NoLyricsReason,
				&record.DocumentJSON, &record.DocumentSHA256, &record.ResultSHA256, &record.CreatedAt); err != nil {
				return err
			}
			result.AvailabilityDocuments = append(result.AvailabilityDocuments, record)
			return nil
		}},
	}
	for _, item := range queries {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, item.query)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := item.scan(rows); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}
