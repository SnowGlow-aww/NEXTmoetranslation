package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/lyricsstaging"
	"moesekai/server/internal/model"
)

func TestRenditionV3ContentBackupRoundTripPreservesOrderCreditsAndGraph(t *testing.T) {
	source, translations := setupRenditionV3PersistenceStore(t)
	exported, err := source.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	assertRenditionV3BackupShape(t, exported, translations)

	restored := setupLyricsStore(t)
	if err := restored.ImportTranslationContent(nil, EventContentExport{}, exported); err != nil {
		t.Fatalf("restore v3 content backup: %v", err)
	}
	roundTrip, err := restored.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(roundTrip)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("v3 content backup was not byte-stable: before=%x after=%x", sha256.Sum256(before), sha256.Sum256(after))
	}
	assertRenditionV3BackupShape(t, roundTrip, translations)
}

func TestRenditionV3ContentBackupRejectsExistingNativeSourceV3RestoreTargetWithoutMutation(t *testing.T) {
	destination, _ := setupRenditionV3PersistenceStore(t)
	before, err := destination.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportTranslationContent(nil, EventContentExport{}, before); err == nil ||
		!strings.Contains(err.Error(), "content restore cannot replace immutable source-v3 lyrics") {
		t.Fatalf("existing source-v3 restore error=%v", err)
	}
	after, err := destination.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("rejected source-v3 restore mutated content: before=%x after=%x", sha256.Sum256(beforeJSON), sha256.Sum256(afterJSON))
	}
}

func TestRenditionV3ContentBackupRejectsLocalizationOmissionsDuplicatesAndReordering(t *testing.T) {
	source, _ := setupRenditionV3PersistenceStore(t)
	valid, err := source.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*LyricsContentExport)
		want string
	}{
		{
			name: "missing rendition localization",
			edit: func(content *LyricsContentExport) {
				content.RenditionLocalizations = content.RenditionLocalizations[:1]
			},
			want: "do not cover every rendition",
		},
		{
			name: "missing all rendition localizations",
			edit: func(content *LyricsContentExport) {
				content.RenditionLocalizations = nil
				content.RenditionTranslationLines = nil
			},
			want: "do not match their checksum",
		},
		{
			name: "duplicate rendition localization",
			edit: func(content *LyricsContentExport) {
				content.RenditionLocalizations = append(content.RenditionLocalizations, content.RenditionLocalizations[0])
			},
			want: "is duplicated",
		},
		{
			name: "reordered rendition localizations",
			edit: func(content *LyricsContentExport) {
				content.RenditionLocalizations[0], content.RenditionLocalizations[1] =
					content.RenditionLocalizations[1], content.RenditionLocalizations[0]
			},
			want: "not canonically ordered",
		},
		{
			name: "missing translation line",
			edit: func(content *LyricsContentExport) {
				content.RenditionTranslationLines = content.RenditionTranslationLines[1:]
			},
			want: "incomplete translation lines",
		},
		{
			name: "reordered translation lines",
			edit: func(content *LyricsContentExport) {
				content.RenditionTranslationLines[0], content.RenditionTranslationLines[1] =
					content.RenditionTranslationLines[1], content.RenditionTranslationLines[0]
			},
			want: "not canonically ordered",
		},
		{
			name: "inconsistent localization revision",
			edit: func(content *LyricsContentExport) {
				content.RenditionLocalizations[1].Revision++
			},
			want: "inconsistent revision metadata",
		},
		{
			name: "invalid translation UTF-8",
			edit: func(content *LyricsContentExport) {
				content.RenditionTranslationLines[0].Text = string([]byte{0xff})
			},
			want: "is invalid",
		},
		{
			name: "oversized translation line",
			edit: func(content *LyricsContentExport) {
				content.RenditionTranslationLines[0].Text = strings.Repeat("x", maxLyricsLineTextBytes+1)
			},
			want: "is invalid",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			invalid := cloneLyricsContentExport(t, valid)
			test.edit(&invalid)
			if err := setupLyricsStore(t).ImportTranslationContent(nil, EventContentExport{}, invalid); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("tampered v3 restore error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestRenditionV3ContentBackupRejectsArtifactAndContributionReordering(t *testing.T) {
	source, _ := setupRenditionV3PersistenceStore(t)
	valid, err := source.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*LyricsContentExport)
		want string
	}{
		{
			name: "source artifact order",
			edit: func(content *LyricsContentExport) {
				content.SourceArtifacts[0], content.SourceArtifacts[1] = content.SourceArtifacts[1], content.SourceArtifacts[0]
			},
			want: "canonical fixed-identity order",
		},
		{
			name: "source contribution order",
			edit: func(content *LyricsContentExport) {
				content.SourceContributions[0], content.SourceContributions[1] = content.SourceContributions[1], content.SourceContributions[0]
			},
			want: "canonical component order",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			invalid := cloneLyricsContentExport(t, valid)
			test.edit(&invalid)
			if err := setupLyricsStore(t).ImportTranslationContent(nil, EventContentExport{}, invalid); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("reordered v3 graph restore error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestRenditionV3ContentBackupRejectsDocumentRenditionReorderingAndCrossFamilyMerge(t *testing.T) {
	source, _ := setupRenditionV3PersistenceStore(t)
	valid, err := source.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*model.LyricsSourceDocument)
		want   string
	}{
		{
			name: "document rendition order",
			mutate: func(document *model.LyricsSourceDocument) {
				document.Renditions[0], document.Renditions[1] = document.Renditions[1], document.Renditions[0]
			},
			want: "renditions are not strictly ordered",
		},
		{
			name: "fixed identity order",
			mutate: func(document *model.LyricsSourceDocument) {
				document.FixedIdentities[0], document.FixedIdentities[1] = document.FixedIdentities[1], document.FixedIdentities[0]
			},
			want: "fixed identities are not strictly ordered",
		},
		{
			name: "cross family component",
			mutate: func(document *model.LyricsSourceDocument) {
				binding, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
				if err != nil {
					panic(err)
				}
				for index := range document.FixedIdentities {
					if document.FixedIdentities[index].RenditionKey == binding[0].FixedIdentityKey {
						document.FixedIdentities[index].CompositionRenditionKey = binding[len(binding)-1].RenditionKey
						return
					}
				}
			},
			want: "crosses logical rendition families",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			invalid := cloneLyricsContentExport(t, valid)
			var document model.LyricsSourceDocument
			if err := json.Unmarshal([]byte(invalid.SourceDocuments[0].DocumentJSON), &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(&document)
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(body)
			invalid.SourceDocuments[0].DocumentJSON = string(body)
			invalid.SourceDocuments[0].DocumentSHA256 = hex.EncodeToString(digest[:])
			if err := setupLyricsStore(t).ImportTranslationContent(nil, EventContentExport{}, invalid); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("tampered v3 document restore error=%v want substring %q", err, test.want)
			}
		})
	}
}

