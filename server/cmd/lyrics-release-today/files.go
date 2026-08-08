package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"moesekai/server/internal/legacy"
)

const (
	maxReceiptBytes        = 4 << 20
	maxFreshRootBytes      = 4 << 20
	maxDeploymentProbeBody = 4 << 10
	maxCiphertextBytes     = int64(32 << 30)
	maxReleaseJSONDepth    = 32
)

var (
	lowerSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	artifactDigestRE   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	compactIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
)

type pinnedFile struct {
	path string
	file *os.File
	info os.FileInfo
}

func openPinnedRegular(path, label string, allowedPerms ...os.FileMode) (*pinnedFile, error) {
	if !canonicalAbsolutePath(path) {
		return nil, fmt.Errorf("%s path must be canonical and absolute", label)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if !validDirectRegular(info, allowedPerms...) {
		return nil, fmt.Errorf("%s must be a direct effective-UID-owned single-link regular file with an allowed mode", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	pinned := &pinnedFile{path: path, file: file, info: info}
	if err := pinned.verify(label); err != nil {
		_ = file.Close()
		return nil, err
	}
	return pinned, nil
}

func (pinned *pinnedFile) verify(label string) error {
	if pinned == nil || pinned.file == nil || pinned.info == nil {
		return fmt.Errorf("%s is not pinned", label)
	}
	opened, err := pinned.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened %s: %w", label, err)
	}
	current, err := os.Lstat(pinned.path)
	if err != nil {
		return fmt.Errorf("reinspect %s: %w", label, err)
	}
	if !os.SameFile(pinned.info, opened) || !os.SameFile(opened, current) ||
		pinned.info.Mode() != opened.Mode() || opened.Mode() != current.Mode() ||
		pinned.info.Size() != opened.Size() || opened.Size() != current.Size() {
		return fmt.Errorf("%s path, inode, mode, or size changed during validation", label)
	}
	return nil
}

func (pinned *pinnedFile) close() error {
	if pinned == nil || pinned.file == nil {
		return nil
	}
	err := pinned.file.Close()
	pinned.file = nil
	return err
}

var testHookBeforePinnedRehash func(path, label string) error

func readPinnedRegular(path, label string, maximum int64, allowedPerms ...os.FileMode) ([]byte, string, error) {
	if maximum <= 0 {
		return nil, "", errors.New("positive file bound is required")
	}
	pinned, err := openPinnedRegular(path, label, allowedPerms...)
	if err != nil {
		return nil, "", err
	}
	defer pinned.close()
	if pinned.info.Size() <= 0 || pinned.info.Size() > maximum {
		return nil, "", fmt.Errorf("%s is empty or exceeds its byte bound", label)
	}
	body, err := io.ReadAll(io.NewSectionReader(pinned.file, 0, maximum+1))
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(body)) != pinned.info.Size() || int64(len(body)) > maximum {
		return nil, "", fmt.Errorf("%s changed or exceeded its byte bound while being read", label)
	}
	if err := pinned.verify(label); err != nil {
		return nil, "", err
	}
	first := sha256.Sum256(body)
	if testHookBeforePinnedRehash != nil {
		if err := testHookBeforePinnedRehash(path, label); err != nil {
			return nil, "", err
		}
	}
	second, size, _, err := hashPinnedDescriptor(pinned, label, maximum)
	if err != nil {
		return nil, "", err
	}
	firstSHA256 := hex.EncodeToString(first[:])
	if size != int64(len(body)) || second != firstSHA256 {
		return nil, "", fmt.Errorf("%s bytes changed during pinned validation", label)
	}
	return body, firstSHA256, nil
}

func hashPinnedRegular(path, label string, maximum int64, allowedPerms ...os.FileMode) (string, int64, []byte, error) {
	if maximum <= 0 {
		return "", 0, nil, errors.New("positive file bound is required")
	}
	pinned, err := openPinnedRegular(path, label, allowedPerms...)
	if err != nil {
		return "", 0, nil, err
	}
	defer pinned.close()
	first, size, prefix, err := hashPinnedDescriptor(pinned, label, maximum)
	if err != nil {
		return "", 0, nil, err
	}
	if testHookBeforePinnedRehash != nil {
		if err := testHookBeforePinnedRehash(path, label); err != nil {
			return "", 0, nil, err
		}
	}
	second, secondSize, secondPrefix, err := hashPinnedDescriptor(pinned, label, maximum)
	if err != nil {
		return "", 0, nil, err
	}
	if first != second || size != secondSize || !bytes.Equal(prefix, secondPrefix) {
		return "", 0, nil, fmt.Errorf("%s bytes changed during pinned validation", label)
	}
	return second, secondSize, secondPrefix, nil
}

func hashPinnedDescriptor(pinned *pinnedFile, label string, maximum int64) (string, int64, []byte, error) {
	if pinned == nil || pinned.info == nil || pinned.info.Size() <= 0 || pinned.info.Size() > maximum {
		return "", 0, nil, fmt.Errorf("%s is empty or exceeds its byte bound", label)
	}
	if err := pinned.verify(label); err != nil {
		return "", 0, nil, err
	}
	hash := sha256.New()
	prefix := make([]byte, 64)
	read, readErr := io.ReadFull(io.NewSectionReader(pinned.file, 0, int64(len(prefix))), prefix)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", 0, nil, fmt.Errorf("read %s prefix: %w", label, readErr)
	}
	prefix = prefix[:read]
	copied, err := io.Copy(hash, io.NewSectionReader(pinned.file, 0, maximum+1))
	if err != nil {
		return "", 0, nil, fmt.Errorf("hash %s: %w", label, err)
	}
	if copied != pinned.info.Size() || copied > maximum {
		return "", 0, nil, fmt.Errorf("%s changed or exceeded its byte bound while being hashed", label)
	}
	if err := pinned.verify(label); err != nil {
		return "", 0, nil, err
	}
	return hex.EncodeToString(hash.Sum(nil)), copied, prefix, nil
}

