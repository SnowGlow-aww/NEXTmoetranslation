package collab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/reearth/ygo/crdt"
	"moesekai/server/internal/db"
	"moesekai/server/internal/store"
)

var roomPattern = regexp.MustCompile(`^lyrics-([1-9][0-9]*)-e([1-9][0-9]*)$`)

type roomIdentity struct {
	musicID int
	epoch   int64
}

type persistedDocument struct {
	roomIdentity
	update          []byte
	baseRevision    int
	authoritySHA256 string
	kind            documentKind
}

type sqlitePersistence struct {
	db                *db.DB
	store             *store.Store
	afterLoadSnapshot func()
}

func parseRoom(room string) (roomIdentity, error) {
	parts := roomPattern.FindStringSubmatch(room)
	if len(parts) != 3 {
		return roomIdentity{}, ErrInvalidRoom
	}
	musicID64, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || musicID64 <= 0 {
		return roomIdentity{}, ErrInvalidRoom
	}
	epoch, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || epoch <= 0 {
		return roomIdentity{}, ErrInvalidRoom
	}
	return roomIdentity{musicID: int(musicID64), epoch: epoch}, nil
}

func roomName(musicID int, epoch int64) string {
	return fmt.Sprintf("lyrics-%d-e%d", musicID, epoch)
}

func (p *sqlitePersistence) LoadDoc(room string) ([]byte, error) {
	return p.loadDocContext(context.Background(), room)
}

