package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	LyricsPeerTranslationSchemaVersion    = 29
	LyricsTranslationEditionSchemaVersion = 30
)

type RuntimeSchemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ValidateLyricsPeerTranslationSchema verifies the exact ledger and physical
// schema required by independently persisted rendition peer translations. It
// performs no writes and is safe for immutable snapshots and open transactions.
func ValidateLyricsPeerTranslationSchema(ctx context.Context, queryer RuntimeSchemaQueryer, required bool, operation string) error {
	if !required {
		return nil
	}
	if ctx == nil || queryer == nil {
		return errors.New("peer-translation schema validation requires context and database")
	}
	if err := validateRuntimeMigrationLedger(ctx, queryer, LyricsPeerTranslationSchemaVersion, operation+" peer-translation"); err != nil {
		return err
	}
	expectedColumns := []runtimeColumn{
		{name: "document_id", typeName: "INTEGER", primaryKey: 1},
		{name: "rendition_key", typeName: "TEXT", primaryKey: 2},
		{name: "side", typeName: "TEXT", primaryKey: 3},
		{name: "locale", typeName: "TEXT", primaryKey: 4},
		{name: "position", typeName: "INTEGER", primaryKey: 5},
		{name: "text", typeName: "TEXT"},
	}
	if err := validateRuntimeTableColumns(ctx, queryer, "song_lyrics_rendition_side_translation_lines", expectedColumns); err != nil {
		return fmt.Errorf("%s peer-translation schema-v%d table is invalid: %w", operation, LyricsPeerTranslationSchemaVersion, err)
	}
	if err := validateRuntimeIndex(ctx, queryer, "idx_song_lyrics_rendition_side_translation_lines_lookup", "document_id,rendition_key,side,locale,position"); err != nil {
		return fmt.Errorf("%s peer-translation schema-v%d index is invalid: %w", operation, LyricsPeerTranslationSchemaVersion, err)
	}
	return nil
}

// ValidateLyricsTranslationEditionSchema verifies the exact v30 migration
// ledger, columns, lookup indexes, and foreign-key graph used by translation
// editions. It performs no writes.
func ValidateLyricsTranslationEditionSchema(ctx context.Context, queryer RuntimeSchemaQueryer, required bool, operation string) error {
	if !required {
		return nil
	}
	if ctx == nil || queryer == nil {
		return errors.New("translation-edition schema validation requires context and database")
	}
	if err := validateRuntimeMigrationLedger(ctx, queryer, LyricsTranslationEditionSchemaVersion, operation+" translation-edition"); err != nil {
		return err
	}
	tables := []struct {
		name    string
		columns []runtimeColumn
	}{
		{name: "song_lyrics_translation_editions", columns: []runtimeColumn{
			{name: "document_id", typeName: "INTEGER", primaryKey: 1},
			{name: "edition_key", typeName: "TEXT", primaryKey: 2},
			{name: "label", typeName: "TEXT"},
			{name: "created_at", typeName: "INTEGER"},
			{name: "created_by", typeName: "TEXT"},
		}},
		{name: "song_lyrics_translation_edition_state", columns: []runtimeColumn{
			{name: "document_id", typeName: "INTEGER", primaryKey: 1},
			{name: "default_edition_key", typeName: "TEXT"},
			{name: "revision", typeName: "INTEGER"},
			{name: "updated_at", typeName: "INTEGER"},
			{name: "updated_by", typeName: "TEXT"},
		}},
		{name: "song_lyrics_translation_edition_localizations", columns: []runtimeColumn{
			{name: "document_id", typeName: "INTEGER", primaryKey: 1},
			{name: "edition_key", typeName: "TEXT", primaryKey: 2},
			{name: "rendition_key", typeName: "TEXT", primaryKey: 3},
			{name: "locale", typeName: "TEXT", primaryKey: 4},
			{name: "translation_credit", typeName: "TEXT"},
			{name: "proofreading_credit", typeName: "TEXT"},
			{name: "updated_at", typeName: "INTEGER"},
			{name: "updated_by", typeName: "TEXT"},
		}},
		{name: "song_lyrics_translation_edition_lines", columns: []runtimeColumn{
			{name: "document_id", typeName: "INTEGER", primaryKey: 1},
			{name: "edition_key", typeName: "TEXT", primaryKey: 2},
			{name: "rendition_key", typeName: "TEXT", primaryKey: 3},
			{name: "side", typeName: "TEXT", primaryKey: 4},
			{name: "locale", typeName: "TEXT", primaryKey: 5},
			{name: "position", typeName: "INTEGER", primaryKey: 6},
			{name: "text", typeName: "TEXT"},
		}},
	}
	for _, table := range tables {
		if err := validateRuntimeTableColumns(ctx, queryer, table.name, table.columns); err != nil {
			return fmt.Errorf("%s translation-edition schema-v%d table %s is invalid: %w", operation, LyricsTranslationEditionSchemaVersion, table.name, err)
		}
	}
	for name, columns := range map[string]string{
		"idx_song_lyrics_translation_editions_document":            "document_id,edition_key",
		"idx_song_lyrics_translation_edition_state_default":        "document_id,default_edition_key",
		"idx_song_lyrics_translation_edition_localizations_lookup": "document_id,edition_key,locale,rendition_key",
		"idx_song_lyrics_translation_edition_lines_lookup":         "document_id,edition_key,rendition_key,side,locale,position",
	} {
		if err := validateRuntimeIndex(ctx, queryer, name, columns); err != nil {
			return fmt.Errorf("%s translation-edition schema-v%d index %s is invalid: %w", operation, LyricsTranslationEditionSchemaVersion, name, err)
		}
	}
	foreignKeys := map[string][]runtimeForeignKey{
		"song_lyrics_translation_editions": {
			{table: "song_lyrics_source_documents", from: "document_id", to: "document_id", onDelete: "CASCADE"},
		},
		"song_lyrics_translation_edition_state": {
			{table: "song_lyrics_translation_editions", from: "document_id,default_edition_key", to: "document_id,edition_key", onDelete: "RESTRICT"},
		},
		"song_lyrics_translation_edition_localizations": {
			{table: "song_lyrics_translation_editions", from: "document_id,edition_key", to: "document_id,edition_key", onDelete: "CASCADE"},
		},
		"song_lyrics_translation_edition_lines": {
			{table: "song_lyrics_translation_edition_localizations", from: "document_id,edition_key,rendition_key,locale", to: "document_id,edition_key,rendition_key,locale", onDelete: "CASCADE"},
		},
	}
	for table, expected := range foreignKeys {
		if err := validateRuntimeForeignKeys(ctx, queryer, table, expected); err != nil {
			return fmt.Errorf("%s translation-edition schema-v%d foreign keys for %s are invalid: %w", operation, LyricsTranslationEditionSchemaVersion, table, err)
		}
	}
	return nil
}

