package db

import (
	"database/sql"

	"fmt"

	"strings"
	"testing"
)

type v23ProviderGraphFixture struct {
	provider, origin, canonicalURL, revisionTimestamp string
	compositionRenditionKey, versionReason            string
	jobID, resultID, artifactID, analysisID           int
	reviewID, decisionID, renditionID, documentID     int
	musicID, pageID, revisionID                       int
	sha1, evidenceID, evidenceSHA256                  string
	fixedCandidateJSON, fixedIdentityJSON             string
	rawWikitext, evidenceRaw                          []byte
}

func v23Hex(width, value int) string {
	return fmt.Sprintf("%0*x", width, value)
}

func newV23ProviderGraphFixture(provider string, ordinal int) v23ProviderGraphFixture {
	base := ordinal * 1000
	fixture := v23ProviderGraphFixture{
		provider:                provider,
		compositionRenditionKey: "full-vocaloid",
		versionReason:           "untagged_full_only",
		jobID:                   base + 1,
		resultID:                base + 2,
		artifactID:              base + 3,
		analysisID:              base + 4,
		reviewID:                base + 5,
		decisionID:              base + 6,
		renditionID:             base + 7,
		documentID:              base + 8,
		musicID:                 base + 10,
		pageID:                  base + 11,
		revisionID:              base + 12,
		sha1:                    v23Hex(40, base+13),
		rawWikitext:             []byte(fmt.Sprintf("provider-%s-wikitext-%d", provider, ordinal)),
		evidenceRaw:             []byte(fmt.Sprintf("provider-%s-evidence-%d", provider, ordinal)),
	}
	switch provider {
	case "vocaloid_fandom":
		fixture.origin = "https://vocaloid.fandom.com"
		fixture.canonicalURL = fmt.Sprintf("%s/wiki/Provider_%d?oldid=%d", fixture.origin, ordinal, fixture.revisionID)
	case "moegirl":
		fixture.origin = "https://moegirl.icu"
		fixture.canonicalURL = fmt.Sprintf("%s/index.php?oldid=%d&title=Provider_%d", fixture.origin, fixture.revisionID, ordinal)
	case "sekaipedia":
		fixture.origin = "https://www.sekaipedia.org"
		fixture.canonicalURL = fmt.Sprintf("%s/wiki/Provider_%d?oldid=%d", fixture.origin, ordinal, fixture.revisionID)
		fixture.revisionTimestamp = "2026-07-30T12:34:56.123456789Z"
	default:
		panic("unsupported provider fixture " + provider)
	}
	fixture.evidenceID = fmt.Sprintf("revision:%s:%d:%d", provider, fixture.pageID, fixture.revisionID)
	fixture.evidenceSHA256 = v23Hex(64, base+14)
	revisionTimestampField := ""
	if fixture.revisionTimestamp != "" {
		revisionTimestampField = fmt.Sprintf(",\"revisionTimestamp\":%q", fixture.revisionTimestamp)
	}
	fixture.fixedCandidateJSON = fmt.Sprintf(`{"schemaVersion":1,"candidate":{"provider":%q,"origin":%q,"pageId":%d,"revisionId":%d%s,"sha1":%q,"title":%q,"canonicalUrl":%q,"categories":["Lyrics","Songs"],"section":"Lyrics","renditionKey":"full-sekai-%d","versionReason":"untagged_full_only","indexEvidenceRefs":[{"evidenceId":%q,"sha256":%q}]}}`,
		fixture.provider, fixture.origin, fixture.pageID, fixture.revisionID, revisionTimestampField,
		fixture.sha1, fmt.Sprintf("Provider %d", ordinal), fixture.canonicalURL, ordinal,
		fixture.evidenceID, fixture.evidenceSHA256)
	fixture.fixedIdentityJSON = fmt.Sprintf(`{"provider":%q,"origin":%q,"pageId":%d,"revisionId":%d,"sha1":%q,"title":%q,"canonicalUrl":%q%s,"fetchedAt":"2026-07-30T12:34:57.123456789Z","categories":["Lyrics","Songs"],"section":"Lyrics","renditionKey":"full-sekai-%d","compositionRenditionKey":%q,"versionReason":%q,"indexEvidenceRefs":[{"evidenceId":%q,"sha256":%q}]}`,
		fixture.provider, fixture.origin, fixture.pageID, fixture.revisionID, fixture.sha1,
		fmt.Sprintf("Provider %d", ordinal), fixture.canonicalURL, revisionTimestampField, ordinal,
		fixture.compositionRenditionKey, fixture.versionReason, fixture.evidenceID, fixture.evidenceSHA256)
	return fixture
}

