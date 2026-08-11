package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	migrationManifestFilename = "migration-manifest.json"
	maxMigrationManifestBytes = 64 << 10
)

func publishMigrationManifest(rootPath string, body []byte) (string, error) {
	var decoded migrationManifest
	if len(body) == 0 || len(body) > maxMigrationManifestBytes || decodeCanonicalJSON(body, &decoded) != nil ||
		decoded.SchemaVersion != 1 || decoded.ImportedAcquisitionCount <= 0 ||
		!canonicalDigest.MatchString(decoded.Checkpoint.CheckpointSHA256) ||
		!canonicalDigest.MatchString(decoded.Checkpoint.EvidenceRowsSHA256) ||
		!canonicalDigest.MatchString(decoded.AcquisitionIDsSHA256) {
		return "", errors.New("migration manifest is not canonical counts-and-digests JSON")
	}
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return "", fmt.Errorf("resolve migration root: %w", err)
	}
	rootInfo, err := os.Lstat(absolute)
	owner, ownerOK := sourceOwner(rootInfo)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 ||
		!ownerOK || int(owner) != os.Geteuid() {
		return "", errors.New("migration root must remain a direct effective-UID-owned mode-0700 directory")
	}
	root, err := os.Open(absolute)
	if err != nil {
		return "", fmt.Errorf("open migration root for manifest publication: %w", err)
	}
	defer root.Close()
	openedRootInfo, err := root.Stat()
	pathRootInfo, pathErr := os.Lstat(absolute)
	if err != nil || pathErr != nil || !os.SameFile(rootInfo, openedRootInfo) || !os.SameFile(rootInfo, pathRootInfo) {
		return "", errors.New("migration root changed while being pinned for manifest publication")
	}
	path := filepath.Join(absolute, migrationManifestFilename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", errors.New("migration manifest already exists; refusing overwrite")
		}
		return "", fmt.Errorf("create migration manifest exclusively: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure migration manifest: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return "", fmt.Errorf("write migration manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync migration manifest: %w", err)
	}
	createdInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect migration manifest: %w", err)
	}
	createdOwner, createdOwnerOK := sourceOwner(createdInfo)
	if !createdInfo.Mode().IsRegular() || createdInfo.Mode().Perm() != 0o600 || !createdOwnerOK ||
		int(createdOwner) != os.Geteuid() || sourceLinkCount(createdInfo) != 1 || createdInfo.Size() != int64(len(body)) {
		return "", errors.New("migration manifest inode, mode, link count, or byte count is invalid")
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close migration manifest: %w", err)
	}
	closed = true
	if err := root.Sync(); err != nil {
		return "", fmt.Errorf("sync migration root after manifest publication: %w", err)
	}
	if err := verifyPublishedMigrationManifest(path, createdInfo, body); err != nil {
		return "", err
	}
	return sha256Hex(body), nil
}

func verifyPublishedMigrationManifest(path string, expectedInfo os.FileInfo, expectedBody []byte) error {
	before, err := os.Lstat(path)
	if err != nil || !os.SameFile(expectedInfo, before) {
		return errors.New("migration manifest path changed after publication")
	}
	owner, ownerOK := sourceOwner(before)
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || !ownerOK || int(owner) != os.Geteuid() ||
		sourceLinkCount(before) != 1 || before.Size() != int64(len(expectedBody)) {
		return errors.New("published migration manifest mode or inode is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open published migration manifest: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, pathInfo) {
		return errors.New("migration manifest path or inode changed while rereading")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxMigrationManifestBytes+1))
	if err != nil {
		return fmt.Errorf("reread migration manifest: %w", err)
	}
	if !bytes.Equal(body, expectedBody) || sha256Hex(body) != sha256Hex(expectedBody) {
		return errors.New("published migration manifest bytes changed")
	}
	return nil
}
