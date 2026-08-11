package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"moesekai/server/internal/legacy"
	"moesekai/server/internal/lyricssource"
)

const (
	sourceApplicationID           = 0x4d4f4550
	sourceCheckpointSchemaVersion = 2
	sourceReportSchemaVersion     = 1
	sourceCatalogSchemaVersion    = 18
	sourcePageSize                = 4096
	maxEvidenceItems              = 65_536
	maxEvidenceRawBytes           = 32 << 20
	maxEvidenceJSONBytes          = 64 << 20
	maxResultJSONBytes            = 8 << 20

	evidenceReceiptPrefix = "{\n  \"schemaVersion\": 1,\n  \"indexEvidence\": ["
	evidenceReceiptSuffix = "\n  ],\n  \"receiptSha256\": \"0000000000000000000000000000000000000000000000000000000000000000\"\n}\n"
)

var canonicalDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type checkpointSummary struct {
	CheckpointSHA256   string `json:"checkpointSha256"`
	CheckpointBytes    int64  `json:"checkpointBytes"`
	CatalogCount       int64  `json:"catalogCount"`
	ResultCount        int64  `json:"resultCount"`
	EvidenceCount      int64  `json:"evidenceCount"`
	EvidenceRawBytes   int64  `json:"evidenceRawBytes"`
	EvidenceJSONBytes  int64  `json:"evidenceJsonBytes"`
	EvidenceRowsSHA256 string `json:"evidenceRowsSha256"`
}

type checkpointCounters struct {
	CatalogReview        int64
	GameSizeEvidence     int64
	UniqueComplete       int64
	Ambiguous            int64
	Missing              int64
	Incomplete           int64
	Error                int64
	Completed            int64
	ResultJSONBytes      int64
	EvidenceItems        int64
	EvidenceRawBytes     int64
	EvidenceJSONBytes    int64
	EvidenceReceiptBytes int64
}

type executionBinding struct {
	SchemaVersion             int   `json:"schemaVersion"`
	Concurrency               int   `json:"concurrency"`
	MaxAttempts               int   `json:"maxAttempts"`
	RequestTimeoutNanoseconds int64 `json:"requestTimeoutNanoseconds"`
	RetryDelayNanoseconds     int64 `json:"retryDelayNanoseconds"`
}

type storedEvidence struct {
	key          string
	body         []byte
	bodySHA256   string
	rawByteCount int64
	envelope     lyricssource.IndexEvidence
}

func (checkpoint *sourceCheckpoint) databaseMainFileMatchesPinnedDescriptor(mainFile string) bool {
	if checkpoint == nil || checkpoint.fileInfo == nil || checkpoint.operationalPath == "" || mainFile == "" ||
		strings.TrimSpace(mainFile) != mainFile || !filepath.IsAbs(mainFile) || filepath.Clean(mainFile) != mainFile {
		return false
	}
	if filepath.Clean(mainFile) == filepath.Clean(checkpoint.operationalPath) {
		return true
	}
	info, err := os.Stat(mainFile)
	return err == nil && info.Mode().IsRegular() && os.SameFile(checkpoint.fileInfo, info)
}