func insertV23ProviderGraphFixture(t *testing.T, database *sql.DB, fixture v23ProviderGraphFixture, schemaVersion int) {
	t.Helper()
	base := fixture.jobID - 1
	categoriesJSON := `["Lyrics","Songs"]`
	refsJSON := fmt.Sprintf(`[{"evidenceId":%q,"sha256":%q}]`, fixture.evidenceID, fixture.evidenceSHA256)
	renditionKey := fmt.Sprintf("full-sekai-%d", base/1000)
	jobFixedIdentityJSON := fixture.fixedIdentityJSON
	if schemaVersion < 23 {
		jobFixedIdentityJSON = strings.Replace(jobFixedIdentityJSON,
			fmt.Sprintf(`,"compositionRenditionKey":%q,"versionReason":%q`, fixture.compositionRenditionKey, fixture.versionReason),
			"", 1)
	}
	title := fmt.Sprintf("Provider %d", base/1000)
	mustExec := func(statement string, args ...any) {
		t.Helper()
		if _, err := database.Exec(statement, args...); err != nil {
			t.Fatalf("insert %s provider graph with %d arguments using %s: %v", fixture.provider, len(args), statement, err)
		}
	}
	mustExec(`INSERT INTO catalog_music(music_id,title_ja) VALUES (?,?)`, fixture.musicID, title)
	mustExec(`INSERT INTO song_lyrics(music_id) VALUES (?)`, fixture.musicID)
	mustExec(`INSERT INTO lyrics_discovery_jobs
		(job_id,idempotency_key,kind,state,music_id,page_id,revision_id,artifact_id,attempts,max_attempts,next_attempt_at,
		 lease_owner,lease_expires_at,last_error_code,created_at,updated_at,completed_at,version,catalog_fingerprint,
		 policy_version,expected_sha1,fixed_candidate_json,provider,fixed_identity_json,provenance_status)
		VALUES (?,?,'fetch_revision','queued',?,?,?,NULL,0,3,1,NULL,NULL,NULL,1,1,NULL,1,?,'matching-v2',?,?,?,?,'complete')`,
		fixture.jobID, v23Hex(64, base+101), fixture.musicID, fixture.pageID, fixture.revisionID,
		v23Hex(64, base+102), fixture.sha1, fixture.fixedCandidateJSON, fixture.provider, jobFixedIdentityJSON)
	mustExec(`INSERT INTO lyrics_discovery_shadow_results
		(result_id,job_id,music_id,catalog_fingerprint,policy_version,outcome,candidate_count,result_json,created_at,provider)
		VALUES (?,?,?,?,?,'candidates_found',1,'{"candidates":[{}]}',2,?)`, fixture.resultID, fixture.jobID,
		fixture.musicID, v23Hex(64, base+102), "matching-v2", fixture.provider)
	mustExec(`INSERT INTO lyrics_source_artifacts
		(artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
		 categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
		 first_creating_job_id,created_at,provider,provenance_status)
		VALUES (?,'mediawiki',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'complete')`, fixture.artifactID, fixture.origin,
		fixture.pageID, fixture.revisionID, title, fixture.canonicalURL, fixture.sha1, categoriesJSON,
		fixture.rawWikitext, len(fixture.rawWikitext), v23Hex(64, base+103), v23Hex(64, base+104),
		int64(3), fixture.jobID, int64(3), fixture.provider)
	mustExec(`INSERT INTO lyrics_source_analyses
		(analysis_id,analysis_key,artifact_id,music_id,catalog_fingerprint,matching_policy_version,
		 restriction_policy_version,extractor_version,match_outcome,restriction_outcome,extraction_outcome,
		 matching_evidence_json,restriction_rule_ids_json,extracted_lines_json,extracted_line_count,
		 extracted_lines_sha256,analysis_sha256,creating_job_id,created_at,selected_version_json,performers_json,
		 ruby_generator_version,provider)
		VALUES (?,?,?,?,?,'matching-v2','restriction-v1','legacy-v1','no_match','unknown','not_run',
		 '[]','[]','[]',0,'',?,?,4,'{}','[]','',?)`, fixture.analysisID, v23Hex(64, base+105),
		fixture.artifactID, fixture.musicID, v23Hex(64, base+102), v23Hex(64, base+106), fixture.jobID, fixture.provider)
	mustExec(`INSERT INTO lyrics_source_review_items
		(review_id,domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,
		 evidence_json,state,identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at,completed_at,provider)
		VALUES (?,?,'artifact_review',?,?,?,'review-v1','manual_review','{}','approved','approved','approved','approved',
		 2,0,5,6,6,?)`, fixture.reviewID, v23Hex(64, base+107), fixture.analysisID, fixture.musicID,
		v23Hex(64, base+102), fixture.provider)
	mustExec(`INSERT INTO lyrics_source_review_decisions
		(decision_id,review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
		 expected_version,result_version,decided_at,provider)
		VALUES (?,?,'overall','approved',NULL,?,'',?,?,1,2,6,?)`, fixture.decisionID, fixture.reviewID,
		fmt.Sprintf("reviewer-%d", base), fmt.Sprintf("provider-review-%016d", base), v23Hex(64, base+108), fixture.provider)
	mustExec(`INSERT INTO lyrics_discovery_job_outputs(job_id,artifact_id,analysis_id,review_id,created_at,provider)
		VALUES (?,?,?,?,7,?)`, fixture.jobID, fixture.artifactID, fixture.analysisID, fixture.reviewID, fixture.provider)
	if schemaVersion >= 23 {
		mustExec(`INSERT INTO lyrics_source_index_evidence
			(provider,evidence_id,sha256,kind,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
			 canonical_revision_url,categories_json,canonical_request_url,fetched_at,raw_bytes,raw_byte_count,
			 raw_sha256,created_at)
			VALUES (?,?,?,'mediawiki_revision',?,?,?,?,?,?,?,?,'','2026-07-30T12:34:57.123456789Z',?,?,?,8)`,
			fixture.provider, fixture.evidenceID, fixture.evidenceSHA256, fixture.origin, fixture.pageID,
			fixture.revisionID, fixture.revisionTimestamp, fixture.sha1, title, fixture.canonicalURL,
			categoriesJSON, fixture.evidenceRaw, len(fixture.evidenceRaw), fixture.evidenceSHA256)
	} else {
		mustExec(`INSERT INTO lyrics_source_index_evidence
			(provider,evidence_id,sha256,kind,origin,page_id,revision_id,mediawiki_sha1,page_title,
			 canonical_revision_url,categories_json,canonical_request_url,fetched_at,raw_bytes,raw_byte_count,
			 raw_sha256,created_at)
			VALUES (?,?,?,'mediawiki_revision',?,?,?,?,?,? ,?,'','2026-07-30T12:34:57.123456789Z',?,?,?,8)`,
			fixture.provider, fixture.evidenceID, fixture.evidenceSHA256, fixture.origin, fixture.pageID,
			fixture.revisionID, fixture.sha1, title, fixture.canonicalURL, categoriesJSON,
			fixture.evidenceRaw, len(fixture.evidenceRaw), fixture.evidenceSHA256)
	}
	mustExec(`INSERT INTO lyrics_discovery_result_index_evidence(result_id,position,provider,evidence_id,sha256)
		VALUES (?,0,?,?,?)`, fixture.resultID, fixture.provider, fixture.evidenceID, fixture.evidenceSHA256)
	mustExec(`INSERT INTO lyrics_discovery_job_index_evidence(job_id,position,provider,evidence_id,sha256,created_at)
		VALUES (?,0,?,?,?,8)`, fixture.jobID, fixture.provider, fixture.evidenceID, fixture.evidenceSHA256)
	mustExec(`INSERT INTO lyrics_source_review_index_evidence(review_id,position,provider,evidence_id,sha256)
		VALUES (?,0,?,?,?)`, fixture.reviewID, fixture.provider, fixture.evidenceID, fixture.evidenceSHA256)
	mustExec(`INSERT INTO lyrics_source_renditions
		(rendition_id,provider,artifact_id,origin,page_id,revision_id,mediawiki_sha1,page_title,canonical_revision_url,
		 fetched_at,categories_json,section,rendition_key,index_evidence_refs_json,fixed_identity_json,
		 fixed_identity_sha256,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,'2026-07-30T12:34:57.123456789Z',?,'Lyrics',?,?,?,?,9)`, fixture.renditionID,
		fixture.provider, fixture.artifactID, fixture.origin, fixture.pageID, fixture.revisionID, fixture.sha1,
		title, fixture.canonicalURL, categoriesJSON, renditionKey, refsJSON, fixture.fixedIdentityJSON,
		v23Hex(64, base+109))
	mustExec(`INSERT INTO lyrics_source_rendition_index_evidence(rendition_id,position,provider,evidence_id,sha256)
		VALUES (?,0,?,?,?)`, fixture.renditionID, fixture.provider, fixture.evidenceID, fixture.evidenceSHA256)
	mustExec(`INSERT INTO lyrics_source_component_contributions
		(analysis_id,component,rendition_id,contribution_sha256,created_at)
		VALUES (?,'full_text',?,?,10)`, fixture.analysisID, fixture.renditionID, v23Hex(64, base+110))
	documentJSON := `{"fixedIdentities":[` + fixture.fixedIdentityJSON + `]}`
	mustExec(`INSERT INTO song_lyrics_source_documents
		(document_id,music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at)
		VALUES (?,?,1,'untagged_full_only',?,?,?,11)`, fixture.documentID, fixture.musicID, documentJSON,
		v23Hex(64, base+111), v23Hex(64, base+112))
	if schemaVersion >= 23 {
		mustExec(`INSERT INTO song_lyrics_source_artifacts
			(document_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
			 canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
			 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256)
			VALUES (?,?,?,?,?,?,?,?,?,?,'2026-07-30T12:34:57.123456789Z',?,'Lyrics',?,?,?,?,?,?,?,?)`,
			fixture.documentID, fixture.provider, renditionKey, fixture.origin, fixture.pageID, fixture.revisionID,
			fixture.revisionTimestamp, fixture.sha1, title, fixture.canonicalURL, categoriesJSON,
			fixture.compositionRenditionKey, fixture.versionReason, refsJSON, fixture.fixedIdentityJSON,
			v23Hex(64, base+109), len(fixture.rawWikitext),
			v23Hex(64, base+103), v23Hex(64, base+104))
	} else {
		mustExec(`INSERT INTO song_lyrics_source_artifacts
			(document_id,provider,rendition_key,origin,page_id,revision_id,mediawiki_sha1,page_title,canonical_revision_url,
			 fetched_at,categories_json,section,index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,
			 raw_byte_count,raw_wikitext_sha256,artifact_sha256)
			VALUES (?,?,?,?,?,?,?,?,?,'2026-07-30T12:34:57.123456789Z',?,'Lyrics',?,?,?,?,?,?)`, fixture.documentID,
			fixture.provider, renditionKey, fixture.origin, fixture.pageID, fixture.revisionID, fixture.sha1, title,
			fixture.canonicalURL, categoriesJSON, refsJSON, fixture.fixedIdentityJSON, v23Hex(64, base+109),
			len(fixture.rawWikitext), v23Hex(64, base+103), v23Hex(64, base+104))
	}
	mustExec(`INSERT INTO song_lyrics_source_artifact_index_evidence
		(document_id,rendition_key,position,provider,evidence_id,sha256) VALUES (?,?,0,?,?,?)`, fixture.documentID,
		renditionKey, fixture.provider, fixture.evidenceID, fixture.evidenceSHA256)
	mustExec(`INSERT INTO song_lyrics_component_contributions
		(document_id,component,rendition_key,contribution_sha256) VALUES (?,'full_text',?,?)`, fixture.documentID,
		renditionKey, v23Hex(64, base+113))
}

