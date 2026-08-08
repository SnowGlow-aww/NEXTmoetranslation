package db

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestV24RecoveryStorageIsAdditiveToFrozenFullSourceTables(t *testing.T) {
	path := legacyFixtureCopy(t, "v24-additive.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	database := &DB{DB: raw, path: path}
	if err := database.applyMigrations(migrations[:23]); err != nil {
		t.Fatal(err)
	}
	tables := []string{
		"song_lyrics_source_documents",
		"song_lyrics_source_artifacts",
		"song_lyrics_source_artifact_index_evidence",
		"song_lyrics_component_contributions",
	}
	before := make(map[string]string, len(tables))
	for _, table := range tables {
		var definition string
		if err := raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		before[table] = definition
	}
	if err := database.applyMigrations(migrations[23:24]); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var after string
		if err := raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before[table] {
			t.Fatalf("v24 rewrote frozen Full source table %s", table)
		}
	}
	for _, table := range []string{
		"lyrics_recovery_import_batches",
		"lyrics_recovery_import_items",
		"lyrics_recovery_source_evidence",
		"lyrics_recovery_import_artifacts",
		"lyrics_recovery_import_artifact_evidence",
		"lyrics_recovery_import_component_contributions",
		"song_lyrics_availability_documents",
	} {
		var count int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v24 table %s count=%d err=%v", table, count, err)
		}
	}
}

