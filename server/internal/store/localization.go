package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"moesekai/server/internal/model"
)

var ErrReadOnlyLocale = errors.New("locale is read-only")

// GetCategoriesLocale returns the legacy category shape for an explicit locale.
// zh-CN delegates to the legacy table so old semantics remain authoritative.
func (s *Store) GetCategoriesLocale(locale string) ([]model.CategoryInfo, error) {
	if locale == model.LocaleChinese {
		return s.GetCategories()
	}
	query := `
		SELECT category, field, source, COUNT(*) FROM (
			SELECT e.category, e.field,
				CASE WHEN ?='ja-JP' THEN 'unknown' ELSE COALESCE(l.source, 'unknown') END AS source,
				e.jp_key
			FROM entries e
			LEFT JOIN entry_localizations l
			  ON l.category=e.category AND l.field=e.field AND l.jp_key=e.jp_key AND l.locale=?
			UNION ALL
			SELECT l.category, l.field, l.source, l.jp_key
			FROM entry_localizations l
			LEFT JOIN entries e
			  ON e.category=l.category AND e.field=l.field AND e.jp_key=l.jp_key
			WHERE l.locale=? AND e.jp_key IS NULL AND ?<>'ja-JP'
		) localized
		GROUP BY category, field, source`
	rows, err := s.db.Query(query, locale, locale, locale, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	acc := make(map[string]map[string]*model.FieldInfo)
	for rows.Next() {
		var category, field, source string
		var count int
		if err := rows.Scan(&category, &field, &source, &count); err != nil {
			return nil, err
		}
		if acc[category] == nil {
			acc[category] = map[string]*model.FieldInfo{}
		}
		info := acc[category][field]
		if info == nil {
			info = &model.FieldInfo{Name: field}
			acc[category][field] = info
		}
		info.Total += count
		switch source {
		case model.SourceCN:
			info.CnCount += count
		case model.SourceHuman:
			info.HumanCount += count
		case model.SourcePinned:
			info.PinnedCount += count
		case model.SourceLLM:
			info.LlmCount += count
		default:
			info.UnknownCount += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var result []model.CategoryInfo
	for _, category := range model.SupportedCategories {
		fieldsMap := acc[category]
		if len(fieldsMap) == 0 {
			continue
		}
		fields := make([]model.FieldInfo, 0, len(fieldsMap))
		for _, field := range fieldsMap {
			fields = append(fields, *field)
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		result = append(result, model.CategoryInfo{Name: category, Fields: fields})
	}
	return result, nil
}

func (s *Store) GetEntriesLocale(category, field, source, locale string) ([]model.EntryWithKey, error) {
	if locale == model.LocaleChinese {
		return s.GetEntries(category, field, source)
	}
	baseRows, err := s.db.Query(`SELECT e.jp_key, e.ids_json, l.text, l.source
		FROM entries e
		LEFT JOIN entry_localizations l
		  ON l.category=e.category AND l.field=e.field AND l.jp_key=e.jp_key AND l.locale=?
		WHERE e.category=? AND e.field=?`, locale, category, field)
	if err != nil {
		return nil, err
	}
	defer baseRows.Close()
	seen := map[string]bool{}
	var result []model.EntryWithKey
	for baseRows.Next() {
		var key, idsJSON string
		var localizedText, localizedSource sql.NullString
		if err := baseRows.Scan(&key, &idsJSON, &localizedText, &localizedSource); err != nil {
			return nil, err
		}
		entry := model.EntryWithKey{Key: key, Source: model.SourceUnknown}
		if locale == model.LocaleJapanese {
			entry.Text = key
		} else if localizedText.Valid {
			entry.Text = localizedText.String
			entry.Source = localizedSource.String
		}
		if source != "" && entry.Source != source {
			seen[key] = true
			continue
		}
		if idsJSON != "" {
			_ = json.Unmarshal([]byte(idsJSON), &entry.Ids)
		}
		seen[key] = true
		result = append(result, entry)
	}
	if err := baseRows.Err(); err != nil {
		return nil, err
	}
	if locale == model.LocaleJapanese {
		return result, nil
	}
	localizedRows, err := s.db.Query(`SELECT jp_key, text, source FROM entry_localizations
		WHERE category=? AND field=? AND locale=?`, category, field, locale)
	if err != nil {
		return nil, err
	}
	defer localizedRows.Close()
	for localizedRows.Next() {
		var entry model.EntryWithKey
		if err := localizedRows.Scan(&entry.Key, &entry.Text, &entry.Source); err != nil {
			return nil, err
		}
		if seen[entry.Key] || (source != "" && entry.Source != source) {
			continue
		}
		result = append(result, entry)
	}
	return result, localizedRows.Err()
}

func (s *Store) CategoryDataLocale(category, locale string) (model.Category, error) {
	if locale == model.LocaleChinese {
		return s.CategoryData(category)
	}
	rows, err := s.db.Query(`SELECT DISTINCT field FROM (
		SELECT field FROM entries WHERE category=?
		UNION SELECT field FROM entry_localizations WHERE category=? AND locale=?
	) ORDER BY field`, category, category, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fields []string
	for rows.Next() {
		var field string
		if err := rows.Scan(&field); err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	categoryData := model.Category{}
	for _, field := range fields {
		entries, err := s.GetEntriesLocale(category, field, "", locale)
		if err != nil {
			return nil, err
		}
		categoryData[field] = map[string]model.Entry{}
		for _, entry := range entries {
			categoryData[field][entry.Key] = model.Entry{Text: entry.Text, Source: entry.Source, Ids: entry.Ids}
		}
	}
	return categoryData, nil
}

// SearchTranslationSnapshotContext reads the Chinese and English category
// projections in one SQLite statement. The statement is a short WAL snapshot,
// so callers can release the restore fence before doing remote index fetches.
func (s *Store) SearchTranslationSnapshotContext(ctx context.Context, categories []string) (map[string]model.Category, map[string]model.Category, error) {
	if len(categories) == 0 {
		return map[string]model.Category{}, map[string]model.Category{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(categories)), ",")
	query := `SELECT 1, e.category, e.field, e.jp_key, e.cn_text, e.source, e.ids_json,
		COALESCE(l.text, ''), COALESCE(l.source, 'unknown')
		FROM entries e
		LEFT JOIN entry_localizations l
		  ON l.category=e.category AND l.field=e.field AND l.jp_key=e.jp_key AND l.locale=?
		WHERE e.category IN (` + placeholders + `)
		UNION ALL
		SELECT 0, l.category, l.field, l.jp_key, '', 'unknown', '', l.text, l.source
		FROM entry_localizations l
		LEFT JOIN entries e
		  ON e.category=l.category AND e.field=l.field AND e.jp_key=l.jp_key
		WHERE l.locale=? AND e.jp_key IS NULL AND l.category IN (` + placeholders + `)`
	args := make([]any, 0, 2+2*len(categories))
	args = append(args, model.LocaleEnglish)
	for _, category := range categories {
		args = append(args, category)
	}
	args = append(args, model.LocaleEnglish)
	for _, category := range categories {
		args = append(args, category)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	chinese := make(map[string]model.Category, len(categories))
	english := make(map[string]model.Category, len(categories))
	for _, category := range categories {
		chinese[category] = model.Category{}
		english[category] = model.Category{}
	}
	for rows.Next() {
		var base int
		var category, field, key, cnText, cnSource, idsJSON, enText, enSource string
		if err := rows.Scan(&base, &category, &field, &key, &cnText, &cnSource, &idsJSON, &enText, &enSource); err != nil {
			return nil, nil, err
		}
		var ids []string
		if idsJSON != "" {
			_ = json.Unmarshal([]byte(idsJSON), &ids)
		}
		if base == 1 {
			if chinese[category][field] == nil {
				chinese[category][field] = map[string]model.Entry{}
			}
			chinese[category][field][key] = model.Entry{Text: cnText, Source: cnSource, Ids: ids}
		}
		if english[category][field] == nil {
			english[category][field] = map[string]model.Entry{}
		}
		english[category][field][key] = model.Entry{Text: enText, Source: enSource, Ids: ids}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return chinese, english, nil
}

func (s *Store) UpdateEntryLocale(category, field, key, text, source, user, locale string) (string, error) {
	if !model.IsValidSource(source) {
		return "", fmt.Errorf("invalid translation source: %q", source)
	}
	var baseExists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE category=? AND field=? AND jp_key=?`, category, field, key).Scan(&baseExists); err != nil {
		return "", err
	}
	if baseExists != 1 {
		return "", sql.ErrNoRows
	}
	if locale == model.LocaleChinese {
		tx, err := s.db.Begin()
		if err != nil {
			return "", err
		}
		defer tx.Rollback()
		var currentText, currentSource string
		err = tx.QueryRow(`SELECT cn_text, source FROM entries
			WHERE category=? AND field=? AND jp_key=?`, category, field, key).Scan(&currentText, &currentSource)
		if err == nil && currentText == text && currentSource == source {
			return "noop", nil
		}
		if err != nil && err != sql.ErrNoRows {
			return "", err
		}
		now := time.Now().Unix()
		_, err = tx.Exec(`UPDATE entries SET cn_text=?, source=?, updated_at=?, updated_by=?
			WHERE category=? AND field=? AND jp_key=?`, text, source, now, user, category, field, key)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'entry.locale.update', ?)`,
			now, user, fmt.Sprintf("locale=%s category=%s field=%s", locale, category, field)); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		s.NotifyChange()
		return "ok", nil
	}
	if locale == model.LocaleJapanese {
		return "", ErrReadOnlyLocale
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var currentText, currentSource string
	err = tx.QueryRow(`SELECT text, source FROM entry_localizations
		WHERE category=? AND field=? AND jp_key=? AND locale=?`, category, field, key, locale).
		Scan(&currentText, &currentSource)
	if err == nil && currentText == text && currentSource == source {
		return "noop", nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO entry_localizations
		(category, field, jp_key, locale, text, source, updated_at, updated_by, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(category, field, jp_key, locale) DO UPDATE SET
		text=excluded.text, source=excluded.source, updated_at=excluded.updated_at,
		updated_by=excluded.updated_by, revision=entry_localizations.revision+1`,
		category, field, key, locale, text, source, now, user); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, 'entry.locale.update', ?)`,
		now, user, fmt.Sprintf("locale=%s category=%s field=%s", locale, category, field)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.NotifyChange()
	return "ok", nil
}

func (s *Store) EntryTextLocale(category, field, key, locale string) (string, error) {
	if locale == model.LocaleChinese {
		var text string
		err := s.db.QueryRow(`SELECT cn_text FROM entries WHERE category=? AND field=? AND jp_key=?`, category, field, key).Scan(&text)
		return text, err
	}
	if locale == model.LocaleJapanese {
		return key, nil
	}
	var text string
	err := s.db.QueryRow(`SELECT text FROM entry_localizations WHERE category=? AND field=? AND jp_key=? AND locale=?`,
		category, field, key, locale).Scan(&text)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return text, err
}