var v23SnapshotOrder = map[string]string{
	"lyrics_discovery_jobs":                      "job_id",
	"lyrics_discovery_shadow_results":            "result_id",
	"lyrics_source_artifacts":                    "artifact_id",
	"lyrics_source_analyses":                     "analysis_id",
	"lyrics_source_review_items":                 "review_id",
	"lyrics_source_review_decisions":             "decision_id",
	"lyrics_discovery_job_outputs":               "job_id",
	"lyrics_source_index_evidence":               "provider,evidence_id",
	"lyrics_source_renditions":                   "rendition_id",
	"song_lyrics_source_artifacts":               "document_id,rendition_key",
	"lyrics_discovery_result_index_evidence":     "result_id,position",
	"lyrics_discovery_job_index_evidence":        "job_id,position",
	"lyrics_source_review_index_evidence":        "review_id,position",
	"lyrics_source_rendition_index_evidence":     "rendition_id,position",
	"song_lyrics_source_artifact_index_evidence": "document_id,rendition_key,position",
	"lyrics_source_component_contributions":      "analysis_id,component",
	"song_lyrics_component_contributions":        "document_id,component",
}

func quotedV23Identifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func v23SnapshotColumns(t *testing.T, database *sql.DB) map[string][]string {
	t.Helper()
	result := make(map[string][]string, len(v23SnapshotOrder))
	for table := range v23SnapshotOrder {
		rows, err := database.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result[table] = append(result[table], column)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if len(result[table]) == 0 {
			t.Fatalf("v23 snapshot table %s has no columns", table)
		}
	}
	return result
}