func TestV24RecoveryStorageAcceptsExactPublicAndFailsClosedByState(t *testing.T) {
	database, err := Open(t.TempDir() + "/v24-recovery.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO catalog_music(music_id,title_ja) VALUES (1,'完整曲'),(2,'纯音乐')`); err != nil {
		t.Fatal(err)
	}
	h := func(character string) string { return strings.Repeat(character, 64) }
	coverage := `{"total":2,"complete":1,"satisfiedNoLyrics":1,"catalogReview":0,"gameSizeEvidence":0,"ambiguous":0,"missing":0,"incomplete":0,"failed":0,"providerOutcomeRefCount":1,"selectionRefCount":1,"uniqueAcquisitionCount":1,"uniqueEvidenceCount":1}`
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_batches
		(batch_sha256,schema_version,root_schema_version,root_id,root_sha256,catalog_count,music_ids_sha256,
		 coverage_json,evidence_receipt_sha256,pack_sha256,selection_sha256,evidence_count,shard_count,
		 raw_byte_count,encoded_byte_count,actor,created_at)
		VALUES (?,1,2,'root-v24',?,2,?,?,?,?,?,1,1,100,200,'offline-operator',1)`,
		h("a"), h("b"), h("c"), coverage, h("d"), h("e"), h("f")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_items
		(batch_sha256,music_id,japanese_title,catalog_fingerprint,target_music_id,association_music_ids_json,
		 state,result_sha256,draft_sha256,document_sha256,availability_document_sha256,created_at)
		VALUES (?,1,'完整曲',?,1,'[]','complete',?,?,?,'',1),
		       (?,2,'纯音乐',?,2,'[]','satisfied_no_lyrics',?,'','',?,1)`,
		h("a"), h("1"), h("2"), h("3"), h("4"), h("a"), h("5"), h("6"), h("7")); err != nil {
		t.Fatal(err)
	}

	raw := []byte("<html>exact public evidence</html>")
	rawDigest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_source_evidence
		(provider,evidence_id,sha256,acquisition_id,envelope_sha256,kind,origin,page_id,revision_id,
		 revision_timestamp,mediawiki_sha1,page_title,canonical_revision_url,categories_json,canonical_request_url,
		 fetched_at,raw_bytes,raw_byte_count,raw_sha256,created_at)
		VALUES ('moegirl_public_exact','public:exact:v24',?,?,?,'exact_public_html','https://zh.moegirl.org.cn',
		 649688,8500224,'','','亿年爱恋','https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B','[]',
		 'https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B','2026-08-03T14:58:50.501307Z',?,?,?,1)`,
		rawDigest, h("8"), h("9"), raw, len(raw), rawDigest); err != nil {
		t.Fatal(err)
	}
	identityJSON := fmt.Sprintf(`{"provider":"moegirl_public_exact","origin":"https://zh.moegirl.org.cn","pageId":649688,"revisionId":8500224,"sha1":"%s","title":"亿年爱恋","canonicalUrl":"https://zh.moegirl.org.cn/%%E4%%BA%%BF%%E5%%B9%%B4%%E7%%88%%B1%%E6%%81%%8B","fetchedAt":"2026-08-03T14:58:50.501307Z","categories":[],"section":"Lyrics","renditionKey":"full-vocaloid","compositionRenditionKey":"full-vocaloid","versionReason":"untagged_full_only","indexEvidenceRefs":[{"evidenceId":"public:exact:v24","sha256":"%s"}]}`, strings.Repeat("a", 40), rawDigest)
	identityDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(identityJSON)))
	refsJSON := fmt.Sprintf(`[{"evidenceId":"public:exact:v24","sha256":"%s"}]`, rawDigest)
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_artifacts
		(batch_sha256,music_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,
		 page_title,canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
		 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,
		 artifact_sha256,created_at)
		VALUES (?,1,'moegirl_public_exact','full-vocaloid','https://zh.moegirl.org.cn',649688,8500224,'',?,
		 '亿年爱恋','https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B','2026-08-03T14:58:50.501307Z',
		 '[]','Lyrics','full-vocaloid','untagged_full_only',?,?,?,?,?,?,1)`,
		h("a"), strings.Repeat("a", 40), refsJSON, identityJSON, identityDigest, len(raw), rawDigest, h("0")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_artifact_evidence
		(batch_sha256,music_id,rendition_key,position,provider,evidence_id,sha256)
		VALUES (?,1,'full-vocaloid',0,'moegirl_public_exact','public:exact:v24',?)`, h("a"), rawDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_component_contributions
		(batch_sha256,music_id,component,rendition_key,contribution_sha256)
		VALUES (?,1,'full_text','full-vocaloid',?)`, h("a"), h("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_artifacts
		(batch_sha256,music_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,
		 page_title,canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
		 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,
		 artifact_sha256,created_at)
		SELECT batch_sha256,2,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,
		 page_title,canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
		 index_evidence_refs_json,fixed_identity_json,?,raw_byte_count,raw_wikitext_sha256,?,created_at
		FROM lyrics_recovery_import_artifacts WHERE batch_sha256=? AND music_id=1`, h("b"), h("c"), h("a")); err == nil {
		t.Fatal("text-free recovery item accepted a source artifact")
	}
	availabilityJSON := `{"schemaVersion":1,"state":"satisfied_no_lyrics","noLyricsReason":"catalog_instrumental","fixedIdentities":[],"provenance":{}}`
	if _, err := database.Exec(`INSERT INTO song_lyrics_availability_documents
		(batch_sha256,music_id,schema_version,state,reason_code,no_lyrics_reason,document_json,document_sha256,result_sha256,created_at)
		VALUES (?,2,1,'satisfied_no_lyrics','','catalog_instrumental',?,?,?,1)`, h("a"), availabilityJSON, h("7"), h("6")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE song_lyrics_availability_documents SET no_lyrics_reason='' WHERE music_id=2`); err == nil {
		t.Fatal("availability document allowed mutation")
	}
	if _, err := database.Exec(`DELETE FROM lyrics_recovery_import_batches WHERE batch_sha256=?`, h("a")); err == nil {
		t.Fatal("recovery batch was deletable while its catalog identities existed")
	}
	if _, err := database.Exec(`DELETE FROM catalog_music WHERE music_id IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM lyrics_recovery_import_batches WHERE batch_sha256=?`, h("a")); err != nil {
		t.Fatalf("restore-style graph removal after catalog deletion: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM lyrics_recovery_source_evidence WHERE provider='moegirl_public_exact'`); err != nil {
		t.Fatalf("orphan recovery evidence removal: %v", err)
	}
}

