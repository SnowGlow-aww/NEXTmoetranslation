package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type sourceSchemaDefinition struct {
	name         string
	sql          string
	withoutRowID int
}

type sourceTableColumn struct {
	name     string
	typeName string
	notNull  int
	primary  int
}

type sourceForeignKey struct {
	table    string
	from     string
	to       string
	onUpdate string
	onDelete string
	match    string
}

var sourceSchemaDefinitions = []sourceSchemaDefinition{
	{name: "checkpoint_metadata", sql: `CREATE TABLE checkpoint_metadata (
			singleton INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
			checkpoint_schema_version INTEGER NOT NULL CHECK (checkpoint_schema_version = 2),
			report_schema_version INTEGER NOT NULL CHECK (report_schema_version = 1),
			catalog_schema_version INTEGER NOT NULL CHECK (catalog_schema_version = 18),
			catalog_count INTEGER NOT NULL CHECK (catalog_count >= 0 AND catalog_count <= 100000),
			catalog_fingerprint TEXT NOT NULL CHECK (length(catalog_fingerprint) = 64),
			generated_at TEXT NOT NULL,
			execution_options_json TEXT NOT NULL,
			execution_options_sha256 TEXT NOT NULL CHECK (length(execution_options_sha256) = 64)
		) STRICT`},
	{name: "checkpoint_counters", sql: `CREATE TABLE checkpoint_counters (
			singleton INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
			catalog_review INTEGER NOT NULL CHECK (catalog_review >= 0 AND catalog_review <= 100000),
			game_size_evidence INTEGER NOT NULL CHECK (game_size_evidence >= 0 AND game_size_evidence <= 100000),
			unique_complete INTEGER NOT NULL CHECK (unique_complete >= 0 AND unique_complete <= 100000),
			ambiguous INTEGER NOT NULL CHECK (ambiguous >= 0 AND ambiguous <= 100000),
			missing INTEGER NOT NULL CHECK (missing >= 0 AND missing <= 100000),
			incomplete INTEGER NOT NULL CHECK (incomplete >= 0 AND incomplete <= 100000),
			error INTEGER NOT NULL CHECK (error >= 0 AND error <= 100000),
			completed INTEGER NOT NULL CHECK (completed >= 0 AND completed <= 100000 AND completed = catalog_review + game_size_evidence + unique_complete + ambiguous + missing + incomplete + error),
			result_json_bytes INTEGER NOT NULL CHECK (result_json_bytes >= 0 AND result_json_bytes <= 8388608),
			evidence_items INTEGER NOT NULL CHECK (evidence_items >= 0 AND evidence_items <= 65536),
			evidence_raw_bytes INTEGER NOT NULL CHECK (evidence_raw_bytes >= 0 AND evidence_raw_bytes <= 33554432),
			evidence_json_bytes INTEGER NOT NULL CHECK (evidence_json_bytes >= 0 AND evidence_json_bytes <= 67108864),
			evidence_receipt_bytes INTEGER NOT NULL CHECK (evidence_receipt_bytes >= 0 AND evidence_receipt_bytes <= 67108864),
			CHECK ((evidence_items = 0 AND evidence_raw_bytes = 0 AND evidence_json_bytes = 0 AND evidence_receipt_bytes = 0) OR (evidence_items > 0 AND evidence_raw_bytes > 0 AND evidence_json_bytes > 0 AND evidence_receipt_bytes > 0))
		) STRICT`},
	{name: "catalog_targets", sql: `CREATE TABLE catalog_targets (
			music_id INTEGER NOT NULL PRIMARY KEY,
			target_kind TEXT NOT NULL CHECK (target_kind IN ('catalog_review','game_size_evidence','provider_work')),
			target_json BLOB NOT NULL,
			target_sha256 TEXT NOT NULL CHECK (length(target_sha256) = 64)
		) STRICT`},
	{name: "results", sql: `CREATE TABLE results (
			music_id INTEGER NOT NULL PRIMARY KEY REFERENCES catalog_targets(music_id) ON DELETE RESTRICT,
			class TEXT NOT NULL CHECK (class IN ('catalog_review','game_size_evidence','unique_complete','ambiguous','missing','incomplete','error')),
			result_json BLOB NOT NULL,
			result_sha256 TEXT NOT NULL CHECK (length(result_sha256) = 64),
			evidence_item_count INTEGER NOT NULL CHECK (evidence_item_count >= 0 AND evidence_item_count <= 65536),
			evidence_raw_bytes INTEGER NOT NULL CHECK (evidence_raw_bytes >= 0 AND evidence_raw_bytes <= 33554432)
		) STRICT`},
	{name: "evidence", withoutRowID: 1, sql: `CREATE TABLE evidence (
			evidence_id TEXT NOT NULL PRIMARY KEY,
			evidence_json BLOB NOT NULL,
			evidence_sha256 TEXT NOT NULL CHECK (length(evidence_sha256) = 64),
			raw_byte_count INTEGER NOT NULL CHECK (raw_byte_count >= 0 AND raw_byte_count <= 2097152)
		) STRICT, WITHOUT ROWID`},
	{name: "result_evidence", withoutRowID: 1, sql: `CREATE TABLE result_evidence (
			music_id INTEGER NOT NULL REFERENCES results(music_id) ON DELETE RESTRICT,
			evidence_id TEXT NOT NULL REFERENCES evidence(evidence_id) ON DELETE RESTRICT,
			PRIMARY KEY (music_id, evidence_id)
		) STRICT, WITHOUT ROWID`},
}

