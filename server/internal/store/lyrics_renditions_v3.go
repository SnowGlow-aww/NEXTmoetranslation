package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"moesekai/server/internal/lyricsstaging"
	"moesekai/server/internal/model"
)

func storeV3DocumentComponentRefs(document model.LyricsSourceDocument) (map[string]string, error) {
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		refs[binding.ComponentKey] = binding.FixedIdentityKey
	}
	return refs, nil
}

// validateStoreV3DocumentGraph adds the persistence boundary that is stricter
// than the wire contract: staged and backed-up v3 documents must have one
// canonical rendition/fixed-identity order, and every component reference must
// stay inside its owning logical rendition family.
func validateStoreV3DocumentGraph(document model.LyricsSourceDocument) error {
	if document.SchemaVersion != model.LyricsSourceDocumentSchemaVersionV3 {
		return nil
	}
	lastRenditionKey := ""
	for _, rendition := range document.Renditions {
		if lastRenditionKey != "" && rendition.RenditionKey <= lastRenditionKey {
			return errors.New("source v3 renditions are not strictly ordered")
		}
		lastRenditionKey = rendition.RenditionKey
	}
	identities := make(map[string]model.LyricsSourceFixedIdentity, len(document.FixedIdentities))
	lastIdentityKey := ""
	for _, identity := range document.FixedIdentities {
		if lastIdentityKey != "" && identity.RenditionKey <= lastIdentityKey {
			return errors.New("source v3 fixed identities are not strictly ordered")
		}
		lastIdentityKey = identity.RenditionKey
		identities[identity.RenditionKey] = identity
	}
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		identity, found := identities[binding.FixedIdentityKey]
		if !found {
			return fmt.Errorf("source v3 component %q references unknown fixed identity %q", binding.ComponentKey, binding.FixedIdentityKey)
		}
		if model.LyricsSourceCompositionRenditionKey(identity) != binding.RenditionKey {
			return fmt.Errorf("source v3 component %q crosses logical rendition families", binding.ComponentKey)
		}
	}
	return nil
}

func cloneSourceDocumentPtr(document model.LyricsSourceDocument) *model.LyricsSourceDocument {
	copy := document
	copy.FixedIdentities = append([]model.LyricsSourceFixedIdentity(nil), document.FixedIdentities...)
	copy.Renditions = model.CloneLyricsSourceRenditions(document.Renditions)
	return &copy
}

func sourceV3ComponentCount(document model.LyricsSourceDocument) int {
	bindings, err := model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		return 0
	}
	return len(bindings)
}