func TestFreshRecoverySchemaAcceptsTaggedGameOnlyAndRejectsUnknownReason(t *testing.T) {
	database, err := Open(t.TempDir() + "/tagged-game-only.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	h := func(character string) string { return strings.Repeat(character, 64) }
	batchSHA := h("a")
	if _, err := database.Exec(`INSERT INTO catalog_music(music_id,title_ja) VALUES (1,'ゲーム版のみ')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_batches
		(batch_sha256,schema_version,root_schema_version,root_id,root_sha256,catalog_count,music_ids_sha256,
		 coverage_json,evidence_receipt_sha256,pack_sha256,selection_sha256,evidence_count,shard_count,
		 raw_byte_count,encoded_byte_count,actor,created_at)
		VALUES (?,1,2,'root-tagged-game-only',?,1,?,'{"total":1}',?,?,?,0,0,0,0,'migration-test',1)`,
		batchSHA, h("b"), h("c"), h("d"), h("e"), h("f")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO lyrics_recovery_import_items
		(batch_sha256,music_id,japanese_title,catalog_fingerprint,target_music_id,association_music_ids_json,
		 state,result_sha256,draft_sha256,document_sha256,availability_document_sha256,created_at)
		VALUES (?,1,'ゲーム版のみ',?,1,'[]','game_only',?,'','',?,1)`, batchSHA, h("1"), h("2"), h("3")); err != nil {
		t.Fatal(err)
	}

	availabilityJSON := `{"schemaVersion":1,"state":"game_only","reasonCode":"tagged_game_only","fixedIdentities":[],"provenance":{}}`
	if _, err := database.Exec(`INSERT INTO song_lyrics_availability_documents
		(batch_sha256,music_id,schema_version,state,reason_code,no_lyrics_reason,document_json,document_sha256,result_sha256,created_at)
		VALUES (?,1,1,'game_only','future_reason','',?,?,?,1)`, batchSHA, availabilityJSON, h("3"), h("2")); err == nil {
		t.Fatal("unknown Game-only availability reason was accepted")
	}
	if _, err := database.Exec(`INSERT INTO song_lyrics_availability_documents
		(batch_sha256,music_id,schema_version,state,reason_code,no_lyrics_reason,document_json,document_sha256,result_sha256,created_at)
		VALUES (?,1,1,'game_only','tagged_game_only','',?,?,?,1)`, batchSHA, availabilityJSON, h("3"), h("2")); err != nil {
		t.Fatalf("tagged_game_only availability: %v", err)
	}

	insertArtifact := func(renditionKey, reason, identitySHA, artifactSHA string) error {
		_, err := database.Exec(`INSERT INTO lyrics_recovery_import_artifacts
			(batch_sha256,music_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,
			 page_title,canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
			 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,
			 artifact_sha256,created_at)
			VALUES (?,1,'vocaloid_fandom',?,'https://vocaloid.fandom.com',10,20,'',?,
			 'Game only','https://vocaloid.fandom.com/wiki/Game_only?oldid=20','2026-08-08T00:00:00Z',
			 '[]','Game Version',?,?,?, ?,?,1,?,?,1)`,
			batchSHA, renditionKey, strings.Repeat("a", 40), renditionKey, reason,
			`[{"evidenceId":"fixed","sha256":"`+h("4")+`"}]`, `{"versionReason":"`+reason+`"}`,
			identitySHA, h("5"), artifactSHA)
		return err
	}
	if err := insertArtifact("game-invalid", "future_reason", h("6"), h("7")); err == nil {
		t.Fatal("unknown recovery artifact version reason was accepted")
	}
	if err := insertArtifact("game-vocaloid", "tagged_game_only", h("8"), h("9")); err != nil {
		t.Fatalf("tagged_game_only recovery artifact: %v", err)
	}

	for _, object := range []struct {
		typeName string
		name     string
	}{
		{typeName: "table", name: "lyrics_recovery_import_artifacts"},
		{typeName: "table", name: "song_lyrics_availability_documents"},
		{typeName: "table", name: "song_lyrics_source_artifacts"},
		{typeName: "view", name: "lyrics_discovery_job_identity_violations"},
		{typeName: "view", name: "lyrics_source_fixed_identity_violations"},
	} {
		var definition string
		if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.typeName, object.name).Scan(&definition); err != nil {
			t.Fatalf("load %s %s definition: %v", object.typeName, object.name, err)
		}
		if !strings.Contains(definition, "'tagged_game_only'") {
			t.Fatalf("%s %s does not carry tagged_game_only closed-set validation", object.typeName, object.name)
		}
	}
}
