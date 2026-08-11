package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	_ "modernc.org/sqlite"
)

const maxSourceCheckpointBytes int64 = 128 << 20

type sourceCheckpoint struct {
	path            string
	parentPath      string
	parentFile      *os.File
	parentInfo      os.FileInfo
	file            *os.File
	fileInfo        os.FileInfo
	database        *sql.DB
	operationalPath string
	sha256          string
	byteCount       int64
}

func openSourceCheckpoint(ctx context.Context, path string) (*sourceCheckpoint, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" || strings.TrimSpace(path) != path {
		return nil, errors.New("checkpoint path is required without surrounding whitespace")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve checkpoint path: %w", err)
	}
	if err := validateSourceAncestorChain(filepath.Dir(absolute)); err != nil {
		return nil, err
	}
	if err := validateDirectParentPath(filepath.Dir(absolute), "checkpoint"); err != nil {
		return nil, err
	}
	visibleInfo, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect checkpoint path: %w", err)
	}
	if err := validateSourceRegularFile(visibleInfo); err != nil {
		return nil, err
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, fmt.Errorf("resolve checkpoint parent: %w", err)
	}
	resolvedParent, err = filepath.Abs(resolvedParent)
	if err != nil {
		return nil, fmt.Errorf("resolve checkpoint parent absolutely: %w", err)
	}
	if err := validateSourceAncestorChain(resolvedParent); err != nil {
		return nil, err
	}
	parentFile, err := os.Open(resolvedParent)
	if err != nil {
		return nil, fmt.Errorf("open checkpoint parent: %w", err)
	}
	failParent := func(cause error) (*sourceCheckpoint, error) {
		_ = parentFile.Close()
		return nil, cause
	}
	parentInfo, err := parentFile.Stat()
	parentPathInfo, pathErr := os.Stat(resolvedParent)
	owner, ownerOK := sourceOwner(parentInfo)
	if err != nil || pathErr != nil || !parentInfo.IsDir() || !os.SameFile(parentInfo, parentPathInfo) ||
		parentInfo.Mode().Perm() != 0o700 || !ownerOK || int(owner) != os.Geteuid() {
		return failParent(errors.New("checkpoint parent must be a stable effective-UID-owned mode-0700 directory"))
	}
	resolvedPath := filepath.Join(resolvedParent, filepath.Base(absolute))
	resolvedInfo, err := os.Lstat(resolvedPath)
	if err != nil || !os.SameFile(visibleInfo, resolvedInfo) {
		return failParent(errors.New("checkpoint path changed while resolving its private parent"))
	}
	if err := rejectSourceCompanions(resolvedPath); err != nil {
		return failParent(err)
	}
	file, err := os.OpenFile(resolvedPath, os.O_RDONLY, 0)
	if err != nil {
		return failParent(fmt.Errorf("open checkpoint read-only: %w", err))
	}
	failFile := func(cause error) (*sourceCheckpoint, error) {
		_ = file.Close()
		_ = parentFile.Close()
		return nil, cause
	}
	openedInfo, err := file.Stat()
	pathInfo, pathErr := os.Lstat(resolvedPath)
	if err != nil || pathErr != nil || !os.SameFile(resolvedInfo, openedInfo) || !os.SameFile(resolvedInfo, pathInfo) {
		return failFile(errors.New("checkpoint path or inode changed while being pinned"))
	}
	if err := validateSourceRegularFile(openedInfo); err != nil {
		return failFile(err)
	}
	checkpoint := &sourceCheckpoint{
		path: resolvedPath, parentPath: resolvedParent, parentFile: parentFile, parentInfo: parentInfo,
		file: file, fileInfo: openedInfo, byteCount: openedInfo.Size(),
	}
	checkpoint.sha256, err = hashPinnedCheckpoint(file, openedInfo.Size())
	if err != nil {
		return failFile(err)
	}
	if err := checkpoint.verifyFile("before SQLite open"); err != nil {
		return failFile(err)
	}
	fdPath := fmt.Sprintf("/dev/fd/%d", file.Fd())
	checkpoint.operationalPath = fdPath
	databaseURL := &url.URL{Scheme: "file", Path: fdPath}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "trusted_schema(0)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "temp_store(MEMORY)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return failFile(fmt.Errorf("open checkpoint SQLite handle: %w", err))
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	checkpoint.database = database
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		checkpoint.database = nil
		return failFile(fmt.Errorf("open checkpoint through pinned read-only descriptor: %w", err))
	}
	if err := checkpoint.verifyFile("after SQLite open"); err != nil {
		_ = checkpoint.Close()
		return nil, err
	}
	return checkpoint, nil
}