func (p *sqlitePersistence) loadDocContext(ctx context.Context, room string) ([]byte, error) {
	identity, err := parseRoom(room)
	if err != nil {
		return nil, err
	}
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var snapshot []byte
	var epoch int64
	err = tx.QueryRowContext(ctx, `SELECT epoch,update_v1 FROM lyrics_collab_documents WHERE music_id=?`, identity.musicID).
		Scan(&epoch, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		// Tickets create the baseline before the ygo loader runs. Treating a
		// missing row as unavailable keeps arbitrary Apply/room creation calls
		// from manufacturing an untracked draft.
		return nil, ErrRoomUnavailable
	}
	if err != nil {
		return nil, err
	}
	if epoch != identity.epoch {
		return nil, ErrRetiredRoom
	}
	if len(snapshot) == 0 || len(snapshot) > maxDocumentUpdateBytes {
		return nil, ErrUpdateTooLarge
	}
	if p.afterLoadSnapshot != nil {
		p.afterLoadSnapshot()
	}
	rows, err := tx.QueryContext(ctx, `SELECT update_v1,update_sha256,update_size FROM lyrics_collab_updates
		WHERE music_id=? AND epoch=? ORDER BY seq`, identity.musicID, identity.epoch)
	if err != nil {
		return nil, err
	}
	updates := [][]byte{snapshot}
	for rows.Next() {
		var update []byte
		var digest string
		var size int
		if err := rows.Scan(&update, &digest, &size); err != nil {
			rows.Close()
			return nil, err
		}
		if err := validatePersistedUpdate(update, digest, size); err != nil {
			rows.Close()
			return nil, err
		}
		updates = append(updates, update)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return mergeDocumentUpdates(updates)
}

func mergeDocumentUpdates(updates [][]byte) ([]byte, error) {
	if len(updates) == 1 {
		return updates[0], nil
	}
	merged, err := crdt.MergeUpdatesV1(updates...)
	if err != nil {
		return nil, fmt.Errorf("merge collaboration log: %w", err)
	}
	if len(merged) == 0 || len(merged) > maxDocumentUpdateBytes {
		return nil, ErrUpdateTooLarge
	}
	return merged, nil
}

func validatePersistedUpdate(update []byte, digest string, size int) error {
	if len(update) == 0 || len(update) > maxUpdateBytes || size != len(update) {
		return fmt.Errorf("invalid persisted collaboration update size")
	}
	actual := sha256.Sum256(update)
	if hex.EncodeToString(actual[:]) != digest {
		return fmt.Errorf("invalid persisted collaboration update checksum")
	}
	return nil
}

func (p *sqlitePersistence) StoreUpdate(room string, update []byte) error {
	return p.StoreUpdateContext(context.Background(), room, update)
}

func (p *sqlitePersistence) StoreUpdateContext(ctx context.Context, room string, update []byte) error {
	identity, err := parseRoom(room)
	if err != nil {
		return err
	}
	if len(update) == 0 || len(update) > maxUpdateBytes {
		return ErrUpdateTooLarge
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT epoch FROM lyrics_collab_documents WHERE music_id=?`, identity.musicID).
		Scan(&currentEpoch); err != nil {
		return err
	}
	if currentEpoch != identity.epoch {
		return ErrRetiredRoom
	}
	var nextSeq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM lyrics_collab_updates
		WHERE music_id=? AND epoch=?`, identity.musicID, identity.epoch).Scan(&nextSeq); err != nil {
		return err
	}
	digest := sha256.Sum256(update)
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `INSERT INTO lyrics_collab_updates
		(music_id,epoch,seq,update_v1,update_sha256,update_size,created_at)
		SELECT ?,?,?,?,?,?,? WHERE EXISTS (
			SELECT 1 FROM lyrics_collab_documents WHERE music_id=? AND epoch=?
		)`, identity.musicID, identity.epoch, nextSeq, update, hex.EncodeToString(digest[:]), len(update), now,
		identity.musicID, identity.epoch)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRetiredRoom
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lyrics_collab_documents SET updated_at=?
		WHERE music_id=? AND epoch=?`, now, identity.musicID, identity.epoch); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *sqlitePersistence) Compact(ctx context.Context, room string) error {
	identity, err := parseRoom(room)
	if err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentEpoch int64
	var snapshot []byte
	if err := tx.QueryRowContext(ctx, `SELECT epoch,update_v1 FROM lyrics_collab_documents WHERE music_id=?`, identity.musicID).
		Scan(&currentEpoch, &snapshot); err != nil {
		return err
	}
	if currentEpoch != identity.epoch {
		return ErrRetiredRoom
	}
	rows, err := tx.QueryContext(ctx, `SELECT update_v1,update_sha256,update_size FROM lyrics_collab_updates
		WHERE music_id=? AND epoch=? ORDER BY seq`, identity.musicID, identity.epoch)
	if err != nil {
		return err
	}
	updates := [][]byte{snapshot}
	for rows.Next() {
		var update []byte
		var digest string
		var size int
		if err := rows.Scan(&update, &digest, &size); err != nil {
			rows.Close()
			return err
		}
		if err := validatePersistedUpdate(update, digest, size); err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(updates) == 1 {
		return tx.Commit()
	}
	merged, err := crdt.MergeUpdatesV1(updates...)
	if err != nil {
		return err
	}
	if len(merged) > maxDocumentUpdateBytes {
		return ErrUpdateTooLarge
	}
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_collab_documents SET update_v1=?,updated_at=?
		WHERE music_id=? AND epoch=?`, merged, time.Now().UTC().Unix(), identity.musicID, identity.epoch)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrRetiredRoom
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lyrics_collab_updates WHERE music_id=? AND epoch=?`, identity.musicID, identity.epoch); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *sqlitePersistence) ensureRoom(ctx context.Context, musicID int) (persistedDocument, error) {
	if musicID <= 0 {
		return persistedDocument{}, ErrInvalidRoom
	}
	var result persistedDocument
	err := p.db.QueryRowContext(ctx, `SELECT epoch,update_v1,base_revision,authority_sha256
		FROM lyrics_collab_documents WHERE music_id=?`, musicID).
		Scan(&result.epoch, &result.update, &result.baseRevision, &result.authoritySHA256)
	if err == nil {
		result.musicID = musicID
		document, loadErr := p.authoritativeDocument(musicID)
		if loadErr != nil {
			return persistedDocument{}, loadErr
		}
		_, currentSHA, currentRevision, kind, canonicalErr := canonicalDocument(document)
		if canonicalErr != nil {
			return persistedDocument{}, canonicalErr
		}
		if currentSHA != result.authoritySHA256 || currentRevision != result.baseRevision {
			return persistedDocument{}, ErrAuthorityDrift
		}
		result.kind = kind
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return persistedDocument{}, err
	}
	document, err := p.authoritativeDocument(musicID)
	if err != nil {
		return persistedDocument{}, err
	}
	update, err := documentUpdate(document)
	if err != nil {
		return persistedDocument{}, err
	}
	_, authoritySHA, revision, _, err := canonicalDocument(document)
	if err != nil {
		return persistedDocument{}, err
	}
	now := time.Now().UTC().Unix()
	_, err = p.db.ExecContext(ctx, `INSERT INTO lyrics_collab_documents
		(music_id,schema_version,epoch,update_v1,base_revision,authority_sha256,updated_at)
		VALUES (?,1,1,?,?,?,?) ON CONFLICT(music_id) DO NOTHING`,
		musicID, update, revision, authoritySHA, now)
	if err != nil {
		return persistedDocument{}, err
	}
	// Reload to handle two simultaneous first tickets without trusting which
	// INSERT won.
	return p.ensureRoom(ctx, musicID)
}

func (p *sqlitePersistence) authoritativeDocument(musicID int) (any, error) {
	document, err := p.store.GetLyricsDocument(musicID)
	if errors.Is(err, store.ErrLyricsNotFound) {
		var exists int
		if queryErr := p.db.QueryRow(`SELECT COUNT(*) FROM catalog_music WHERE music_id=?`, musicID).Scan(&exists); queryErr != nil {
			return nil, queryErr
		}
		if exists != 1 {
			return nil, store.ErrLyricsNotFound
		}
		return blankLyrics(musicID), nil
	}
	return document, err
}

func (p *sqlitePersistence) baseline(ctx context.Context, musicID int) (persistedDocument, error) {
	var result persistedDocument
	var kindValue int
	err := p.db.QueryRowContext(ctx, `SELECT epoch,update_v1,base_revision,authority_sha256,
		CASE WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents s
			WHERE s.music_id=lyrics_collab_documents.music_id AND s.schema_version=3) THEN 1 ELSE 0 END
		FROM lyrics_collab_documents WHERE music_id=?`, musicID).
		Scan(&result.epoch, &result.update, &result.baseRevision, &result.authoritySHA256, &kindValue)
	if err != nil {
		return persistedDocument{}, err
	}
	result.musicID = musicID
	result.kind = documentKind(kindValue)
	result.update, err = p.loadDocContext(ctx, roomName(musicID, result.epoch))
	if err != nil {
		return persistedDocument{}, err
	}
	return result, nil
}

func (p *sqlitePersistence) currentEpoch(ctx context.Context, musicID int) (int64, error) {
	var epoch int64
	if err := p.db.QueryRowContext(ctx, `SELECT epoch FROM lyrics_collab_documents WHERE music_id=?`, musicID).Scan(&epoch); err != nil {
		return 0, err
	}
	return epoch, nil
}

func (p *sqlitePersistence) commitCheckpoint(
	ctx context.Context,
	baseline persistedDocument,
	update []byte,
	newRevision int,
	newAuthoritySHA256 string,
	actor string,
	changed bool,
) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := p.commitCheckpointTx(ctx, tx, baseline, update, newRevision, newAuthoritySHA256, actor, changed); err != nil {
		return err
	}
	return tx.Commit()
}

// commitCheckpointTx advances a collaboration snapshot and its checkpoint
// ledger using a transaction owned by the caller. It deliberately does not
// commit or roll back, allowing the authoritative store mutation and the
// collaboration state to share one atomic SQLite commit.
func (p *sqlitePersistence) commitCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	baseline persistedDocument,
	update []byte,
	newRevision int,
	newAuthoritySHA256 string,
	actor string,
	changed bool,
) error {
	if tx == nil {
		return errors.New("checkpoint transaction is nil")
	}
	if len(update) == 0 || len(update) > maxDocumentUpdateBytes {
		return ErrUpdateTooLarge
	}
	// Checkpointing races with ygo's persistence worker, which may compact the
	// incremental log after this method's caller encoded `update`.  Never write
	// that potentially stale encoding over a newer compacted snapshot.  The
	// write transaction is also the serialization point for StoreUpdate and
	// Compact, so the snapshot and the log rows observed below form one state.
	var currentEpoch int64
	var currentRevision int
	var currentSHA string
	var currentSnapshot []byte
	if err := tx.QueryRowContext(ctx, `SELECT epoch,base_revision,authority_sha256,update_v1
		FROM lyrics_collab_documents WHERE music_id=?`, baseline.musicID).
		Scan(&currentEpoch, &currentRevision, &currentSHA, &currentSnapshot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRetiredRoom
		}
		return err
	}
	if currentEpoch != baseline.epoch || currentRevision != baseline.baseRevision || currentSHA != baseline.authoritySHA256 {
		return ErrRetiredRoom
	}
	if len(currentSnapshot) == 0 || len(currentSnapshot) > maxDocumentUpdateBytes {
		return ErrUpdateTooLarge
	}
	updates := [][]byte{currentSnapshot, update}
	var cutoff int64
	rows, err := tx.QueryContext(ctx, `SELECT seq,update_v1,update_sha256,update_size
		FROM lyrics_collab_updates WHERE music_id=? AND epoch=? ORDER BY seq`, baseline.musicID, baseline.epoch)
	if err != nil {
		return err
	}
	for rows.Next() {
		var seq int64
		var incremental []byte
		var digest string
		var size int
		if err := rows.Scan(&seq, &incremental, &digest, &size); err != nil {
			rows.Close()
			return err
		}
		if err := validatePersistedUpdate(incremental, digest, size); err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, incremental)
		if seq > cutoff {
			cutoff = seq
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	merged, err := crdt.MergeUpdatesV1(updates...)
	if err != nil {
		return fmt.Errorf("merge checkpoint state: %w", err)
	}
	if len(merged) == 0 || len(merged) > maxDocumentUpdateBytes {
		return ErrUpdateTooLarge
	}
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_collab_documents
		SET update_v1=?,base_revision=?,authority_sha256=?,updated_at=?,checkpointed_at=?,checkpointed_by=?
		WHERE music_id=? AND epoch=? AND base_revision=? AND authority_sha256=?`,
		merged, newRevision, newAuthoritySHA256, now, now, actor,
		baseline.musicID, baseline.epoch, baseline.baseRevision, baseline.authoritySHA256)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrRetiredRoom
	}
	if err := insertCheckpointLedger(ctx, tx, baseline, newRevision, newAuthoritySHA256, actor, changed, now); err != nil {
		return err
	}
	// Only remove log rows observed by this transaction. StoreUpdate and Compact
	// serialize behind the write transaction, so a row arriving after the
	// checkpoint starts remains available for the next load/compaction.
	if cutoff > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM lyrics_collab_updates
			WHERE music_id=? AND epoch=? AND seq<=?`, baseline.musicID, baseline.epoch, cutoff); err != nil {
			return err
		}
	}
	return nil
}

func (p *sqlitePersistence) reseedCheckpoint(
	ctx context.Context,
	baseline persistedDocument,
	document any,
	actor string,
	changed bool,
) (string, string, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	oldRoom, newRoom, err := p.reseedCheckpointTx(ctx, tx, baseline, document, actor, changed)
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return oldRoom, newRoom, nil
}

// reseedCheckpointTx retires the current epoch and seeds the saved authority
// using a caller-owned transaction. It deliberately leaves commit/rollback to
// the caller so a first authoritative save and its collaboration epoch switch
// cannot diverge.
func (p *sqlitePersistence) reseedCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	baseline persistedDocument,
	document any,
	actor string,
	changed bool,
) (string, string, error) {
	if tx == nil {
		return "", "", errors.New("checkpoint transaction is nil")
	}
	update, err := documentUpdate(document)
	if err != nil {
		return "", "", err
	}
	_, newAuthoritySHA256, newRevision, _, err := canonicalDocument(document)
	if err != nil {
		return "", "", err
	}
	if baseline.epoch == int64(^uint64(0)>>1) {
		return "", "", fmt.Errorf("collaboration epoch exhausted")
	}
	newEpoch := baseline.epoch + 1
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE lyrics_collab_documents SET
		epoch=?,update_v1=?,base_revision=?,authority_sha256=?,updated_at=?,checkpointed_at=?,checkpointed_by=?
		WHERE music_id=? AND epoch=? AND base_revision=? AND authority_sha256=?`,
		newEpoch, update, newRevision, newAuthoritySHA256, now, now, actor,
		baseline.musicID, baseline.epoch, baseline.baseRevision, baseline.authoritySHA256)
	if err != nil {
		return "", "", err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if affected != 1 {
		return "", "", ErrRetiredRoom
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lyrics_collab_updates WHERE music_id=?`, baseline.musicID); err != nil {
		return "", "", err
	}
	if err := insertCheckpointLedger(ctx, tx, baseline, newRevision, newAuthoritySHA256, actor, changed, now); err != nil {
		return "", "", err
	}
	return roomName(baseline.musicID, baseline.epoch), roomName(baseline.musicID, newEpoch), nil
}

func insertCheckpointLedger(
	ctx context.Context,
	tx *sql.Tx,
	baseline persistedDocument,
	newRevision int,
	newAuthoritySHA256 string,
	actor string,
	changed bool,
	createdAt int64,
) error {
	changedValue := 0
	if changed {
		changedValue = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO lyrics_collab_checkpoints
		(music_id,epoch,base_revision,new_revision,base_authority_sha256,new_authority_sha256,actor,changed,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, baseline.musicID, baseline.epoch, baseline.baseRevision, newRevision,
		baseline.authoritySHA256, newAuthoritySHA256, actor, changedValue, createdAt)
	return err
}

func (p *sqlitePersistence) replaceFromAuthoritative(ctx context.Context, musicID int) (string, string, error) {
	document, err := p.authoritativeDocument(musicID)
	if err != nil {
		return "", "", err
	}
	update, err := documentUpdate(document)
	if err != nil {
		return "", "", err
	}
	_, authoritySHA, revision, _, err := canonicalDocument(document)
	if err != nil {
		return "", "", err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var oldEpoch int64
	err = tx.QueryRowContext(ctx, `SELECT epoch FROM lyrics_collab_documents WHERE music_id=?`, musicID).Scan(&oldEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		oldEpoch = 0
	} else if err != nil {
		return "", "", err
	}
	newEpoch := oldEpoch + 1
	if newEpoch <= 0 {
		newEpoch = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lyrics_collab_documents
		(music_id,schema_version,epoch,update_v1,base_revision,authority_sha256,updated_at,checkpointed_at,checkpointed_by)
		VALUES (?,1,?,?,?,?,?,0,'') ON CONFLICT(music_id) DO UPDATE SET
		epoch=excluded.epoch,update_v1=excluded.update_v1,base_revision=excluded.base_revision,
		authority_sha256=excluded.authority_sha256,updated_at=excluded.updated_at,
		checkpointed_at=0,checkpointed_by=''`,
		musicID, newEpoch, update, revision, authoritySHA, time.Now().UTC().Unix())
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lyrics_collab_updates WHERE music_id=?`, musicID); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	oldRoom := ""
	if oldEpoch > 0 {
		oldRoom = roomName(musicID, oldEpoch)
	}
	return oldRoom, roomName(musicID, newEpoch), nil
}

func (p *sqlitePersistence) bumpAllEpochs(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT music_id,epoch FROM lyrics_collab_documents`)
	if err != nil {
		return nil, err
	}
	var rooms []string
	for rows.Next() {
		var musicID int
		var epoch int64
		if err := rows.Scan(&musicID, &epoch); err != nil {
			rows.Close()
			return nil, err
		}
		rooms = append(rooms, roomName(musicID, epoch))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	_, err = p.db.ExecContext(ctx, `UPDATE lyrics_collab_documents SET epoch=epoch+1, updated_at=?`, time.Now().UTC().Unix())
	return rooms, err
}