func (checkpoint *sourceCheckpoint) validate(ctx context.Context) (checkpointSummary, error) {
	var summary checkpointSummary
	if checkpoint == nil || checkpoint.database == nil {
		return summary, errors.New("checkpoint database is required")
	}
	if err := checkpoint.verifyFile("before validation"); err != nil {
		return summary, err
	}
	var applicationID, userVersion, pageSize, pageCount int64
	for query, destination := range map[string]*int64{
		`PRAGMA application_id`: &applicationID,
		`PRAGMA user_version`:   &userVersion,
		`PRAGMA page_size`:      &pageSize,
		`PRAGMA page_count`:     &pageCount,
	} {
		if err := checkpoint.database.QueryRowContext(ctx, query).Scan(destination); err != nil {
			return summary, err
		}
	}
	if applicationID != sourceApplicationID || userVersion != sourceCheckpointSchemaVersion || pageSize != sourcePageSize ||
		pageCount <= 0 || pageCount*pageSize != checkpoint.byteCount {
		return summary, errors.New("checkpoint SQLite envelope, version, or page accounting is invalid")
	}
	var queryOnly, foreignKeys, trustedSchema, tempStore int
	for query, destination := range map[string]*int{
		`PRAGMA query_only`:     &queryOnly,
		`PRAGMA foreign_keys`:   &foreignKeys,
		`PRAGMA trusted_schema`: &trustedSchema,
		`PRAGMA temp_store`:     &tempStore,
	} {
		if err := checkpoint.database.QueryRowContext(ctx, query).Scan(destination); err != nil {
			return summary, err
		}
	}
	if queryOnly != 1 || foreignKeys != 1 || trustedSchema != 0 || tempStore != 2 {
		return summary, errors.New("checkpoint SQLite read-only safety pragmas are invalid")
	}
	if err := checkpoint.validateIntegrity(ctx); err != nil {
		return summary, err
	}
	var mainCount, unexpectedAttachments int
	var mainFile string
	if err := checkpoint.database.QueryRowContext(ctx,
		`SELECT COUNT(*),MAX(file) FROM pragma_database_list WHERE name='main'`).Scan(&mainCount, &mainFile); err != nil {
		return summary, err
	}
	if err := checkpoint.database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_database_list WHERE name NOT IN ('main','temp')`).Scan(&unexpectedAttachments); err != nil {
		return summary, err
	}
	if mainCount != 1 || unexpectedAttachments != 0 || !checkpoint.databaseMainFileMatchesPinnedDescriptor(mainFile) {
		return summary, errors.New("checkpoint SQLite handle is not bound only to the pinned read-only descriptor")
	}
	if err := checkpoint.validateSchema(ctx); err != nil {
		return summary, err
	}
	catalogCount, err := checkpoint.validateMetadata(ctx)
	if err != nil {
		return summary, err
	}
	counters, err := checkpoint.readCounters(ctx)
	if err != nil {
		return summary, err
	}
	resultCount, err := checkpoint.validateCatalogResultsAndLinks(ctx, catalogCount, counters)
	if err != nil {
		return summary, err
	}
	evidenceCount, evidenceRawBytes, evidenceJSONBytes, evidenceReceiptBytes, rowsSHA, err := checkpoint.validateEvidenceRows(ctx)
	if err != nil {
		return summary, err
	}
	if counters.EvidenceItems != evidenceCount || counters.EvidenceRawBytes != evidenceRawBytes ||
		counters.EvidenceJSONBytes != evidenceJSONBytes || counters.EvidenceReceiptBytes != evidenceReceiptBytes {
		return summary, errors.New("checkpoint evidence counters do not bind the exact canonical rows")
	}
	if evidenceCount <= 0 || evidenceCount > maxEvidenceItems || evidenceRawBytes <= 0 || evidenceRawBytes > maxEvidenceRawBytes ||
		evidenceJSONBytes <= 0 || evidenceJSONBytes > maxEvidenceJSONBytes || evidenceReceiptBytes <= 0 || evidenceReceiptBytes > maxEvidenceJSONBytes {
		return summary, errors.New("checkpoint evidence capacities are invalid")
	}
	if err := checkpoint.verifyDigest("after validation"); err != nil {
		return summary, err
	}
	return checkpointSummary{
		CheckpointSHA256: checkpoint.sha256, CheckpointBytes: checkpoint.byteCount,
		CatalogCount: catalogCount, ResultCount: resultCount, EvidenceCount: evidenceCount,
		EvidenceRawBytes: evidenceRawBytes, EvidenceJSONBytes: evidenceJSONBytes, EvidenceRowsSHA256: rowsSHA,
	}, nil
}

func (checkpoint *sourceCheckpoint) validateIntegrity(ctx context.Context) error {
	rows, err := checkpoint.database.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("check checkpoint integrity: %w", err)
	}
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			rows.Close()
			return err
		}
		count++
		if result != "ok" {
			rows.Close()
			return errors.New("checkpoint integrity check failed")
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("checkpoint integrity check returned an invalid row count")
	}
	foreignRows, err := checkpoint.database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check checkpoint foreign keys: %w", err)
	}
	if foreignRows.Next() {
		foreignRows.Close()
		return errors.New("checkpoint foreign-key check failed")
	}
	if err := foreignRows.Close(); err != nil {
		return err
	}
	return foreignRows.Err()
}

func (checkpoint *sourceCheckpoint) validateMetadata(ctx context.Context) (int64, error) {
	var schemaVersion, reportVersion, catalogVersion, catalogCount int64
	var catalogFingerprint, generatedAt, executionJSON, executionSHA string
	if err := checkpoint.database.QueryRowContext(ctx, `SELECT checkpoint_schema_version,report_schema_version,
		catalog_schema_version,catalog_count,catalog_fingerprint,generated_at,execution_options_json,execution_options_sha256
		FROM checkpoint_metadata WHERE singleton=1`).Scan(&schemaVersion, &reportVersion, &catalogVersion, &catalogCount,
		&catalogFingerprint, &generatedAt, &executionJSON, &executionSHA); err != nil {
		return 0, fmt.Errorf("read checkpoint metadata: %w", err)
	}
	var rowCount int
	if err := checkpoint.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoint_metadata`).Scan(&rowCount); err != nil {
		return 0, err
	}
	if rowCount != 1 || schemaVersion != sourceCheckpointSchemaVersion || reportVersion != sourceReportSchemaVersion ||
		catalogVersion != sourceCatalogSchemaVersion || catalogCount <= 0 || catalogCount > 100_000 ||
		!canonicalDigest.MatchString(catalogFingerprint) || !canonicalDigest.MatchString(executionSHA) || sha256Hex([]byte(executionJSON)) != executionSHA {
		return 0, errors.New("checkpoint metadata singleton or version binding is invalid")
	}
	var execution executionBinding
	if err := decodeCanonicalJSON([]byte(executionJSON), &execution); err != nil || execution.SchemaVersion != 1 ||
		execution.Concurrency <= 0 || execution.MaxAttempts <= 0 || execution.RequestTimeoutNanoseconds <= 0 || execution.RetryDelayNanoseconds < 0 {
		return 0, errors.New("checkpoint execution binding is invalid")
	}
	parsed, err := time.Parse(time.RFC3339Nano, generatedAt)
	if err != nil || !strings.HasSuffix(generatedAt, "Z") || parsed.UTC().Format(time.RFC3339Nano) != generatedAt {
		return 0, errors.New("checkpoint generated timestamp is invalid")
	}
	return catalogCount, nil
}