func insertLyricsRenditionLocalizationsTx(ctx context.Context, tx *sql.Tx, documentID int64, document model.LyricsSourceDocument, translations []lyricsstaging.RenditionTranslation, actor string, now int64) error {
	if document.SchemaVersion != model.LyricsSourceDocumentSchemaVersionV3 {
		return nil
	}
	if err := validateStoreV3DocumentGraph(document); err != nil {
		return err
	}
	if translations == nil {
		return nil
	}
	if len(translations) != len(document.Renditions) {
		return errors.New("v3 localization array does not cover every rendition")
	}
	for index, rendition := range document.Renditions {
		item := translations[index]
		if item.RenditionKey != rendition.RenditionKey {
			return fmt.Errorf("v3 localization record %d is out of canonical rendition order: got %q want %q",
				index, item.RenditionKey, rendition.RenditionKey)
		}
		if item.Translations == nil && (item.TranslationCredit != "" || item.ProofreadingCredit != "") {
			return errors.New("v3 localization credits exist without translations")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyrics_rendition_localizations
			(document_id,rendition_key,locale,translation_credit,proofreading_credit,updated_at,updated_by,revision)
			VALUES (?,?,?,?,?,?,?,?)`, documentID, item.RenditionKey, "zh-CN", item.TranslationCredit,
			item.ProofreadingCredit, now, actor, 1); err != nil {
			return err
		}
		for position, text := range item.Translations {
			if _, err := tx.ExecContext(ctx, `INSERT INTO song_lyrics_rendition_translation_lines
				(document_id,rendition_key,locale,position,text) VALUES (?,?,?,?,?)`, documentID,
				item.RenditionKey, "zh-CN", position, text); err != nil {
				return err
			}
		}
	}
	return nil
}

func exportLyricsRenditionLocalizationsTx(ctx context.Context, tx *sql.Tx, documentID int64, document model.LyricsSourceDocument) ([]lyricsstaging.RenditionTranslation, error) {
	if document.SchemaVersion != model.LyricsSourceDocumentSchemaVersionV3 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT rendition_key,translation_credit,proofreading_credit
		FROM song_lyrics_rendition_localizations WHERE document_id=? AND locale='zh-CN' ORDER BY rendition_key`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]lyricsstaging.RenditionTranslation{}
	expectedKeys := make(map[string]struct{}, len(document.Renditions))
	for _, rendition := range document.Renditions {
		expectedKeys[rendition.RenditionKey] = struct{}{}
	}
	for rows.Next() {
		var item lyricsstaging.RenditionTranslation
		if err := rows.Scan(&item.RenditionKey, &item.TranslationCredit, &item.ProofreadingCredit); err != nil {
			return nil, err
		}
		if _, expected := expectedKeys[item.RenditionKey]; !expected {
			return nil, fmt.Errorf("stored v3 localization has unknown rendition %q", item.RenditionKey)
		}
		if _, duplicate := byKey[item.RenditionKey]; duplicate {
			return nil, fmt.Errorf("stored v3 localization repeats rendition %q", item.RenditionKey)
		}
		byKey[item.RenditionKey] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(byKey) == 0 {
		return nil, nil
	}
	lineRows, err := tx.QueryContext(ctx, `SELECT rendition_key,position,text
		FROM song_lyrics_rendition_translation_lines WHERE document_id=? AND locale='zh-CN' ORDER BY rendition_key,position`, documentID)
	if err != nil {
		return nil, err
	}
	hasLines := make(map[string]bool, len(byKey))
	for lineRows.Next() {
		var key, text string
		var position int
		if err := lineRows.Scan(&key, &position, &text); err != nil {
			lineRows.Close()
			return nil, err
		}
		item, found := byKey[key]
		if !found || position != len(item.Translations) {
			lineRows.Close()
			return nil, errors.New("stored v3 localization lines are incomplete")
		}
		item.Translations = append(item.Translations, text)
		hasLines[key] = true
		byKey[key] = item
	}
	if err := lineRows.Err(); err != nil {
		lineRows.Close()
		return nil, err
	}
	if err := lineRows.Close(); err != nil {
		return nil, err
	}
	result := make([]lyricsstaging.RenditionTranslation, 0, len(document.Renditions))
	for _, rendition := range document.Renditions {
		item, found := byKey[rendition.RenditionKey]
		if !found {
			return nil, fmt.Errorf("stored v3 localization for rendition %q is incomplete", rendition.RenditionKey)
		}
		if hasLines[rendition.RenditionKey] {
			if len(item.Translations) != renditionLineCountForStore(rendition) {
				return nil, fmt.Errorf("stored v3 localization for rendition %q is incomplete", rendition.RenditionKey)
			}
		} else {
			item.Translations = nil
		}
		result = append(result, item)
	}
	return result, nil
}

func renditionLineCountForStore(rendition model.LyricsSourceRendition) int {
	if rendition.Full != nil {
		return len(rendition.Full.Lines)
	}
	if rendition.Game != nil {
		return len(rendition.Game.Lines)
	}
	return 0
}

func v3TranslationsJSON(translations []lyricsstaging.RenditionTranslation) (string, error) {
	if translations == nil {
		return "", nil
	}
	body, err := json.Marshal(translations)
	return string(body), err
}

func v3TranslationsDigest(translations []lyricsstaging.RenditionTranslation) (string, error) {
	body, err := v3TranslationsJSON(translations)
	if err != nil {
		return "", err
	}
	if body == "" {
		return "", nil
	}
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:]), nil
}

func sourceV3RenditionKeys(document model.LyricsSourceDocument) []string {
	keys := make([]string, len(document.Renditions))
	for index, rendition := range document.Renditions {
		keys[index] = rendition.RenditionKey
	}
	sort.Strings(keys)
	return keys
}

func sourceV3TranslationCreditPair(item lyricsstaging.RenditionTranslation) (string, string) {
	return strings.TrimSpace(item.TranslationCredit), strings.TrimSpace(item.ProofreadingCredit)
}

// LyricsRenditionDocument is the authenticated editor envelope for every
// source-v3 document. It deliberately has no manual-publication state:
// recovery Public v3 publication remains a separate, batch-bound operation.
type LyricsRenditionDocument struct {
	MusicID           int                       `json:"musicId"`
	Status            string                    `json:"status"`
	PublishedRevision int                       `json:"publishedRevision,omitempty"`
	Revision          int                       `json:"revision"`
	UpdatedAt         string                    `json:"updatedAt"`
	Renditions        []PublicLyricsV3Rendition `json:"renditions"`
}

type LyricsRenditionContractError struct {
	Code    string
	Details []string
	Current *LyricsRenditionDocument
}

func (e *LyricsRenditionContractError) Error() string { return e.Code }

type lyricsRenditionLocalizationState struct {
	HasRows      bool
	Revision     int
	UpdatedAt    int64
	Translations []lyricsstaging.RenditionTranslation
}

