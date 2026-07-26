package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"time"

	"moesekai/server/internal/model"
)

var (
	ErrCategoryRevisionConflict = errors.New("category revision conflict")
	ErrEntryIdentityConflict    = errors.New("entry identity conflict")
)

type CategoryBatchResult struct {
	Snapshot model.CategoryLocaleSnapshot
	Changed  []model.CategoryEntryUpdate
}

type categorySnapshotQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type categorySnapshotRow struct {
	field, key, text, source, idsJSON string
}

// CategorySnapshotLocale returns all fields for one explicit locale from one
// SQLite transaction. The revision is deliberately opaque and covers both
// source identity and localized values.
func (s *Store) CategorySnapshotLocale(category, locale string) (model.CategoryLocaleSnapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.CategoryLocaleSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, _, err := categorySnapshotFrom(tx, category, locale)
	if err != nil {
		return model.CategoryLocaleSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CategoryLocaleSnapshot{}, err
	}
	return snapshot, nil
}

// UpdateCategoryLocale applies a set of existing category identities in one
// transaction after comparing the complete locale snapshot revision.
func (s *Store) UpdateCategoryLocale(category, locale, baseRevision, user string, updates []model.CategoryEntryUpdate) (CategoryBatchResult, error) {
	if locale == model.LocaleJapanese {
		return CategoryBatchResult{}, ErrReadOnlyLocale
	}
	tx, err := s.db.Begin()
	if err != nil {
		return CategoryBatchResult{}, err
	}
	defer tx.Rollback()

	current, rowsByIdentity, err := categorySnapshotFrom(tx, category, locale)
	if err != nil {
		return CategoryBatchResult{}, err
	}
	if baseRevision == "" || current.Revision != baseRevision {
		return CategoryBatchResult{Snapshot: current}, ErrCategoryRevisionConflict
	}

	seen := make(map[string]bool, len(updates))
	for _, update := range updates {
		identity := update.Field + "\x00" + update.Key
		if update.Field == "" || update.Key == "" || seen[identity] || !model.IsValidSource(update.Source) {
			return CategoryBatchResult{}, ErrEntryIdentityConflict
		}
		seen[identity] = true
		if _, ok := rowsByIdentity[identity]; !ok {
			return CategoryBatchResult{}, ErrEntryIdentityConflict
		}
	}

	now := time.Now().Unix()
	changed := make([]model.CategoryEntryUpdate, 0, len(updates))
	for _, update := range updates {
		identity := update.Field + "\x00" + update.Key
		row := rowsByIdentity[identity]
		if row.text == update.Text && row.source == update.Source {
			continue
		}
		if locale == model.LocaleChinese {
			result, err := tx.Exec(`UPDATE entries SET cn_text=?, source=?, updated_at=?, updated_by=?
				WHERE category=? AND field=? AND jp_key=?`,
				update.Text, update.Source, now, user, category, update.Field, update.Key)
			if err != nil {
				return CategoryBatchResult{}, err
			}
			if affected, err := result.RowsAffected(); err != nil || affected != 1 {
				if err != nil {
					return CategoryBatchResult{}, err
				}
				return CategoryBatchResult{}, ErrEntryIdentityConflict
			}
		} else {
			if _, err := tx.Exec(`INSERT INTO entry_localizations
				(category, field, jp_key, locale, text, source, updated_at, updated_by, revision)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
				ON CONFLICT(category, field, jp_key, locale) DO UPDATE SET
				text=excluded.text, source=excluded.source, updated_at=excluded.updated_at,
				updated_by=excluded.updated_by, revision=entry_localizations.revision+1`,
				category, update.Field, update.Key, locale, update.Text, update.Source, now, user); err != nil {
				return CategoryBatchResult{}, err
			}
		}
		if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail)
			VALUES (?, ?, 'entry.locale.update', ?)`, now, user,
			fmt.Sprintf("locale=%s category=%s field=%s batch=true", locale, category, update.Field)); err != nil {
			return CategoryBatchResult{}, err
		}
		changed = append(changed, update)
	}

	updated := current
	if len(changed) > 0 {
		updated, _, err = categorySnapshotFrom(tx, category, locale)
		if err != nil {
			return CategoryBatchResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CategoryBatchResult{}, err
	}
	if len(changed) > 0 {
		s.NotifyChange()
	}
	return CategoryBatchResult{Snapshot: updated, Changed: changed}, nil
}

func categorySnapshotFrom(q categorySnapshotQueryer, category, locale string) (model.CategoryLocaleSnapshot, map[string]categorySnapshotRow, error) {
	query := `SELECT e.field, e.jp_key, e.jp_key, 'unknown', e.ids_json
		FROM entries e WHERE e.category=? ORDER BY e.field, e.jp_key`
	args := []any{category}
	switch locale {
	case model.LocaleChinese:
		query = `SELECT e.field, e.jp_key, e.cn_text, e.source, e.ids_json
			FROM entries e WHERE e.category=? ORDER BY e.field, e.jp_key`
	case model.LocaleEnglish:
		query = `SELECT e.field, e.jp_key, COALESCE(l.text, ''), COALESCE(l.source, 'unknown'), e.ids_json
			FROM entries e LEFT JOIN entry_localizations l
			ON l.category=e.category AND l.field=e.field AND l.jp_key=e.jp_key AND l.locale=?
			WHERE e.category=? ORDER BY e.field, e.jp_key`
		args = []any{locale, category}
	case model.LocaleJapanese:
	default:
		return model.CategoryLocaleSnapshot{}, nil, fmt.Errorf("unsupported locale: %s", locale)
	}
	rows, err := q.Query(query, args...)
	if err != nil {
		return model.CategoryLocaleSnapshot{}, nil, err
	}
	defer rows.Close()

	snapshot := model.CategoryLocaleSnapshot{Category: category, Locale: locale, Fields: map[string][]model.EntryWithKey{}}
	byIdentity := map[string]categorySnapshotRow{}
	digest := sha256.New()
	writeRevisionPart(digest, category)
	writeRevisionPart(digest, locale)
	for rows.Next() {
		var row categorySnapshotRow
		if err := rows.Scan(&row.field, &row.key, &row.text, &row.source, &row.idsJSON); err != nil {
			return model.CategoryLocaleSnapshot{}, nil, err
		}
		entry := model.EntryWithKey{Key: row.key, Text: row.text, Source: row.source}
		if row.idsJSON != "" {
			if err := json.Unmarshal([]byte(row.idsJSON), &entry.Ids); err != nil {
				return model.CategoryLocaleSnapshot{}, nil, err
			}
		}
		snapshot.Fields[row.field] = append(snapshot.Fields[row.field], entry)
		byIdentity[row.field+"\x00"+row.key] = row
		writeRevisionPart(digest, row.field)
		writeRevisionPart(digest, row.key)
		writeRevisionPart(digest, row.text)
		writeRevisionPart(digest, row.source)
		writeRevisionPart(digest, row.idsJSON)
	}
	if err := rows.Err(); err != nil {
		return model.CategoryLocaleSnapshot{}, nil, err
	}
	snapshot.Revision = base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
	return snapshot, byIdentity, nil
}

func writeRevisionPart(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}