func setupRenditionV3PersistenceStore(t *testing.T) (*Store, []lyricsstaging.RenditionTranslation) {
	t.Helper()
	s := setupLyricsStore(t)
	document, evidenceByIdentity := renditionV3PersistenceDocument(t)
	translations := []lyricsstaging.RenditionTranslation{
		{
			RenditionKey:       document.Renditions[0].RenditionKey,
			Translations:       []string{"sekai-translation-1", "sekai-translation-2"},
			TranslationCredit:  "translator-sekai",
			ProofreadingCredit: "proofreader-sekai",
		},
		{
			RenditionKey:       document.Renditions[1].RenditionKey,
			Translations:       []string{"vocaloid-translation-1"},
			TranslationCredit:  "translator-vocaloid",
			ProofreadingCredit: "proofreader-vocaloid",
		},
	}
	if err := insertRenditionV3PersistenceGraph(t, s, document, evidenceByIdentity, translations); err != nil {
		t.Fatal(err)
	}
	return s, translations
}

func renditionV3PersistenceDocument(t *testing.T) (model.LyricsSourceDocument, [][]lyricssource.IndexEvidence) {
	t.Helper()
	_, sekaiV2 := publicLyricsV2SekaiFixture(10)
	sekaiV2.FixedIdentities = append([]model.LyricsSourceFixedIdentity(nil), sekaiV2.FixedIdentities[:2]...)
	sekaiGame := *model.CloneLyricsSourceFull(&sekaiV2.Full)
	for index := range sekaiGame.Lines {
		sekaiGame.Lines[index].ID = fmt.Sprintf("game-%06d", index+1)
	}
	sekaiGameRef := model.LyricsSourceComponentRef{RenditionKey: sekaiV2.FixedIdentities[1].RenditionKey}
	sekaiV2.Game = &sekaiGame
	sekaiV2.GameProjection = nil
	sekaiV2.Provenance.GameProjection = nil
	sekaiV2.Provenance.GameText = &sekaiGameRef
	sekaiV3, err := model.UpconvertLyricsSourceDocumentV2(sekaiV2)
	if err != nil {
		t.Fatalf("up-convert independent Full/Game fixture: %v", err)
	}
	sekaiV3.Renditions[0].SourceTabPaths = []model.LyricsSourceTabPath{{"Full Version", "SEKAI"}, {"Game Version", "SEKAI"}}

	_, vocaloidV2 := publicLyricsV2VirtualSingerFixture(10)
	vocaloidV2.ReasonCode = model.LyricsSourceVersionReasonUntaggedUncutIdentity
	vocaloidV2.GameProjection = &model.LyricsSourceGameProjection{LineIDs: []string{"full-000001"}}
	vocaloidProjectionRef := vocaloidV2.Provenance.FullText
	vocaloidV2.Provenance.GameProjection = &vocaloidProjectionRef
	vocaloidV3, err := model.UpconvertLyricsSourceDocumentV2(vocaloidV2)
	if err != nil {
		t.Fatalf("up-convert exact-projection fixture: %v", err)
	}
	vocaloidV3.Renditions[0].SourceTabPaths = []model.LyricsSourceTabPath{{"Full Version", "VIRTUAL SINGER"}}

	document := model.LyricsSourceDocument{
		SchemaVersion:   model.LyricsSourceDocumentSchemaVersionV3,
		FixedIdentities: append(append([]model.LyricsSourceFixedIdentity{}, sekaiV3.FixedIdentities...), vocaloidV3.FixedIdentities...),
		Renditions:      append(model.CloneLyricsSourceRenditions(sekaiV3.Renditions), vocaloidV3.Renditions...),
	}
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		t.Fatalf("enumerate v3 fixture components: %v", err)
	}
	familyByIdentity := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if previous, exists := familyByIdentity[binding.FixedIdentityKey]; exists && previous != binding.RenditionKey {
			t.Fatalf("fixture identity crosses families: %q", binding.FixedIdentityKey)
		}
		familyByIdentity[binding.FixedIdentityKey] = binding.RenditionKey
	}
	evidenceByKey := make(map[string][]lyricssource.IndexEvidence, len(document.FixedIdentities))
	for index := range document.FixedIdentities {
		family, found := familyByIdentity[document.FixedIdentities[index].RenditionKey]
		if !found {
			t.Fatalf("fixture identity %q has no component", document.FixedIdentities[index].RenditionKey)
		}
		document.FixedIdentities[index].CompositionRenditionKey = family
		identity, parents := contentBackupEvidenceForIdentity(t, document.FixedIdentities[index], index)
		document.FixedIdentities[index] = identity
		evidenceByKey[identity.RenditionKey] = parents
	}
	sort.Slice(document.FixedIdentities, func(left, right int) bool {
		return document.FixedIdentities[left].RenditionKey < document.FixedIdentities[right].RenditionKey
	})
	if err := model.ValidateLyricsSourceDocument(document); err != nil {
		t.Fatalf("validate v3 persistence fixture: %v", err)
	}
	if err := validateStoreV3DocumentGraph(document); err != nil {
		t.Fatalf("validate store v3 persistence fixture: %v", err)
	}
	evidenceByIdentity := make([][]lyricssource.IndexEvidence, len(document.FixedIdentities))
	for index, identity := range document.FixedIdentities {
		evidenceByIdentity[index] = evidenceByKey[identity.RenditionKey]
	}
	return document, evidenceByIdentity
}