type lyricsRenditionEditorBundle struct {
	musicID             int
	documentID          int64
	documentSHA         string
	manifestBatchSHA256 string
	recoveryProvenance  bool
	createdAt           int64
	document            model.LyricsSourceDocument
	bindings            []model.LyricsSourceRenditionComponentBinding
	identities          map[string]model.LyricsSourceFixedIdentity
	contributions       map[string]string
	localization        lyricsRenditionLocalizationState
}

// GetLyricsDocument keeps the legacy response shape for legacy source
// documents and uses the authenticated plural envelope for every source-v3
// document. A source-v3 document must never silently fall back to a second
// mutable SongLyrics store.
func (s *Store) GetLyricsDocument(musicID int) (any, error) {
	plural, pluralErr := s.GetLyricsRenditionDocument(musicID)
	if pluralErr == nil {
		return plural, nil
	}
	if !errors.Is(pluralErr, ErrLyricsNotFound) {
		return nil, pluralErr
	}
	legacy, legacyErr := s.GetLyrics(musicID)
	if legacyErr != nil {
		return nil, legacyErr
	}
	return legacy, nil
}

func (s *Store) GetLyricsRenditionDocument(musicID int) (LyricsRenditionDocument, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LyricsRenditionDocument{}, err
	}
	defer tx.Rollback()
	bundle, err := loadLyricsRenditionEditorBundle(tx, musicID)
	if err != nil {
		return LyricsRenditionDocument{}, err
	}
	result, err := buildLyricsRenditionEditorDocument(bundle, bundle.localization)
	if err != nil {
		return LyricsRenditionDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return LyricsRenditionDocument{}, err
	}
	return result, nil
}

// resolveLyricsRenditionEditorProvenance selects exactly one provenance graph.
// A source document whose batch is a recovery batch must have an exact
// recovery item/document owner and no legacy rows; a non-recovery batch must
// not be claimed by any recovery graph before the legacy tables are read.
func resolveLyricsRenditionEditorProvenance(q queryRower, bundle lyricsRenditionEditorBundle) (bool, error) {
	var recoveryBatchCount int
	if err := q.QueryRow(`SELECT COUNT(*) FROM lyrics_recovery_import_batches WHERE batch_sha256=?`,
		bundle.manifestBatchSHA256).Scan(&recoveryBatchCount); err != nil {
		return false, err
	}
	if recoveryBatchCount > 1 {
		return false, errors.New("source v3 editor recovery batch identity is duplicated")
	}
	if recoveryBatchCount == 1 {
		var state, draftSHA, documentSHA string
		if err := q.QueryRow(`SELECT state,draft_sha256,document_sha256
			FROM lyrics_recovery_import_items WHERE batch_sha256=? AND music_id=?`,
			bundle.manifestBatchSHA256, bundle.musicID).Scan(&state, &draftSHA, &documentSHA); err == sql.ErrNoRows {
			return false, errors.New("source v3 editor recovery batch does not own the document music")
		} else if err != nil {
			return false, err
		}
		if state != "complete" && state != "game_only" || draftSHA == "" || documentSHA != bundle.documentSHA {
			return false, errors.New("source v3 editor recovery item does not own the exact document")
		}
		var legacyGraphRows int
		if err := q.QueryRow(`SELECT
			(SELECT COUNT(*) FROM song_lyrics_source_artifacts WHERE document_id=?)+
			(SELECT COUNT(*) FROM song_lyrics_component_contributions WHERE document_id=?)`,
			bundle.documentID, bundle.documentID).Scan(&legacyGraphRows); err != nil {
			return false, err
		}
		if legacyGraphRows != 0 {
			return false, errors.New("source v3 editor document mixes recovery and legacy provenance graphs")
		}
		return true, nil
	}

	var recoveryOwnershipRows int
	if err := q.QueryRow(`SELECT
		(SELECT COUNT(*) FROM lyrics_recovery_import_items WHERE music_id=?)+
		(SELECT COUNT(*) FROM lyrics_recovery_import_artifacts WHERE music_id=?)+
		(SELECT COUNT(*) FROM lyrics_recovery_import_component_contributions WHERE music_id=?)`,
		bundle.musicID, bundle.musicID, bundle.musicID).Scan(&recoveryOwnershipRows); err != nil {
		return false, err
	}
	if recoveryOwnershipRows != 0 {
		return false, errors.New("source v3 editor legacy document is claimed by a recovery provenance graph")
	}
	return false, nil
}