func snapshotV23Rows(t *testing.T, database *sql.DB, columns map[string][]string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(columns))
	for table, tableColumns := range columns {
		expressions := make([]string, len(tableColumns))
		for index, column := range tableColumns {
			expressions[index] = "quote(" + quotedV23Identifier(column) + ")"
		}
		query := fmt.Sprintf("SELECT json_array(%s) FROM %s ORDER BY %s", strings.Join(expressions, ","),
			quotedV23Identifier(table), v23SnapshotOrder[table])
		rows, err := database.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		var encoded []string
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			encoded = append(encoded, row)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		result[table] = strings.Join(encoded, "\n")
	}
	return result
}

func v23RebuiltSchemaObjects(t *testing.T, database *sql.DB) map[string]bool {
	t.Helper()
	rows, err := database.Query(`SELECT type||':'||name FROM sqlite_master
		WHERE type IN ('index','trigger') AND name NOT LIKE 'sqlite_autoindex_%' AND tbl_name IN
		('lyrics_discovery_jobs','lyrics_discovery_shadow_results','lyrics_source_artifacts','lyrics_source_analyses',
		 'lyrics_source_review_items','lyrics_source_review_decisions','lyrics_discovery_job_outputs',
		 'lyrics_source_index_evidence','lyrics_source_renditions','song_lyrics_source_artifacts') ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		result[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
