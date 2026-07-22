package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

func (s *Store) UpdateEntryLocale(category, field, key, text, source, user, locale string) (string, error) {
	if locale == model.LocaleChinese {
		return s.UpdateEntry(category, field, key, text, source, user)
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