func loadLyricsRenditionEditorBundle(q queryRower, musicID int) (lyricsRenditionEditorBundle, error) {
	bundle := lyricsRenditionEditorBundle{musicID: musicID}
	var schemaVersion int
	var reasonCode, documentJSON string
	if err := q.QueryRow(`SELECT document_id,schema_version,reason_code,document_json,document_sha256,
		manifest_batch_sha256,created_at
		FROM song_lyrics_source_documents AS source
		WHERE source.music_id=? AND source.schema_version=?`,
		musicID, model.LyricsSourceDocumentSchemaVersionV3).Scan(
		&bundle.documentID, &schemaVersion, &reasonCode, &documentJSON, &bundle.documentSHA,
		&bundle.manifestBatchSHA256, &bundle.createdAt,
	); err == sql.ErrNoRows {
		return lyricsRenditionEditorBundle{}, ErrLyricsNotFound
	} else if err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	if musicID <= 0 || bundle.documentID <= 0 || bundle.createdAt <= 0 {
		return lyricsRenditionEditorBundle{}, errors.New("source v3 editor document has invalid persisted identity")
	}
	var legacyRows int
	if err := q.QueryRow(`SELECT COUNT(*) FROM song_lyrics WHERE music_id=?`, musicID).Scan(&legacyRows); err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	if legacyRows != 0 {
		return lyricsRenditionEditorBundle{}, errors.New("source v3 editor document mixes plural localizations with legacy editable lyrics")
	}
	digest := sha256.Sum256([]byte(documentJSON))
	if hex.EncodeToString(digest[:]) != bundle.documentSHA {
		return lyricsRenditionEditorBundle{}, errors.New("source v3 editor document checksum changed")
	}
	document, err := model.DecodeLyricsSourceDocument([]byte(documentJSON))
	if err != nil || document.SchemaVersion != schemaVersion || string(document.ReasonCode) != reasonCode {
		return lyricsRenditionEditorBundle{}, errors.New("source v3 editor document is invalid or stale")
	}
	if err := validateStoreV3DocumentGraph(document); err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	canonicalJSON, err := json.Marshal(document)
	if err != nil || string(canonicalJSON) != documentJSON {
		return lyricsRenditionEditorBundle{}, errors.New("source v3 editor document JSON is not canonical")
	}
	bundle.document = document
	bundle.recoveryProvenance, err = resolveLyricsRenditionEditorProvenance(q, bundle)
	if err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	bundle.bindings, err = model.EnumerateLyricsSourceRenditionComponents(document.Renditions)
	if err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	bundle.identities = make(map[string]model.LyricsSourceFixedIdentity, len(document.FixedIdentities))
	for _, identity := range document.FixedIdentities {
		if _, duplicate := bundle.identities[identity.RenditionKey]; duplicate {
			return lyricsRenditionEditorBundle{}, errors.New("source v3 editor document repeats a fixed identity")
		}
		bundle.identities[identity.RenditionKey] = identity
	}
	artifactQuery := `SELECT provider,rendition_key,origin,page_id,revision_id,revision_timestamp,
		mediawiki_sha1,page_title,canonical_revision_url,fetched_at,categories_json,section,
		composition_rendition_key,version_reason,index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256
		FROM song_lyrics_source_artifacts WHERE document_id=? ORDER BY rendition_key`
	artifactArgs := []any{bundle.documentID}
	if bundle.recoveryProvenance {
		artifactQuery = `SELECT provider,rendition_key,origin,page_id,revision_id,revision_timestamp,
			mediawiki_sha1,page_title,canonical_revision_url,fetched_at,categories_json,section,
			composition_rendition_key,version_reason,index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256
			FROM lyrics_recovery_import_artifacts WHERE batch_sha256=? AND music_id=? ORDER BY rendition_key`
		artifactArgs = []any{bundle.manifestBatchSHA256, bundle.musicID}
	}
	rows, err := q.Query(artifactQuery, artifactArgs...)
	if err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	seenArtifacts := make(map[string]struct{}, len(bundle.identities))
	for rows.Next() {
		var stored model.LyricsSourceFixedIdentity
		var categoriesJSON, evidenceJSON, identityJSON, identitySHA string
		if err := rows.Scan(&stored.Provider, &stored.RenditionKey, &stored.Origin, &stored.PageID, &stored.RevisionID,
			&stored.RevisionTimestamp, &stored.SHA1, &stored.Title, &stored.CanonicalURL, &stored.FetchedAt,
			&categoriesJSON, &stored.Section, &stored.CompositionRenditionKey, &stored.VersionReason,
			&evidenceJSON, &identityJSON, &identitySHA); err != nil {
			rows.Close()
			return lyricsRenditionEditorBundle{}, err
		}
		if err := json.Unmarshal([]byte(categoriesJSON), &stored.Categories); err != nil {
			rows.Close()
			return lyricsRenditionEditorBundle{}, err
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &stored.IndexEvidenceRefs); err != nil {
			rows.Close()
			return lyricsRenditionEditorBundle{}, err
		}
		decoded, err := model.DecodeLyricsSourceFixedIdentity([]byte(identityJSON))
		if err != nil {
			rows.Close()
			return lyricsRenditionEditorBundle{}, err
		}
		canonicalIdentityJSON, err := json.Marshal(decoded)
		identityDigest := sha256.Sum256([]byte(identityJSON))
		expected, found := bundle.identities[stored.RenditionKey]
		if err != nil || !found || !publicLyricsFixedIdentityScalarsMatch(stored, decoded) ||
			!reflect.DeepEqual(decoded, expected) || string(canonicalIdentityJSON) != identityJSON ||
			hex.EncodeToString(identityDigest[:]) != identitySHA {
			rows.Close()
			return lyricsRenditionEditorBundle{}, fmt.Errorf("source v3 editor artifact %q changed", stored.RenditionKey)
		}
		if _, duplicate := seenArtifacts[stored.RenditionKey]; duplicate {
			rows.Close()
			return lyricsRenditionEditorBundle{}, fmt.Errorf("source v3 editor artifact %q is duplicated", stored.RenditionKey)
		}
		seenArtifacts[stored.RenditionKey] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return lyricsRenditionEditorBundle{}, err
	}
	if err := rows.Close(); err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	if len(seenArtifacts) != len(bundle.identities) {
		return lyricsRenditionEditorBundle{}, errors.New("source v3 editor artifacts do not cover every fixed identity")
	}
	expectedRefs, err := storeV3DocumentComponentRefs(document)
	if err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	bundle.contributions = make(map[string]string, len(expectedRefs))
	contributionQuery := `SELECT component,rendition_key,contribution_sha256
		FROM song_lyrics_component_contributions WHERE document_id=? ORDER BY component`
	contributionArgs := []any{bundle.documentID}
	if bundle.recoveryProvenance {
		contributionQuery = `SELECT component,rendition_key,contribution_sha256
			FROM lyrics_recovery_import_component_contributions
			WHERE batch_sha256=? AND music_id=? ORDER BY component`
		contributionArgs = []any{bundle.manifestBatchSHA256, bundle.musicID}
	}
	rows, err = q.Query(contributionQuery, contributionArgs...)
	if err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	for rows.Next() {
		var component, identityKey, contributionSHA string
		if err := rows.Scan(&component, &identityKey, &contributionSHA); err != nil {
			rows.Close()
			return lyricsRenditionEditorBundle{}, err
		}
		expected, found := expectedRefs[component]
		contributionDigest := sha256.Sum256([]byte(bundle.documentSHA + "\x00" + component + "\x00" + identityKey))
		if !found || expected != identityKey || bundle.contributions[component] != "" ||
			hex.EncodeToString(contributionDigest[:]) != contributionSHA {
			rows.Close()
			return lyricsRenditionEditorBundle{}, fmt.Errorf("source v3 editor contribution %q changed", component)
		}
		bundle.contributions[component] = identityKey
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return lyricsRenditionEditorBundle{}, err
	}
	if err := rows.Close(); err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	if len(bundle.contributions) != len(expectedRefs) {
		return lyricsRenditionEditorBundle{}, errors.New("source v3 editor contributions are incomplete")
	}
	localization, err := loadLyricsRenditionLocalizationState(q, bundle.documentID, document)
	if err != nil {
		return lyricsRenditionEditorBundle{}, err
	}
	bundle.localization = localization
	return bundle, nil
}