func (checkpoint *sourceCheckpoint) Close() error {
	if checkpoint == nil {
		return nil
	}
	var result error
	if checkpoint.database != nil {
		result = errors.Join(result, checkpoint.database.Close())
		checkpoint.database = nil
	}
	if checkpoint.file != nil {
		result = errors.Join(result, checkpoint.file.Close())
		checkpoint.file = nil
	}
	if checkpoint.parentFile != nil {
		result = errors.Join(result, checkpoint.parentFile.Close())
		checkpoint.parentFile = nil
	}
	return result
}

func (checkpoint *sourceCheckpoint) verifyFile(stage string) error {
	if checkpoint == nil || checkpoint.file == nil || checkpoint.parentFile == nil {
		return errors.New("checkpoint is not pinned")
	}
	opened, openedErr := checkpoint.file.Stat()
	pathInfo, pathErr := os.Lstat(checkpoint.path)
	parentOpened, parentErr := checkpoint.parentFile.Stat()
	parentPathInfo, parentPathErr := os.Stat(checkpoint.parentPath)
	if openedErr != nil || pathErr != nil || parentErr != nil || parentPathErr != nil ||
		!os.SameFile(checkpoint.fileInfo, opened) || !os.SameFile(checkpoint.fileInfo, pathInfo) ||
		!os.SameFile(checkpoint.parentInfo, parentOpened) || !os.SameFile(checkpoint.parentInfo, parentPathInfo) ||
		opened.Size() != checkpoint.byteCount {
		return fmt.Errorf("checkpoint path, inode, parent, or size changed %s", stage)
	}
	if err := validateSourceRegularFile(opened); err != nil {
		return fmt.Errorf("checkpoint file changed %s: %w", stage, err)
	}
	owner, ownerOK := sourceOwner(parentOpened)
	if !parentOpened.IsDir() || parentOpened.Mode().Perm() != 0o700 || !ownerOK || int(owner) != os.Geteuid() {
		return fmt.Errorf("checkpoint parent mode or owner changed %s", stage)
	}
	if err := validateSourceAncestorChain(checkpoint.parentPath); err != nil {
		return fmt.Errorf("checkpoint ancestry changed %s: %w", stage, err)
	}
	if err := rejectSourceCompanions(checkpoint.path); err != nil {
		return fmt.Errorf("checkpoint companion paths changed %s: %w", stage, err)
	}
	return nil
}

func (checkpoint *sourceCheckpoint) verifyDigest(stage string) error {
	if err := checkpoint.verifyFile("before " + stage + " digest verification"); err != nil {
		return err
	}
	digest, err := hashPinnedCheckpoint(checkpoint.file, checkpoint.byteCount)
	if err != nil {
		return fmt.Errorf("rehash checkpoint %s: %w", stage, err)
	}
	if digest != checkpoint.sha256 {
		return fmt.Errorf("checkpoint SHA-256 changed %s", stage)
	}
	return checkpoint.verifyFile("after " + stage + " digest verification")
}