type runtimeColumn struct {
	name       string
	typeName   string
	primaryKey int
}

type runtimeForeignKey struct {
	table    string
	from     string
	to       string
	onDelete string
}

func validateRuntimeMigrationLedger(ctx context.Context, queryer RuntimeSchemaQueryer, version int, operation string) error {
	if version < 1 || len(migrations) < version {
		return fmt.Errorf("%s schema is not registered by this binary", operation)
	}
	migration := migrations[version-1]
	var name, checksum string
	if err := queryer.QueryRowContext(ctx, `SELECT name,checksum FROM schema_migrations WHERE version=?`, version).Scan(&name, &checksum); err == sql.ErrNoRows {
		return fmt.Errorf("%s requires schema-v%d", operation, version)
	} else if err != nil {
		return fmt.Errorf("read %s schema: %w", operation, err)
	}
	if name != migration.name || checksum != migration.checksum() {
		return fmt.Errorf("%s schema-v%d ledger is invalid", operation, version)
	}
	return nil
}

func validateRuntimeTableColumns(ctx context.Context, queryer RuntimeSchemaQueryer, table string, expected []runtimeColumn) error {
	rows, err := queryer.QueryContext(ctx, fmt.Sprintf(`SELECT name,type,"notnull",pk FROM pragma_table_info('%s') ORDER BY cid`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(expected) {
			return errors.New("unexpected column")
		}
		var name, typeName string
		var notNull, primaryKey int
		if err := rows.Scan(&name, &typeName, &notNull, &primaryKey); err != nil {
			return err
		}
		want := expected[index]
		if name != want.name || typeName != want.typeName || notNull != 1 || primaryKey != want.primaryKey {
			return fmt.Errorf("column %d is %s/%s/notnull=%d/pk=%d", index, name, typeName, notNull, primaryKey)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(expected) {
		return errors.New("missing column")
	}
	return nil
}

func validateRuntimeIndex(ctx context.Context, queryer RuntimeSchemaQueryer, indexName, expectedColumns string) error {
	var columns sql.NullString
	if err := queryer.QueryRowContext(ctx, fmt.Sprintf(`SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_index_info('%s') ORDER BY seqno)`, indexName)).Scan(&columns); err != nil {
		return err
	}
	if !columns.Valid || columns.String != expectedColumns {
		return fmt.Errorf("columns=%q", columns.String)
	}
	return nil
}

func validateRuntimeForeignKeys(ctx context.Context, queryer RuntimeSchemaQueryer, table string, expected []runtimeForeignKey) error {
	rows, err := queryer.QueryContext(ctx, fmt.Sprintf(`SELECT id,seq,"table","from","to",on_delete FROM pragma_foreign_key_list('%s') ORDER BY id,seq`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	type partial struct {
		table    string
		from     []string
		to       []string
		onDelete string
	}
	var groups []partial
	lastID := -1
	for rows.Next() {
		var id, seq int
		var parent, from, to, onDelete string
		if err := rows.Scan(&id, &seq, &parent, &from, &to, &onDelete); err != nil {
			return err
		}
		if id != lastID {
			groups = append(groups, partial{table: parent, onDelete: onDelete})
			lastID = id
		}
		current := &groups[len(groups)-1]
		if current.table != parent || current.onDelete != onDelete || seq != len(current.from) {
			return errors.New("foreign-key sequence is invalid")
		}
		current.from = append(current.from, from)
		current.to = append(current.to, to)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(groups) != len(expected) {
		return fmt.Errorf("count=%d", len(groups))
	}
	for _, want := range expected {
		found := false
		for _, group := range groups {
			if group.table == want.table && strings.Join(group.from, ",") == want.from && strings.Join(group.to, ",") == want.to && group.onDelete == want.onDelete {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing %s(%s)->(%s) on delete %s", want.table, want.from, want.to, want.onDelete)
		}
	}
	return nil
}