func loadLyricsRenditionLocalizationState(q queryRower, documentID int64, document model.LyricsSourceDocument) (lyricsRenditionLocalizationState, error) {
	rows, err := q.Query(`SELECT rendition_key,locale,translation_credit,proofreading_credit,updated_at,revision
		FROM song_lyrics_rendition_localizations WHERE document_id=? ORDER BY rendition_key,locale`, documentID)
	if err != nil {
		return lyricsRenditionLocalizationState{}, err
	}
	defer rows.Close()
	byKey := make(map[string]lyricsstaging.RenditionTranslation, len(document.Renditions))
	state := lyricsRenditionLocalizationState{}
	for rows.Next() {
		var item lyricsstaging.RenditionTranslation
		var locale string
		var updatedAt int64
		var revision int
		if err := rows.Scan(&item.RenditionKey, &locale, &item.TranslationCredit, &item.ProofreadingCredit, &updatedAt, &revision); err != nil {
			return lyricsRenditionLocalizationState{}, err
		}
		if locale != "zh-CN" || revision <= 0 || updatedAt <= 0 {
			return lyricsRenditionLocalizationState{}, errors.New("source v3 editor localization metadata is unsupported")
		}
		if _, duplicate := byKey[item.RenditionKey]; duplicate {
			return lyricsRenditionLocalizationState{}, fmt.Errorf("source v3 editor localization %q is duplicated", item.RenditionKey)
		}
		if !state.HasRows {
			state.HasRows, state.Revision, state.UpdatedAt = true, revision, updatedAt
		} else if revision != state.Revision || updatedAt != state.UpdatedAt {
			return lyricsRenditionLocalizationState{}, errors.New("source v3 editor localization revisions are inconsistent")
		}
		byKey[item.RenditionKey] = item
	}
	if err := rows.Err(); err != nil {
		return lyricsRenditionLocalizationState{}, err
	}
	if !state.HasRows {
		state.Revision = 1
		return state, nil
	}
	if len(byKey) != len(document.Renditions) {
		return lyricsRenditionLocalizationState{}, errors.New("source v3 editor localizations do not cover every rendition")
	}
	lineRows, err := q.Query(`SELECT rendition_key,locale,position,text
		FROM song_lyrics_rendition_translation_lines WHERE document_id=? ORDER BY rendition_key,locale,position`, documentID)
	if err != nil {
		return lyricsRenditionLocalizationState{}, err
	}
	linesByKey := make(map[string][]string, len(byKey))
	for lineRows.Next() {
		var key, locale, text string
		var position int
		if err := lineRows.Scan(&key, &locale, &position, &text); err != nil {
			lineRows.Close()
			return lyricsRenditionLocalizationState{}, err
		}
		if locale != "zh-CN" || position != len(linesByKey[key]) {
			lineRows.Close()
			return lyricsRenditionLocalizationState{}, errors.New("source v3 editor translation lines are incomplete")
		}
		if _, found := byKey[key]; !found {
			lineRows.Close()
			return lyricsRenditionLocalizationState{}, fmt.Errorf("source v3 editor translation line has unknown rendition %q", key)
		}
		linesByKey[key] = append(linesByKey[key], text)
	}
	if err := lineRows.Err(); err != nil {
		lineRows.Close()
		return lyricsRenditionLocalizationState{}, err
	}
	if err := lineRows.Close(); err != nil {
		return lyricsRenditionLocalizationState{}, err
	}
	state.Translations = make([]lyricsstaging.RenditionTranslation, 0, len(document.Renditions))
	for _, rendition := range document.Renditions {
		item, found := byKey[rendition.RenditionKey]
		if !found {
			return lyricsRenditionLocalizationState{}, fmt.Errorf("source v3 editor localization %q is missing", rendition.RenditionKey)
		}
		lines := linesByKey[rendition.RenditionKey]
		if len(lines) != 0 && len(lines) != renditionLineCountForStore(rendition) {
			return lyricsRenditionLocalizationState{}, fmt.Errorf("source v3 editor localization %q has incomplete translation lines", rendition.RenditionKey)
		}
		if len(lines) == 0 && (item.TranslationCredit != "" || item.ProofreadingCredit != "") {
			return lyricsRenditionLocalizationState{}, fmt.Errorf("source v3 editor localization %q has credits without translation rows", rendition.RenditionKey)
		}
		item.Translations = append([]string(nil), lines...)
		state.Translations = append(state.Translations, item)
	}
	return state, nil
}