func insertRenditionV3PersistenceGraph(t *testing.T, s *Store, document model.LyricsSourceDocument,
	evidenceByIdentity [][]lyricssource.IndexEvidence, translations []lyricsstaging.RenditionTranslation,
) error {
	t.Helper()
	ctx := context.Background()
	documentJSON, err := json.Marshal(document)
	if err != nil {
		return err
	}
	documentDigest := sha256.Sum256(documentJSON)
	documentSHA := hex.EncodeToString(documentDigest[:])
	createdAt := time.Date(2026, time.July, 31, 15, 0, 0, 0, time.UTC)
	result, err := s.db.Exec(`INSERT INTO song_lyrics_source_documents
		(music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at)
		VALUES (?,?,?,?,?,?,?)`, 10, document.SchemaVersion, document.ReasonCode, string(documentJSON), documentSHA,
		strings.Repeat("b", 64), createdAt.Unix())
	if err != nil {
		return err
	}
	documentID, err := result.LastInsertId()
	if err != nil {
		return err
	}

	for index, identity := range document.FixedIdentities {
		identityJSON, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		identityDigest := sha256.Sum256(identityJSON)
		categoriesJSON, err := json.Marshal(identity.Categories)
		if err != nil {
			return err
		}
		evidenceJSON, err := json.Marshal(identity.IndexEvidenceRefs)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`INSERT INTO song_lyrics_source_artifacts
			(document_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
			 canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
			 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, documentID, identity.Provider, identity.RenditionKey,
			identity.Origin, identity.PageID, identity.RevisionID, identity.RevisionTimestamp, identity.SHA1, identity.Title,
			identity.CanonicalURL, identity.FetchedAt, string(categoriesJSON), identity.Section, identity.CompositionRenditionKey,
			identity.VersionReason, string(evidenceJSON), string(identityJSON), hex.EncodeToString(identityDigest[:]), 1,
			strings.Repeat(fmt.Sprintf("%x", index+1), 64), strings.Repeat(fmt.Sprintf("%x", index+1), 64)); err != nil {
			return err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	evidenceOffset := 0
	for index, parents := range evidenceByIdentity {
		identity := document.FixedIdentities[index]
		for position, parent := range parents {
			if err := insertOrVerifyLyricsIndexEvidenceTx(ctx, tx, parent, createdAt.Add(time.Duration(evidenceOffset)*time.Millisecond)); err != nil {
				return rollback(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyrics_source_artifact_index_evidence
				(document_id,rendition_key,position,provider,evidence_id,sha256) VALUES (?,?,?,?,?,?)`, documentID,
				identity.RenditionKey, position, identity.Provider, parent.EvidenceID, parent.SHA256); err != nil {
				return rollback(err)
			}
			evidenceOffset++
		}
	}
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		return rollback(err)
	}
	for _, binding := range bindings {
		digest := sha256.Sum256([]byte(documentSHA + "\x00" + binding.ComponentKey + "\x00" + binding.FixedIdentityKey))
		if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyrics_component_contributions
			(document_id,component,rendition_key,contribution_sha256) VALUES (?,?,?,?)`, documentID,
			binding.ComponentKey, binding.FixedIdentityKey, hex.EncodeToString(digest[:])); err != nil {
			return rollback(err)
		}
	}
	if err := insertLyricsRenditionLocalizationsTx(ctx, tx, documentID, document, translations, "rendition-v3-test", createdAt.Unix()); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func assertRenditionV3BackupShape(t *testing.T, content LyricsContentExport, translations []lyricsstaging.RenditionTranslation) {
	t.Helper()
	if len(content.SourceDocuments) != 1 || len(content.SourceArtifacts) != 4 || len(content.SourceContributions) == 0 ||
		len(content.RenditionLocalizations) != len(translations) || len(content.RenditionTranslationLines) != 3 {
		t.Fatalf("v3 backup graph sizes documents=%d artifacts=%d contributions=%d localizations=%d lines=%d",
			len(content.SourceDocuments), len(content.SourceArtifacts), len(content.SourceContributions),
			len(content.RenditionLocalizations), len(content.RenditionTranslationLines))
	}
	var document model.LyricsSourceDocument
	if err := json.Unmarshal([]byte(content.SourceDocuments[0].DocumentJSON), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Renditions) != 2 || document.Renditions[0].RenditionKey != translations[0].RenditionKey ||
		document.Renditions[1].RenditionKey != translations[1].RenditionKey ||
		content.RenditionLocalizations[0].RenditionKey != translations[0].RenditionKey ||
		content.RenditionLocalizations[1].RenditionKey != translations[1].RenditionKey {
		t.Fatalf("v3 rendition order document=%+v localizations=%+v", document.Renditions, content.RenditionLocalizations)
	}
	for index, expected := range translations {
		actual := content.RenditionLocalizations[index]
		if actual.TranslationCredit != expected.TranslationCredit || actual.ProofreadingCredit != expected.ProofreadingCredit {
			t.Fatalf("v3 rendition %q credits=%q/%q want=%q/%q", expected.RenditionKey, actual.TranslationCredit,
				actual.ProofreadingCredit, expected.TranslationCredit, expected.ProofreadingCredit)
		}
	}
	for index := 1; index < len(content.RenditionTranslationLines); index++ {
		left, right := content.RenditionTranslationLines[index-1], content.RenditionTranslationLines[index]
		if left.DocumentID > right.DocumentID || left.DocumentID == right.DocumentID &&
			(left.RenditionKey > right.RenditionKey || left.RenditionKey == right.RenditionKey && left.Position >= right.Position) {
			t.Fatalf("v3 translation lines are not ordered: %+v", content.RenditionTranslationLines)
		}
	}
}