func (checkpoint *sourceCheckpoint) readCounters(ctx context.Context) (checkpointCounters, error) {
	var counters checkpointCounters
	err := checkpoint.database.QueryRowContext(ctx, `SELECT catalog_review,game_size_evidence,unique_complete,ambiguous,
		missing,incomplete,error,completed,result_json_bytes,evidence_items,evidence_raw_bytes,evidence_json_bytes,evidence_receipt_bytes
		FROM checkpoint_counters WHERE singleton=1`).Scan(
		&counters.CatalogReview, &counters.GameSizeEvidence, &counters.UniqueComplete, &counters.Ambiguous,
		&counters.Missing, &counters.Incomplete, &counters.Error, &counters.Completed, &counters.ResultJSONBytes,
		&counters.EvidenceItems, &counters.EvidenceRawBytes, &counters.EvidenceJSONBytes, &counters.EvidenceReceiptBytes,
	)
	if err != nil {
		return counters, fmt.Errorf("read checkpoint counters: %w", err)
	}
	var rows int
	if err := checkpoint.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoint_counters`).Scan(&rows); err != nil {
		return counters, err
	}
	if rows != 1 {
		return counters, errors.New("checkpoint counter singleton is invalid")
	}
	return counters, nil
}

func (checkpoint *sourceCheckpoint) validateCatalogResultsAndLinks(
	ctx context.Context,
	catalogCount int64,
	counters checkpointCounters,
) (int64, error) {
	var targetCount int64
	if err := checkpoint.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_targets`).Scan(&targetCount); err != nil {
		return 0, err
	}
	if targetCount != catalogCount {
		return 0, errors.New("checkpoint catalog target count does not match metadata")
	}
	if err := checkpoint.validateJSONDigestRows(ctx, `SELECT target_json,target_sha256 FROM catalog_targets ORDER BY music_id`); err != nil {
		return 0, fmt.Errorf("validate checkpoint catalog targets: %w", err)
	}
	if err := checkpoint.validateJSONDigestRows(ctx, `SELECT result_json,result_sha256 FROM results ORDER BY music_id`); err != nil {
		return 0, fmt.Errorf("validate checkpoint results: %w", err)
	}
	var resultCount, resultBytes int64
	if err := checkpoint.database.QueryRowContext(ctx,
		`SELECT COUNT(*),COALESCE(SUM(length(result_json)),0) FROM results`).Scan(&resultCount, &resultBytes); err != nil {
		return 0, err
	}
	if resultCount != counters.Completed || resultBytes != counters.ResultJSONBytes || resultBytes < 0 || resultBytes > maxResultJSONBytes {
		return 0, errors.New("checkpoint result counters do not bind exact result rows")
	}
	classCounts := map[string]*int64{
		"catalog_review": &counters.CatalogReview, "game_size_evidence": &counters.GameSizeEvidence,
		"unique_complete": &counters.UniqueComplete, "ambiguous": &counters.Ambiguous,
		"missing": &counters.Missing, "incomplete": &counters.Incomplete, "error": &counters.Error,
	}
	actual := make(map[string]int64)
	rows, err := checkpoint.database.QueryContext(ctx, `SELECT class,COUNT(*) FROM results GROUP BY class`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var class string
		var count int64
		if err := rows.Scan(&class, &count); err != nil {
			rows.Close()
			return 0, err
		}
		if _, supported := classCounts[class]; !supported {
			rows.Close()
			return 0, errors.New("checkpoint contains an unsupported result class")
		}
		actual[class] = count
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for class, expected := range classCounts {
		if actual[class] != *expected {
			return 0, errors.New("checkpoint class counters do not bind exact result rows")
		}
	}
	var mismatchedResults, orphanEvidence int64
	if err := checkpoint.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT r.music_id FROM results r LEFT JOIN result_evidence re ON re.music_id=r.music_id
		LEFT JOIN evidence e ON e.evidence_id=re.evidence_id GROUP BY r.music_id
		HAVING COUNT(e.evidence_id) != r.evidence_item_count OR COALESCE(SUM(e.raw_byte_count),0) != r.evidence_raw_bytes
	)`).Scan(&mismatchedResults); err != nil {
		return 0, err
	}
	if err := checkpoint.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence e
		LEFT JOIN result_evidence re ON re.evidence_id=e.evidence_id WHERE re.evidence_id IS NULL`).Scan(&orphanEvidence); err != nil {
		return 0, err
	}
	if mismatchedResults != 0 || orphanEvidence != 0 {
		return 0, errors.New("checkpoint result-to-evidence links are partial or inconsistent")
	}
	return resultCount, nil
}