func lyricsRenditionEditorUpdatedAt(bundle lyricsRenditionEditorBundle, localization lyricsRenditionLocalizationState) int64 {
	if localization.HasRows {
		return localization.UpdatedAt
	}
	return bundle.createdAt
}

func buildLyricsRenditionEditorDocument(bundle lyricsRenditionEditorBundle, localization lyricsRenditionLocalizationState) (LyricsRenditionDocument, error) {
	updatedAt := lyricsRenditionEditorUpdatedAt(bundle, localization)
	result := LyricsRenditionDocument{
		MusicID: bundle.musicID, Status: "draft", Revision: localization.Revision,
		UpdatedAt: formatTimestamp(updatedAt), Renditions: make([]PublicLyricsV3Rendition, 0, len(bundle.document.Renditions)),
	}
	localizedByKey := make(map[string]lyricsstaging.RenditionTranslation, len(localization.Translations))
	for _, item := range localization.Translations {
		localizedByKey[item.RenditionKey] = item
	}
	for _, rendition := range bundle.document.Renditions {
		item, localized := localizedByKey[rendition.RenditionKey]
		var localizationRecord *LyricsRenditionLocalizationBackupRecord
		if localization.HasRows {
			if !localized {
				return LyricsRenditionDocument{}, fmt.Errorf("source v3 editor localization %q is missing", rendition.RenditionKey)
			}
			localizationRecord = &LyricsRenditionLocalizationBackupRecord{
				DocumentID: bundle.documentID, RenditionKey: rendition.RenditionKey, Locale: "zh-CN",
				TranslationCredit: item.TranslationCredit, ProofreadingCredit: item.ProofreadingCredit,
				UpdatedAt: localization.UpdatedAt, Revision: localization.Revision,
			}
		}
		publicRendition, err := buildPublicLyricsV3Rendition(
			rendition, item.Translations, localizationRecord, bundle.bindings, bundle.identities, bundle.contributions,
		)
		if err != nil {
			return LyricsRenditionDocument{}, err
		}
		result.Renditions = append(result.Renditions, publicRendition)
	}
	if result.MusicID <= 0 || result.Revision <= 0 || len(result.Renditions) == 0 {
		return LyricsRenditionDocument{}, errors.New("source v3 editor envelope is incomplete")
	}
	return result, nil
}