func (checkpoint *sourceCheckpoint) rejectDestinationAlias(destination string) error {
	if destination == "" || strings.TrimSpace(destination) != destination {
		return errors.New("destination root is required without surrounding whitespace")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve destination root: %w", err)
	}
	if err := validateSourceAncestorChain(filepath.Dir(absolute)); err != nil {
		return err
	}
	if err := validateDirectParentPath(filepath.Dir(absolute), "destination"); err != nil {
		return err
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return fmt.Errorf("resolve destination parent: %w", err)
	}
	resolved := filepath.Join(resolvedParent, filepath.Base(absolute))
	for _, protected := range sourcePathFamily(checkpoint.path) {
		if filepath.Clean(resolved) == filepath.Clean(protected) {
			return errors.New("destination root must not alias the checkpoint or a protected companion path")
		}
	}
	if info, err := os.Stat(resolved); err == nil {
		for _, protected := range sourcePathFamily(checkpoint.path) {
			if protectedInfo, protectedErr := os.Stat(protected); protectedErr == nil && os.SameFile(info, protectedInfo) {
				return errors.New("destination root resolves to the checkpoint or a protected companion inode")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination root: %w", err)
	}
	return nil
}

func validateDirectParentPath(path, label string) error {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("inspect %s direct parent: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s path must have a direct directory parent", label)
	}
	return nil
}

func validateSourceRegularFile(info os.FileInfo) error {
	owner, ownerOK := sourceOwner(info)
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		!ownerOK || int(owner) != os.Geteuid() || sourceLinkCount(info) != 1 || info.Size() <= 0 || info.Size() > maxSourceCheckpointBytes {
		return errors.New("checkpoint must be a direct effective-UID-owned mode-0600 unaliased regular file within its byte bound")
	}
	return nil
}

func validateSourceAncestorChain(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	root := volume + string(os.PathSeparator)
	relative := strings.TrimPrefix(clean, root)
	current := root
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect checkpoint ancestry: %w", err)
		}
		owner, ownerOK := sourceOwner(info)
		if !ownerOK || owner != 0 && int(owner) != os.Geteuid() {
			return errors.New("checkpoint ancestry is not owned by root or the effective UID")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if owner != 0 {
				return errors.New("checkpoint ancestry contains an untrusted symlink")
			}
			continue
		}
		if !info.IsDir() {
			return errors.New("checkpoint ancestry is not a directory chain")
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return errors.New("checkpoint ancestry is writable by an untrusted local UID")
		}
	}
	return nil
}

func rejectSourceCompanions(path string) error {
	for _, companion := range sourcePathFamily(path)[1:] {
		if _, err := os.Lstat(companion); err == nil {
			return errors.New("checkpoint has an unexpected anchor, hard-link alias, or SQLite sidecar")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect checkpoint companion path: %w", err)
		}
	}
	return nil
}

func sourcePathFamily(path string) []string {
	baseDigest := sha256.Sum256([]byte(filepath.Base(path)))
	anchor := filepath.Join(filepath.Dir(path), ".lyrics-preflight-"+hex.EncodeToString(baseDigest[:16])+".anchor")
	result := []string{path, anchor}
	for _, base := range []string{path, anchor} {
		for _, suffix := range []string{"-journal", "-wal", "-shm"} {
			result = append(result, base+suffix)
		}
	}
	return result
}

func hashPinnedCheckpoint(file *os.File, expectedSize int64) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek checkpoint for hashing: %w", err)
	}
	digest := sha256.New()
	count, err := io.Copy(digest, io.LimitReader(file, maxSourceCheckpointBytes+1))
	if err != nil {
		return "", fmt.Errorf("hash checkpoint: %w", err)
	}
	if count != expectedSize || count <= 0 || count > maxSourceCheckpointBytes {
		return "", errors.New("checkpoint size changed while hashing")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind checkpoint after hashing: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashFrame(destination hash.Hash, body []byte) {
	var length [8]byte
	for index := 0; index < len(length); index++ {
		length[len(length)-1-index] = byte(uint64(len(body)) >> (index * 8))
	}
	_, _ = destination.Write(length[:])
	_, _ = destination.Write(body)
}

func sourceOwner(info os.FileInfo) (uint32, bool) {
	if info == nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}

func sourceLinkCount(info os.FileInfo) uint64 {
	if info == nil {
		return 0
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Nlink)
}
