package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"moesekai/server/internal/lyricssource"
)

type checkpointFixture struct {
	path          string
	sha256        string
	evidenceCount int
	privateTokens []string
}

func createCheckpointFixture(t *testing.T, evidenceCount int) checkpointFixture {
	t.Helper()
	if evidenceCount <= 0 {
		t.Fatal("fixture requires evidence")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "checkpoint.sqlite")
	reservation, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Chmod(0o600); err != nil {
		reservation.Close()
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`PRAGMA page_size=4096`, `PRAGMA journal_mode=DELETE`, `PRAGMA synchronous=FULL`,
		fmt.Sprintf(`PRAGMA application_id=%d`, sourceApplicationID),
		fmt.Sprintf(`PRAGMA user_version=%d`, sourceCheckpointSchemaVersion),
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, definition := range sourceSchemaDefinitions {
		if _, err := database.Exec(definition.sql); err != nil {
			t.Fatalf("create fixture schema %s: %v", definition.name, err)
		}
	}
	executionBody, err := json.Marshal(executionBinding{
		SchemaVersion: 1, Concurrency: 2, MaxAttempts: 3,
		RequestTimeoutNanoseconds: 1_000_000_000, RetryDelayNanoseconds: 1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalogFingerprint := sha256Hex([]byte("fixture-catalog"))
	if _, err := database.Exec(`INSERT INTO checkpoint_metadata(singleton,checkpoint_schema_version,report_schema_version,
		catalog_schema_version,catalog_count,catalog_fingerprint,generated_at,execution_options_json,execution_options_sha256)
		VALUES (1,2,1,18,1,?,'2026-08-01T12:00:00Z',?,?)`,
		catalogFingerprint, string(executionBody), sha256Hex(executionBody)); err != nil {
		t.Fatal(err)
	}
	targetBody := []byte(`{"musicId":1}`)
	resultBody := []byte(`{"musicId":1}`)
	if _, err := database.Exec(`INSERT INTO catalog_targets(music_id,target_kind,target_json,target_sha256)
		VALUES (1,'provider_work',?,?)`, targetBody, sha256Hex(targetBody)); err != nil {
		t.Fatal(err)
	}

	privateTokens := []string{"PRIVATE_TITLE_SENTINEL", "PRIVATE_RAW_SENTINEL", "PRIVATE_ROMANIZATION_SENTINEL"}
	type encodedEvidence struct {
		envelope lyricssource.IndexEvidence
		body     []byte
		sha      string
	}
	encoded := make([]encodedEvidence, evidenceCount)
	var evidenceRawBytes, evidenceJSONBytes, evidenceReceiptBytes int64
	for index := 0; index < evidenceCount; index++ {
		raw := []byte(fmt.Sprintf("%s-%d-%s", privateTokens[1], index, privateTokens[2]))
		rawSHA1 := sha1.Sum(raw)
		rawSHA256 := sha256.Sum256(raw)
		rawSHA256Hex := hex.EncodeToString(rawSHA256[:])
		fetchedAt := fmt.Sprintf("2026-08-01T12:00:%02dZ", index)
		pageID := index + 10
		revisionID := index + 100
		title := fmt.Sprintf("%s_%d", privateTokens[0], index)
		canonicalURL := fmt.Sprintf("https://vocaloid.fandom.com/wiki/%s?oldid=%d", title, revisionID)
		evidenceID := lyricssource.MediaWikiRevisionAcquisitionEvidenceID(
			lyricssource.ProviderVocaloidFandom, fmt.Sprintf("fetch:vocaloid-fandom:%d", pageID), fetchedAt, rawSHA256Hex,
		)
		envelope := lyricssource.IndexEvidence{
			EvidenceID: evidenceID, SHA256: rawSHA256Hex,
			Kind: lyricssource.IndexEvidenceKindMediaWikiRevision, Provider: lyricssource.ProviderVocaloidFandom,
			Origin: lyricssource.OriginVocaloidFandom, PageID: pageID, RevisionID: revisionID,
			MediaWikiSHA1: hex.EncodeToString(rawSHA1[:]), Title: title, CanonicalURL: canonicalURL,
			Categories: []string{}, FetchedAt: fetchedAt, Raw: raw, RawSHA256: rawSHA256Hex,
		}
		if err := lyricssource.ValidateIndexEvidenceEnvelope(envelope); err != nil {
			t.Fatalf("validate fixture evidence: %v", err)
		}
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		encoded[index] = encodedEvidence{envelope: envelope, body: body, sha: sha256Hex(body)}
		evidenceRawBytes += int64(len(raw))
		evidenceJSONBytes += int64(len(body))
		contribution, err := receiptEvidenceItemBytes(body)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			evidenceReceiptBytes = int64(len(evidenceReceiptPrefix) + len(evidenceReceiptSuffix))
		} else {
			evidenceReceiptBytes++
		}
		evidenceReceiptBytes += contribution
	}
	if _, err := database.Exec(`INSERT INTO results(music_id,class,result_json,result_sha256,evidence_item_count,evidence_raw_bytes)
		VALUES (1,'unique_complete',?,?,?,?)`, resultBody, sha256Hex(resultBody), evidenceCount, evidenceRawBytes); err != nil {
		t.Fatal(err)
	}
	for _, item := range encoded {
		if _, err := database.Exec(`INSERT INTO evidence(evidence_id,evidence_json,evidence_sha256,raw_byte_count) VALUES (?,?,?,?)`,
			item.envelope.EvidenceID, item.body, item.sha, len(item.envelope.Raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO result_evidence(music_id,evidence_id) VALUES (1,?)`, item.envelope.EvidenceID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO checkpoint_counters(singleton,catalog_review,game_size_evidence,unique_complete,
		ambiguous,missing,incomplete,error,completed,result_json_bytes,evidence_items,evidence_raw_bytes,evidence_json_bytes,evidence_receipt_bytes)
		VALUES (1,0,0,1,0,0,0,0,1,?,?,?,?,?)`, len(resultBody), evidenceCount, evidenceRawBytes, evidenceJSONBytes, evidenceReceiptBytes); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return checkpointFixture{path: path, sha256: sha256Hex(body), evidenceCount: evidenceCount, privateTokens: privateTokens}
}

func refreshFixtureSHA(t *testing.T, fixture checkpointFixture) checkpointFixture {
	t.Helper()
	body, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	fixture.sha256 = sha256Hex(body)
	return fixture
}

func mutateCheckpointFixture(t *testing.T, fixture checkpointFixture, statements ...string) checkpointFixture {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("mutate checkpoint: %v", err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return refreshFixtureSHA(t, fixture)
}
