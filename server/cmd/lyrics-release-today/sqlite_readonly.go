package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	_ "modernc.org/sqlite"
)

var sqliteSidecars = [...]string{"-wal", "-shm", "-journal"}

type readOnlySQLite struct {
	pinned        *pinnedFile
	db            *sql.DB
	initialSHA256 string
	maximumBytes  int64
	label         string
}

func openReadOnlySQLite(ctx context.Context, path, label string, allowedPerms ...os.FileMode) (*readOnlySQLite, error) {
	if err := rejectSQLiteSidecars(path, label); err != nil {
		return nil, err
	}
	if len(allowedPerms) == 0 {
		allowedPerms = []os.FileMode{0o600}
	}
	pinned, err := openPinnedRegular(path, label, allowedPerms...)
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*readOnlySQLite, error) {
		_ = pinned.close()
		return nil, cause
	}
	initialSHA256, _, _, err := hashPinnedDescriptor(pinned, label, maxCiphertextBytes)
	if err != nil {
		return fail(err)
	}
	descriptor := fmt.Sprintf("/dev/fd/%d", pinned.file.Fd())
	databaseURL := &url.URL{Scheme: "file", Path: descriptor}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "trusted_schema(0)")
	query.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return fail(fmt.Errorf("open immutable %s: %w", label, err))
	}
	result := &readOnlySQLite{
		pinned: pinned, db: database, initialSHA256: initialSHA256,
		maximumBytes: maxCiphertextBytes, label: label,
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	if err := database.PingContext(ctx); err != nil {
		_ = result.close()
		return nil, fmt.Errorf("ping immutable %s: %w", label, err)
	}
	var queryOnly, trustedSchema, attached int
	if err := database.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = result.close()
		return nil, errors.New("read-only SQLite query_only defense is not active")
	}
	if err := database.QueryRowContext(ctx, `PRAGMA trusted_schema`).Scan(&trustedSchema); err != nil || trustedSchema != 0 {
		_ = result.close()
		return nil, errors.New("read-only SQLite trusted_schema defense is not active")
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_database_list`).Scan(&attached); err != nil || attached != 1 {
		_ = result.close()
		return nil, errors.New("read-only SQLite attachment defense is not active")
	}
	if err := result.verifyUnchanged(); err != nil {
		_ = result.close()
		return nil, err
	}
	return result, nil
}

func (database *readOnlySQLite) verifyUnchanged() error {
	if database == nil || database.pinned == nil || database.initialSHA256 == "" || database.maximumBytes <= 0 {
		return errors.New("read-only SQLite byte binding is not active")
	}
	actual, _, _, err := hashPinnedDescriptor(database.pinned, database.label, database.maximumBytes)
	if err != nil {
		return err
	}
	if actual != database.initialSHA256 {
		return fmt.Errorf("%s bytes changed during pinned read-only validation", database.label)
	}
	return nil
}

func (database *readOnlySQLite) close() error {
	if database == nil {
		return nil
	}
	var result error
	if database.db != nil {
		result = database.db.Close()
		database.db = nil
	}
	if database.pinned != nil {
		result = errors.Join(result, database.pinned.close())
		database.pinned = nil
	}
	return result
}

func rejectSQLiteSidecars(path, label string) error {
	for _, suffix := range sqliteSidecars {
		_, err := os.Lstat(path + suffix)
		if err == nil {
			return fmt.Errorf("%s has an unexpected SQLite sidecar %s", label, suffix)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s SQLite sidecar: %w", label, err)
		}
	}
	return nil
}

func verifySQLiteIntegrity(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("run SQLite integrity_check: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		count++
		if value != "ok" {
			return errors.New("SQLite integrity_check did not return ok")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("SQLite integrity_check returned an unexpected result set")
	}
	foreignRows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("run SQLite foreign_key_check: %w", err)
	}
	defer foreignRows.Close()
	if foreignRows.Next() {
		return errors.New("SQLite foreign_key_check found a violation")
	}
	return foreignRows.Err()
}
