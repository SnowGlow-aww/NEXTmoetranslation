package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"moesekai/server/internal/model"
)

// ValidateLyricsStorageOwnership rejects historical databases that already
// contain both the legacy editable lyrics row and a native source-v3 document
// for the same music. Runtime triggers prevent new mixed writes, but existing
// mixed rows must also fail closed at every read/publish boundary.
func (d *DB) ValidateLyricsStorageOwnership(ctx context.Context) error {
	var musicID int
	err := d.QueryRowContext(ctx, `SELECT legacy.music_id
		FROM song_lyrics AS legacy
		JOIN song_lyrics_source_documents AS source
			ON source.music_id=legacy.music_id
		WHERE source.schema_version=?
		ORDER BY legacy.music_id
		LIMIT 1`, 3).Scan(&musicID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan lyrics storage ownership: %w", err)
	}
	return fmt.Errorf("mixed lyrics storage ownership for music %d: source-v3 and legacy editable rows coexist", musicID)
}

// ensureRuntimeInvariants installs additive guards that protect newer
// cross-table ownership rules without changing the checksum of an already
// published migration. These guards are safe to re-run on every normal Open.
func (d *DB) ensureRuntimeInvariants(ctx context.Context) error {
	if err := d.ValidateLyricsStorageOwnership(ctx); err != nil {
		return err
	}
	statements := []string{
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_source_v3_reject_legacy_insert
			BEFORE INSERT ON song_lyrics_source_documents
			WHEN NEW.schema_version=3 AND EXISTS (SELECT 1 FROM song_lyrics WHERE music_id=NEW.music_id)
			BEGIN SELECT RAISE(ABORT, 'source v3 cannot coexist with legacy editable lyrics'); END`,
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_reject_source_v3_insert
			BEFORE INSERT ON song_lyrics
			WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE music_id=NEW.music_id AND schema_version=3)
			BEGIN SELECT RAISE(ABORT, 'legacy editable lyrics cannot coexist with source v3'); END`,
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_reject_source_v3_update
			BEFORE UPDATE ON song_lyrics
			WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE music_id=NEW.music_id AND schema_version=3)
			BEGIN SELECT RAISE(ABORT, 'legacy editable lyrics cannot coexist with source v3'); END`,
		`CREATE TRIGGER IF NOT EXISTS song_lyrics_source_v3_reject_delete
			BEFORE DELETE ON song_lyrics_source_documents
			WHEN OLD.schema_version=3
			BEGIN SELECT RAISE(ABORT, 'source v3 documents are immutable'); END`,
		`UPDATE song_lyrics_rendition_localizations
			SET revision = (
				SELECT revision FROM song_lyrics_translation_edition_state
				WHERE song_lyrics_translation_edition_state.document_id = song_lyrics_rendition_localizations.document_id
			)
			WHERE document_id IN (
				SELECT document_id FROM song_lyrics_translation_edition_state
			)`,
	}
	for _, statement := range statements {
		if _, err := d.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return d.repairCatalogLyricsIdentity(ctx)
}

// sha256HexGlob matches exactly 64 lowercase hex characters, the shape every
// valid catalog identity fingerprint has.
const sha256HexGlob = "[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]"

// repairCatalogLyricsIdentity rewrites only catalog rows whose stored identity
// is structurally invalid: migration 34 shipped a placeholder fingerprint and a
// non-canonical policy version, which every later identity gate rejects. Rows
// that already carry a canonical policy and a SHA-256 shaped fingerprint are
// left untouched so valid identities keep their exact recorded bytes. The
// replacement fingerprint is derived from the row's own evidence columns rather
// than stored as a constant.
func (d *DB) repairCatalogLyricsIdentity(ctx context.Context) error {
	rows, err := d.QueryContext(ctx, `SELECT music_id,title_ja,lyricist,composer,arranger,
		assetbundle_name,version_hint,lyrics_version,lyrics_evidence_presence_json,vocal_signals_json,
		lyrics_catalog_fingerprint,lyrics_catalog_policy_version FROM catalog_music
		WHERE lyrics_catalog_policy_version<>? OR lyrics_catalog_fingerprint NOT GLOB ?`,
		model.LyricsCatalogIdentityPolicyVersion, sha256HexGlob)
	if err != nil {
		return fmt.Errorf("scan catalog lyrics identity: %w", err)
	}
	type repair struct {
		musicID       int
		fingerprint   string
		presenceJSON  string
		vocalsJSON    string
		lyricsVersion string
	}
	pending := []repair{}
	for rows.Next() {
		var musicID int
		var title, lyricist, composer, arranger, assetbundle, versionHint, lyricsVersion string
		var presenceJSON, vocalsJSON, storedFingerprint, storedPolicy string
		if err := rows.Scan(&musicID, &title, &lyricist, &composer, &arranger, &assetbundle,
			&versionHint, &lyricsVersion, &presenceJSON, &vocalsJSON, &storedFingerprint, &storedPolicy); err != nil {
			rows.Close()
			return fmt.Errorf("scan catalog lyrics identity row: %w", err)
		}
		var presence model.CatalogEvidencePresence
		if strings.TrimSpace(presenceJSON) != "" {
			if err := json.Unmarshal([]byte(presenceJSON), &presence); err != nil {
				rows.Close()
				return fmt.Errorf("catalog music %d evidence presence: %w", musicID, err)
			}
		}
		var vocals []model.CatalogVocalSignal
		if strings.TrimSpace(vocalsJSON) != "" {
			if err := json.Unmarshal([]byte(vocalsJSON), &vocals); err != nil {
				rows.Close()
				return fmt.Errorf("catalog music %d vocal signals: %w", musicID, err)
			}
		}
		// Normalization drops vocal signals that carry no identifying field, so
		// presence must be derived from the canonical set that actually gets
		// stored. Otherwise recomputing from the repaired row would yield a
		// different fingerprint and fail the identity gate again.
		canonicalVocals := model.NormalizeCatalogLyricsEvidence(
			model.CatalogLyricsEvidence{Vocals: vocals}).Vocals
		evidence := model.NormalizeCatalogLyricsEvidence(model.CatalogLyricsEvidence{
			Title: title, Lyricist: lyricist, Composer: composer, Arranger: arranger,
			Assetbundle: assetbundle, VersionHint: versionHint,
			LyricsVersion: strings.ToLower(strings.TrimSpace(lyricsVersion)), Vocals: canonicalVocals,
			Presence: model.CatalogEvidencePresence{
				Lyricist: strings.TrimSpace(lyricist) != "", Composer: strings.TrimSpace(composer) != "",
				Arranger: strings.TrimSpace(arranger) != "", Assetbundle: strings.TrimSpace(assetbundle) != "",
				VersionHint: strings.TrimSpace(versionHint) != "", LyricsVersion: presence.LyricsVersion,
				Vocals: len(canonicalVocals) > 0,
			},
		})
		fingerprint, err := model.CatalogLyricsEvidenceFingerprint(evidence)
		if err != nil {
			rows.Close()
			return fmt.Errorf("catalog music %d fingerprint: %w", musicID, err)
		}
		if storedFingerprint == fingerprint && storedPolicy == model.LyricsCatalogIdentityPolicyVersion {
			continue
		}
		canonicalPresence, err := json.Marshal(evidence.Presence)
		if err != nil {
			rows.Close()
			return err
		}
		storedVocals, err := json.Marshal(evidence.Vocals)
		if err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, repair{musicID: musicID, fingerprint: fingerprint,
			presenceJSON: string(canonicalPresence), vocalsJSON: string(storedVocals),
			lyricsVersion: evidence.LyricsVersion})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate catalog lyrics identity: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range pending {
		if _, err := d.ExecContext(ctx, `UPDATE catalog_music
			SET lyrics_evidence_presence_json=?, vocal_signals_json=?, lyrics_version=?,
				lyrics_catalog_fingerprint=?, lyrics_catalog_policy_version=?
			WHERE music_id=?`, item.presenceJSON, item.vocalsJSON, item.lyricsVersion,
			item.fingerprint, model.LyricsCatalogIdentityPolicyVersion, item.musicID); err != nil {
			return fmt.Errorf("repair catalog music %d identity: %w", item.musicID, err)
		}
	}
	return nil
}
