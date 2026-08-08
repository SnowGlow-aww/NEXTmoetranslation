package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
)

var immutableSQLiteSidecarSuffixes = []string{"-journal", "-wal", "-shm"}

// ImmutableSnapshot pins one direct, standalone SQLite database file and opens
// it through its file descriptor with mode=ro, immutable=1, and query_only=1.
// Close revalidates the path, inode, size, bytes, and sidecar absence before and
// after SQLite closes.
type ImmutableSnapshot struct {
	Database *DB

	file         *os.File
	absolutePath string
	info         os.FileInfo
	size         int64
	digest       [sha256.Size]byte
	closeOnce    sync.Once
	closeErr     error
}

func OpenImmutableSnapshot(ctx context.Context, databasePath string) (*ImmutableSnapshot, error) {
	if ctx == nil {
		return nil, errors.New("immutable SQLite snapshot requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve immutable SQLite snapshot path: %w", err)
	}
	if filepath.Clean(absolutePath) != absolutePath {
		return nil, errors.New("immutable SQLite snapshot path must be canonical")
	}
	inspectedInfo, err := os.Lstat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("inspect immutable SQLite snapshot: %w", err)
	}
	if !inspectedInfo.Mode().IsRegular() || inspectedInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("immutable SQLite snapshot must be a direct regular file")
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil || resolvedPath != absolutePath {
		return nil, errors.New("immutable SQLite snapshot must not traverse a filesystem alias")
	}
	if err := rejectImmutableSQLiteSidecars(absolutePath); err != nil {
		return nil, err
	}

	file, err := os.Open(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open immutable SQLite snapshot: %w", err)
	}
	snapshot := &ImmutableSnapshot{file: file, absolutePath: absolutePath}
	fail := func(cause error) (*ImmutableSnapshot, error) {
		if snapshot.Database != nil && snapshot.Database.DB != nil {
			_ = snapshot.Database.DB.Close()
			snapshot.Database = nil
		}
		if snapshot.file != nil {
			_ = snapshot.file.Close()
			snapshot.file = nil
		}
		return nil, cause
	}

	openedInfo, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect opened immutable SQLite snapshot: %w", err))
	}
	pathInfo, err := os.Lstat(absolutePath)
	if err != nil || !openedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(inspectedInfo, openedInfo) ||
		!os.SameFile(inspectedInfo, pathInfo) || openedInfo.Size() != inspectedInfo.Size() ||
		pathInfo.Size() != inspectedInfo.Size() {
		return fail(errors.New("immutable SQLite snapshot path, inode, or size changed while being opened"))
	}
	snapshot.info = openedInfo
	snapshot.size = openedInfo.Size()
	snapshot.digest, err = hashImmutableSQLiteSnapshot(file, snapshot.size)
	if err != nil {
		return fail(fmt.Errorf("hash immutable SQLite snapshot: %w", err))
	}
	if err := snapshot.verifyCurrent("before SQLite open"); err != nil {
		return fail(err)
	}

	descriptorPath := fmt.Sprintf("/dev/fd/%d", file.Fd())
	databaseURL := &url.URL{Scheme: "file", Path: descriptorPath}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "trusted_schema(0)")
	query.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = query.Encode()
	sqlDB, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return fail(fmt.Errorf("open immutable SQLite database: %w", err))
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	snapshot.Database = &DB{DB: sqlDB, path: absolutePath}
	if err := sqlDB.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("ping immutable SQLite database: %w", err))
	}

	var databaseListSequence int
	var databaseListName, databaseListPath string
	if err := sqlDB.QueryRowContext(ctx, `PRAGMA database_list`).Scan(
		&databaseListSequence, &databaseListName, &databaseListPath,
	); err != nil {
		return fail(fmt.Errorf("verify opened immutable SQLite database: %w", err))
	}
	if databaseListSequence != 0 || databaseListName != "main" || databaseListPath == "" {
		return fail(errors.New("opened SQLite database does not match the pinned immutable snapshot"))
	}
	if filepath.Clean(databaseListPath) != filepath.Clean(descriptorPath) {
		openedDatabaseInfo, err := os.Stat(databaseListPath)
		if err != nil {
			return fail(fmt.Errorf("inspect opened SQLite database %q: %w", databaseListPath, err))
		}
		if !os.SameFile(openedInfo, openedDatabaseInfo) || openedDatabaseInfo.Size() != snapshot.size {
			return fail(fmt.Errorf("opened SQLite database %q does not match the pinned immutable snapshot", databaseListPath))
		}
	}
	var queryOnly, trustedSchema int
	if err := sqlDB.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil {
		return fail(fmt.Errorf("verify immutable SQLite query_only: %w", err))
	}
	if queryOnly != 1 {
		return fail(errors.New("immutable SQLite query_only is not enabled"))
	}
	if err := sqlDB.QueryRowContext(ctx, `PRAGMA trusted_schema`).Scan(&trustedSchema); err != nil {
		return fail(fmt.Errorf("verify immutable SQLite trusted_schema: %w", err))
	}
	if trustedSchema != 0 {
		return fail(errors.New("immutable SQLite trusted_schema is not disabled"))
	}
	var attachedCount int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_database_list`).Scan(&attachedCount); err != nil {
		return fail(fmt.Errorf("verify immutable SQLite attachment count: %w", err))
	}
	if attachedCount != 1 {
		return fail(errors.New("immutable SQLite snapshot has unexpected attached databases"))
	}
	if err := snapshot.verifyCurrent("after SQLite open"); err != nil {
		return fail(err)
	}
	return snapshot, nil
}

func (snapshot *ImmutableSnapshot) Path() string {
	if snapshot == nil {
		return ""
	}
	return snapshot.absolutePath
}

func (snapshot *ImmutableSnapshot) Size() int64 {
	if snapshot == nil {
		return 0
	}
	return snapshot.size
}

func (snapshot *ImmutableSnapshot) SHA256() string {
	if snapshot == nil {
		return ""
	}
	return hex.EncodeToString(snapshot.digest[:])
}

func (snapshot *ImmutableSnapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	snapshot.closeOnce.Do(func() {
		var result error
		if snapshot.Database != nil && snapshot.Database.DB != nil {
			if err := snapshot.verifyCurrent("before close"); err != nil {
				result = errors.Join(result, err)
			}
			if err := snapshot.Database.DB.Close(); err != nil {
				result = errors.Join(result, err)
			}
			snapshot.Database = nil
			if err := snapshot.verifyCurrent("after close"); err != nil {
				result = errors.Join(result, err)
			}
		}
		if snapshot.file != nil {
			if err := snapshot.file.Close(); err != nil {
				result = errors.Join(result, err)
			}
			snapshot.file = nil
		}
		snapshot.closeErr = result
	})
	return snapshot.closeErr
}

func (snapshot *ImmutableSnapshot) verifyCurrent(stage string) error {
	if snapshot == nil || snapshot.file == nil || snapshot.info == nil {
		return errors.New("immutable SQLite snapshot is not active")
	}
	if err := rejectImmutableSQLiteSidecars(snapshot.absolutePath); err != nil {
		return fmt.Errorf("immutable SQLite snapshot is not standalone %s: %w", stage, err)
	}
	fileInfo, err := snapshot.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect pinned immutable SQLite snapshot %s: %w", stage, err)
	}
	pathInfo, err := os.Lstat(snapshot.absolutePath)
	if err != nil || !fileInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(snapshot.info, fileInfo) ||
		!os.SameFile(snapshot.info, pathInfo) || fileInfo.Size() != snapshot.size || pathInfo.Size() != snapshot.size {
		return fmt.Errorf("immutable SQLite snapshot path, inode, or size changed %s", stage)
	}
	digest, err := hashImmutableSQLiteSnapshot(snapshot.file, snapshot.size)
	if err != nil {
		return fmt.Errorf("hash pinned immutable SQLite snapshot %s: %w", stage, err)
	}
	if digest != snapshot.digest {
		return fmt.Errorf("immutable SQLite snapshot bytes changed %s", stage)
	}
	return nil
}

func rejectImmutableSQLiteSidecars(databasePath string) error {
	for _, suffix := range immutableSQLiteSidecarSuffixes {
		if _, err := os.Lstat(databasePath + suffix); err == nil {
			return fmt.Errorf("immutable SQLite snapshot requires no %s sidecar", suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect immutable SQLite %s sidecar: %w", suffix, err)
		}
	}
	return nil
}

func hashImmutableSQLiteSnapshot(file *os.File, size int64) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if file == nil || size < 0 {
		return digest, errors.New("immutable SQLite snapshot file and non-negative size are required")
	}
	hasher := sha256.New()
	read, err := io.Copy(hasher, io.NewSectionReader(file, 0, size))
	if err != nil {
		return digest, err
	}
	if read != size {
		return digest, errors.New("immutable SQLite snapshot size changed while hashing")
	}
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}