func (checkpoint *sourceCheckpoint) validateJSONDigestRows(ctx context.Context, query string) error {
	rows, err := checkpoint.database.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	for rows.Next() {
		var body []byte
		var wantSHA string
		if err := rows.Scan(&body, &wantSHA); err != nil {
			rows.Close()
			return err
		}
		if !canonicalDigest.MatchString(wantSHA) || sha256Hex(body) != wantSHA || !json.Valid(body) || bytes.TrimSpace(body) == nil {
			rows.Close()
			return errors.New("checkpoint JSON digest does not bind one valid JSON value")
		}
		if err := legacy.ValidateUniqueJSON(body); err != nil {
			rows.Close()
			return errors.New("checkpoint JSON contains duplicate fields")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, body); err != nil || !bytes.Equal(compact.Bytes(), body) {
			rows.Close()
			return errors.New("checkpoint JSON is not compact canonical transport JSON")
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return rows.Err()
}

func (checkpoint *sourceCheckpoint) validateEvidenceRows(ctx context.Context) (int64, int64, int64, int64, string, error) {
	digest := sha256.New()
	var count, rawBytes, jsonBytes, receiptBytes int64
	err := checkpoint.forEachEvidence(ctx, func(item storedEvidence) error {
		count++
		rawBytes += item.rawByteCount
		jsonBytes += int64(len(item.body))
		contribution, err := receiptEvidenceItemBytes(item.body)
		if err != nil {
			return err
		}
		if count == 1 {
			receiptBytes = int64(len(evidenceReceiptPrefix) + len(evidenceReceiptSuffix))
		} else {
			receiptBytes++
		}
		receiptBytes += contribution
		hashFrame(digest, []byte(item.key))
		hashFrame(digest, item.body)
		hashFrame(digest, []byte(item.bodySHA256))
		var rawCount [8]byte
		binary.BigEndian.PutUint64(rawCount[:], uint64(item.rawByteCount))
		hashFrame(digest, rawCount[:])
		return nil
	})
	if err != nil {
		return 0, 0, 0, 0, "", err
	}
	return count, rawBytes, jsonBytes, receiptBytes, hex.EncodeToString(digest.Sum(nil)), nil
}

func (checkpoint *sourceCheckpoint) forEachEvidence(ctx context.Context, visit func(storedEvidence) error) error {
	if visit == nil {
		return errors.New("evidence visitor is required")
	}
	rows, err := checkpoint.database.QueryContext(ctx,
		`SELECT evidence_id,evidence_json,evidence_sha256,raw_byte_count FROM evidence ORDER BY evidence_id`)
	if err != nil {
		return fmt.Errorf("read checkpoint evidence: %w", err)
	}
	for rows.Next() {
		var item storedEvidence
		if err := rows.Scan(&item.key, &item.body, &item.bodySHA256, &item.rawByteCount); err != nil {
			rows.Close()
			return err
		}
		item.envelope, err = decodeStoredEvidence(item.key, item.body, item.bodySHA256, item.rawByteCount)
		if err != nil {
			rows.Close()
			return err
		}
		if err := visit(item); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return rows.Err()
}

func decodeStoredEvidence(key string, body []byte, wantSHA string, rawByteCount int64) (lyricssource.IndexEvidence, error) {
	var envelope lyricssource.IndexEvidence
	if len(body) == 0 || len(body) > 4<<20 || !canonicalDigest.MatchString(wantSHA) || sha256Hex(body) != wantSHA {
		return envelope, errors.New("checkpoint evidence canonical JSON SHA or byte bound is invalid")
	}
	if err := decodeCanonicalJSON(body, &envelope); err != nil {
		return envelope, fmt.Errorf("decode checkpoint canonical evidence envelope: %w", err)
	}
	if key == "" || envelope.EvidenceID != key {
		return envelope, errors.New("checkpoint evidence key does not match its embedded evidence ID")
	}
	if rawByteCount <= 0 || rawByteCount != int64(len(envelope.Raw)) || rawByteCount > lyricssource.MaxIndexEvidenceRawBytes {
		return envelope, errors.New("checkpoint evidence raw-byte count does not match its exact bytes")
	}
	if !canonicalDigest.MatchString(envelope.RawSHA256) || sha256Hex(envelope.Raw) != envelope.RawSHA256 ||
		envelope.SHA256 != envelope.RawSHA256 {
		return envelope, errors.New("checkpoint evidence raw SHA does not bind its exact bytes")
	}
	if err := lyricssource.ValidateIndexEvidenceEnvelope(envelope); err != nil {
		return envelope, fmt.Errorf("validate checkpoint evidence envelope: %w", err)
	}
	return envelope, nil
}

func decodeCanonicalJSON(body []byte, target any) error {
	if target == nil {
		return errors.New("JSON target is required")
	}
	if err := legacy.ValidateUniqueJSON(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("trailing JSON value")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(body, canonical) {
		return errors.New("JSON is not canonical")
	}
	return nil
}

func receiptEvidenceItemBytes(body []byte) (int64, error) {
	var indented bytes.Buffer
	if err := json.Indent(&indented, body, "    ", "  "); err != nil {
		return 0, errors.New("checkpoint evidence cannot be indented canonically")
	}
	return int64(1 + 4 + indented.Len()), nil
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