var sourceSchemaColumns = map[string][]sourceTableColumn{
	"checkpoint_metadata": {
		{name: "singleton", typeName: "INTEGER", notNull: 1, primary: 1},
		{name: "checkpoint_schema_version", typeName: "INTEGER", notNull: 1},
		{name: "report_schema_version", typeName: "INTEGER", notNull: 1},
		{name: "catalog_schema_version", typeName: "INTEGER", notNull: 1},
		{name: "catalog_count", typeName: "INTEGER", notNull: 1},
		{name: "catalog_fingerprint", typeName: "TEXT", notNull: 1},
		{name: "generated_at", typeName: "TEXT", notNull: 1},
		{name: "execution_options_json", typeName: "TEXT", notNull: 1},
		{name: "execution_options_sha256", typeName: "TEXT", notNull: 1},
	},
	"checkpoint_counters": {
		{name: "singleton", typeName: "INTEGER", notNull: 1, primary: 1},
		{name: "catalog_review", typeName: "INTEGER", notNull: 1},
		{name: "game_size_evidence", typeName: "INTEGER", notNull: 1},
		{name: "unique_complete", typeName: "INTEGER", notNull: 1},
		{name: "ambiguous", typeName: "INTEGER", notNull: 1},
		{name: "missing", typeName: "INTEGER", notNull: 1},
		{name: "incomplete", typeName: "INTEGER", notNull: 1},
		{name: "error", typeName: "INTEGER", notNull: 1},
		{name: "completed", typeName: "INTEGER", notNull: 1},
		{name: "result_json_bytes", typeName: "INTEGER", notNull: 1},
		{name: "evidence_items", typeName: "INTEGER", notNull: 1},
		{name: "evidence_raw_bytes", typeName: "INTEGER", notNull: 1},
		{name: "evidence_json_bytes", typeName: "INTEGER", notNull: 1},
		{name: "evidence_receipt_bytes", typeName: "INTEGER", notNull: 1},
	},
	"catalog_targets": {
		{name: "music_id", typeName: "INTEGER", notNull: 1, primary: 1},
		{name: "target_kind", typeName: "TEXT", notNull: 1},
		{name: "target_json", typeName: "BLOB", notNull: 1},
		{name: "target_sha256", typeName: "TEXT", notNull: 1},
	},
	"results": {
		{name: "music_id", typeName: "INTEGER", notNull: 1, primary: 1},
		{name: "class", typeName: "TEXT", notNull: 1},
		{name: "result_json", typeName: "BLOB", notNull: 1},
		{name: "result_sha256", typeName: "TEXT", notNull: 1},
		{name: "evidence_item_count", typeName: "INTEGER", notNull: 1},
		{name: "evidence_raw_bytes", typeName: "INTEGER", notNull: 1},
	},
	"evidence": {
		{name: "evidence_id", typeName: "TEXT", notNull: 1, primary: 1},
		{name: "evidence_json", typeName: "BLOB", notNull: 1},
		{name: "evidence_sha256", typeName: "TEXT", notNull: 1},
		{name: "raw_byte_count", typeName: "INTEGER", notNull: 1},
	},
	"result_evidence": {
		{name: "music_id", typeName: "INTEGER", notNull: 1, primary: 1},
		{name: "evidence_id", typeName: "TEXT", notNull: 1, primary: 2},
	},
}