func validDirectRegular(info os.FileInfo, allowedPerms ...os.FileMode) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return false
	}
	if len(allowedPerms) == 0 {
		return true
	}
	for _, allowed := range allowedPerms {
		if info.Mode().Perm() == allowed.Perm() {
			return true
		}
	}
	return false
}

func validatePrivateDirectory(path, label string) error {
	if !canonicalAbsolutePath(path) {
		return fmt.Errorf("%s path must be canonical and absolute", label)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%s must be a direct private mode-0700 directory", label)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s must be owned by the effective UID", label)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("%s must not traverse a symlink or filesystem alias", label)
	}
	return nil
}

func exactRegularDirectoryEntries(path, label string, expected map[string]struct{}) error {
	if err := validatePrivateDirectory(path, label); err != nil {
		return err
	}
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", label, err)
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil || len(entries) != len(expected) {
		return fmt.Errorf("%s does not contain the exact declared file set", label)
	}
	for _, entry := range entries {
		if _, found := expected[entry.Name()]; !found || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s contains an orphan or nonregular entry", label)
		}
		info, err := entry.Info()
		if err != nil || !validDirectRegular(info, 0o600) {
			return fmt.Errorf("%s contains an invalid private file", label)
		}
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) {
		return fmt.Errorf("%s changed while being enumerated", label)
	}
	return nil
}

func exactLedgerManifestEntries(ledgerPath string, acquisitionIDs map[string]struct{}) error {
	manifestDirectory := filepath.Join(ledgerPath, "manifests")
	expected := make(map[string]struct{}, len(acquisitionIDs))
	for acquisitionID := range acquisitionIDs {
		expected[acquisitionID+".json"] = struct{}{}
	}
	return exactRegularDirectoryEntries(manifestDirectory, "acquisition ledger manifests", expected)
}

func validateReleaseJSONDepth(body []byte, label string) error {
	if len(body) == 0 {
		return fmt.Errorf("%s JSON is required", label)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect %s JSON depth: %w", label, err)
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delimiter {
		case '{', '[':
			depth++
			if depth > maxReleaseJSONDepth {
				return fmt.Errorf("%s JSON exceeds maximum nesting depth %d", label, maxReleaseJSONDepth)
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("%s JSON has invalid nesting", label)
			}
		}
	}
}

func decodeStrictJSON(body []byte, target any, label string) error {
	if len(body) == 0 || target == nil {
		return fmt.Errorf("%s JSON and target are required", label)
	}
	if err := validateReleaseJSONDepth(body, label); err != nil {
		return err
	}
	if err := legacy.ValidateUniqueJSON(body); err != nil {
		return fmt.Errorf("%s JSON: %w", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s contains trailing JSON", label)
	}
	return nil
}

func rejectJSONKeys(body []byte, prohibited map[string]struct{}, label string) error {
	if err := validateReleaseJSONDepth(body, label); err != nil {
		return err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	var walk func(any) bool
	walk = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if _, forbidden := prohibited[strings.ToLower(key)]; forbidden || walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	if walk(value) {
		return fmt.Errorf("%s contains a forbidden content-bearing field", label)
	}
	return nil
}

func canonicalAbsolutePath(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func canonicalTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !strings.HasSuffix(value, "Z") || parsed.UTC().Format(time.RFC3339Nano) != value || parsed.UnixNano() <= 0 {
		return time.Time{}, errors.New("timestamp must be positive canonical UTC RFC3339Nano")
	}
	return parsed, nil
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