func (s *Store) SaveLyricsRenditionMutation(input LyricsRenditionDocument, user string) (LyricsRenditionDocument, bool, error) {
	unlock := s.lockLyrics(input.MusicID)
	defer unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	defer tx.Rollback()
	bundle, err := loadLyricsRenditionEditorBundle(tx, input.MusicID)
	if err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	current, err := buildLyricsRenditionEditorDocument(bundle, bundle.localization)
	if err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	if input.Revision != current.Revision {
		copy := current
		return LyricsRenditionDocument{}, false, &LyricsRenditionContractError{Code: "revision_conflict", Current: &copy}
	}
	if err := validateLyricsRenditionImmutableEnvelope(input, current); err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	requested, err := lyricsRenditionEditorTranslations(input, current, bundle.localization.HasRows)
	if err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	stored, err := lyricsRenditionEditorTranslations(current, current, bundle.localization.HasRows)
	if err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	if reflect.DeepEqual(requested, stored) {
		return current, false, nil
	}
	nextRevision := current.Revision + 1
	now := time.Now().Unix()
	if currentUpdatedAt := lyricsRenditionEditorUpdatedAt(bundle, bundle.localization); now < currentUpdatedAt {
		now = currentUpdatedAt
	}
	if _, err := tx.Exec(`DELETE FROM song_lyrics_rendition_translation_lines WHERE document_id=?`, bundle.documentID); err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	for _, item := range requested {
		if _, err := tx.Exec(`INSERT INTO song_lyrics_rendition_localizations
			(document_id,rendition_key,locale,translation_credit,proofreading_credit,updated_at,updated_by,revision)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(document_id,rendition_key,locale) DO UPDATE SET
			translation_credit=excluded.translation_credit,proofreading_credit=excluded.proofreading_credit,
			updated_at=excluded.updated_at,updated_by=excluded.updated_by,revision=excluded.revision`,
			bundle.documentID, item.RenditionKey, "zh-CN", item.TranslationCredit, item.ProofreadingCredit,
			now, user, nextRevision); err != nil {
			return LyricsRenditionDocument{}, false, err
		}
		for position, text := range item.Translations {
			if _, err := tx.Exec(`INSERT INTO song_lyrics_rendition_translation_lines
				(document_id,rendition_key,locale,position,text) VALUES (?,?,?,?,?)`,
				bundle.documentID, item.RenditionKey, "zh-CN", position, text); err != nil {
				return LyricsRenditionDocument{}, false, err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(ts,user,action,detail) VALUES (?,?,'lyrics.rendition.save',?)`,
		now, user, fmt.Sprintf("musicId=%d revision=%d", input.MusicID, nextRevision)); err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	nextState := lyricsRenditionLocalizationState{HasRows: true, Revision: nextRevision, UpdatedAt: now, Translations: requested}
	result, err := buildLyricsRenditionEditorDocument(bundle, nextState)
	if err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return LyricsRenditionDocument{}, false, err
	}
	s.NotifyChange()
	return result, true, nil
}

func validateLyricsRenditionImmutableEnvelope(input, current LyricsRenditionDocument) error {
	if input.MusicID <= 0 || input.Status != "draft" || input.PublishedRevision != 0 || input.UpdatedAt != current.UpdatedAt {
		return &LyricsRenditionContractError{Code: "source_drift", Details: []string{"plural rendition status and source envelope are immutable"}}
	}
	cloneAndClear := func(document LyricsRenditionDocument) (LyricsRenditionDocument, error) {
		body, err := json.Marshal(document)
		if err != nil {
			return LyricsRenditionDocument{}, err
		}
		var clone LyricsRenditionDocument
		if err := json.Unmarshal(body, &clone); err != nil {
			return LyricsRenditionDocument{}, err
		}
		for renditionIndex := range clone.Renditions {
			clone.Renditions[renditionIndex].TranslationCredits = nil
			for _, side := range []*PublicLyricsV3Side{clone.Renditions[renditionIndex].Full, clone.Renditions[renditionIndex].Game} {
				if side == nil {
					continue
				}
				for lineIndex := range side.Lines {
					side.Lines[lineIndex].Chinese = ""
				}
			}
		}
		return clone, nil
	}
	left, err := cloneAndClear(input)
	if err != nil {
		return err
	}
	right, err := cloneAndClear(current)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(left, right) {
		return &LyricsRenditionContractError{Code: "source_drift", Details: []string{
			"rendition keys, source text, line IDs/order, English text, relation, provenance, performers, segmentation, and ruby are immutable",
		}}
	}
	return nil
}

func lyricsRenditionEditorTranslations(input, current LyricsRenditionDocument, preserveRows bool) ([]lyricsstaging.RenditionTranslation, error) {
	if len(input.Renditions) != len(current.Renditions) || len(input.Renditions) == 0 {
		return nil, &LyricsRenditionContractError{Code: "source_drift", Details: []string{"plural rendition set changed"}}
	}
	result := make([]lyricsstaging.RenditionTranslation, len(input.Renditions))
	hasLocalization := preserveRows
	totalBytes := 0
	for index := range input.Renditions {
		rendition := input.Renditions[index]
		currentRendition := current.Renditions[index]
		if rendition.Key != currentRendition.Key {
			return nil, &LyricsRenditionContractError{Code: "source_drift", Details: []string{"plural rendition order changed"}}
		}
		item := lyricsstaging.RenditionTranslation{RenditionKey: rendition.Key}
		if rendition.TranslationCredits != nil {
			item.TranslationCredit = rendition.TranslationCredits.Translation
			item.ProofreadingCredit = rendition.TranslationCredits.Proofreading
			if item.TranslationCredit == "" && item.ProofreadingCredit == "" {
				return nil, &LyricsRenditionContractError{Code: "segment_mismatch", Details: []string{"translationCredits must not be empty"}}
			}
		}
		if len(item.TranslationCredit) > maxLyricsMetadataBytes || len(item.ProofreadingCredit) > maxLyricsMetadataBytes ||
			!utf8.ValidString(item.TranslationCredit) || !utf8.ValidString(item.ProofreadingCredit) {
			return nil, &LyricsRenditionContractError{Code: "segment_mismatch", Details: []string{"rendition credits exceed safe limits"}}
		}
		totalBytes += len(item.TranslationCredit) + len(item.ProofreadingCredit)
		var target, currentTarget *PublicLyricsV3Side
		if rendition.Full != nil {
			target, currentTarget = rendition.Full, currentRendition.Full
		} else {
			target, currentTarget = rendition.Game, currentRendition.Game
		}
		if target == nil || currentTarget == nil || len(target.Lines) != len(currentTarget.Lines) {
			return nil, &LyricsRenditionContractError{Code: "source_drift", Details: []string{"authoritative rendition side changed"}}
		}
		item.Translations = make([]string, len(target.Lines))
		for lineIndex, line := range target.Lines {
			if len(line.Chinese) > maxLyricsLineTextBytes || !utf8.ValidString(line.Chinese) {
				return nil, &LyricsRenditionContractError{Code: "segment_mismatch", Details: []string{"rendition translation exceeds the safe per-line limit"}}
			}
			item.Translations[lineIndex] = line.Chinese
			totalBytes += len(line.Chinese)
			if line.Chinese != "" {
				hasLocalization = true
			}
		}
		if item.TranslationCredit != "" || item.ProofreadingCredit != "" {
			hasLocalization = true
		}
		if rendition.Full != nil && rendition.Game != nil {
			if rendition.Relation.Kind == model.LyricsSourceRenditionRelationExactProjection {
				fullByID := make(map[string]string, len(rendition.Full.Lines))
				for _, line := range rendition.Full.Lines {
					fullByID[line.ID] = line.Chinese
				}
				if len(rendition.Relation.LineIDs) != len(rendition.Game.Lines) {
					return nil, &LyricsRenditionContractError{Code: "invalid_game_projection", Details: []string{"exact projection line count changed"}}
				}
				for lineIndex, lineID := range rendition.Relation.LineIDs {
					translation, found := fullByID[lineID]
					if !found || rendition.Game.Lines[lineIndex].Chinese != translation {
						return nil, &LyricsRenditionContractError{Code: "invalid_game_projection", Details: []string{"exact projection Game translation must follow its Full line IDs"}}
					}
				}
			} else if currentRendition.Game == nil || !samePublicV3ChineseLines(rendition.Game.Lines, currentRendition.Game.Lines) {
				return nil, &LyricsRenditionContractError{Code: "source_drift", Details: []string{"independent Game translation is not persisted by the current rendition localization schema"}}
			}
		}
		result[index] = item
	}
	if totalBytes > maxLyricsDocumentBytes {
		return nil, &LyricsRenditionContractError{Code: "segment_mismatch", Details: []string{"rendition localizations exceed the safe document size"}}
	}
	if !hasLocalization {
		return nil, nil
	}
	return result, nil
}

func samePublicV3ChineseLines(left, right []PublicLyricsV3Line) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Chinese != right[index].Chinese {
			return false
		}
	}
	return true
}