var sourceSchemaForeignKeys = map[string][]sourceForeignKey{
	"results": {
		{table: "catalog_targets", from: "music_id", to: "music_id", onUpdate: "NO ACTION", onDelete: "RESTRICT", match: "NONE"},
	},
	"result_evidence": {
		{table: "evidence", from: "evidence_id", to: "evidence_id", onUpdate: "NO ACTION", onDelete: "RESTRICT", match: "NONE"},
		{table: "results", from: "music_id", to: "music_id", onUpdate: "NO ACTION", onDelete: "RESTRICT", match: "NONE"},
	},
}

func (checkpoint *sourceCheckpoint) validateSchema(ctx context.Context) error {
	rows, err := checkpoint.database.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return fmt.Errorf("read checkpoint schema: %w", err)
	}
	objects := make(map[string]struct{ objectType, sql string })
	for rows.Next() {
		var objectType, name, createSQL string
		if err := rows.Scan(&objectType, &name, &createSQL); err != nil {
			rows.Close()
			return err
		}
		if _, duplicate := objects[name]; duplicate {
			rows.Close()
			return errors.New("checkpoint schema contains a duplicate object")
		}
		objects[name] = struct{ objectType, sql string }{objectType: objectType, sql: createSQL}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(objects) != len(sourceSchemaDefinitions) {
		return errors.New("checkpoint schema contains unexpected objects")
	}
	for _, definition := range sourceSchemaDefinitions {
		object, found := objects[definition.name]
		if !found || object.objectType != "table" || object.sql != definition.sql {
			return errors.New("checkpoint schema does not match the exact v2 definition")
		}
		var schemaName, tableName, objectType string
		var columns, withoutRowID, strict int
		if err := checkpoint.database.QueryRowContext(ctx,
			`SELECT schema,name,type,ncol,wr,strict FROM pragma_table_list WHERE schema='main' AND name=?`, definition.name).
			Scan(&schemaName, &tableName, &objectType, &columns, &withoutRowID, &strict); err != nil {
			return err
		}
		if schemaName != "main" || tableName != definition.name || objectType != "table" ||
			columns != len(sourceSchemaColumns[definition.name]) || withoutRowID != definition.withoutRowID || strict != 1 {
			return errors.New("checkpoint table shape does not match the exact v2 definition")
		}
		actualColumns, err := checkpoint.tableColumns(ctx, definition.name)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actualColumns, sourceSchemaColumns[definition.name]) {
			return errors.New("checkpoint table columns changed")
		}
		actualForeignKeys, err := checkpoint.tableForeignKeys(ctx, definition.name)
		if err != nil {
			return err
		}
		expectedForeignKeys := append([]sourceForeignKey{}, sourceSchemaForeignKeys[definition.name]...)
		sortForeignKeys(actualForeignKeys)
		sortForeignKeys(expectedForeignKeys)
		if !reflect.DeepEqual(actualForeignKeys, expectedForeignKeys) {
			return errors.New("checkpoint foreign-key graph changed")
		}
	}
	return nil
}

func (checkpoint *sourceCheckpoint) tableColumns(ctx context.Context, table string) ([]sourceTableColumn, error) {
	rows, err := checkpoint.database.QueryContext(ctx, `PRAGMA table_info("`+table+`")`)
	if err != nil {
		return nil, err
	}
	result := []sourceTableColumn{}
	for rows.Next() {
		var sequence int
		var name, typeName string
		var notNull, primary int
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &typeName, &notNull, &defaultValue, &primary); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, sourceTableColumn{name: name, typeName: strings.ToUpper(typeName), notNull: notNull, primary: primary})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, rows.Err()
}

func (checkpoint *sourceCheckpoint) tableForeignKeys(ctx context.Context, table string) ([]sourceForeignKey, error) {
	rows, err := checkpoint.database.QueryContext(ctx, `PRAGMA foreign_key_list("`+table+`")`)
	if err != nil {
		return nil, err
	}
	result := []sourceForeignKey{}
	for rows.Next() {
		var id, sequence int
		var key sourceForeignKey
		if err := rows.Scan(&id, &sequence, &key.table, &key.from, &key.to, &key.onUpdate, &key.onDelete, &key.match); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, key)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, rows.Err()
}

func sortForeignKeys(keys []sourceForeignKey) {
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].table+"\x00"+keys[left].from < keys[right].table+"\x00"+keys[right].from
	})
}
