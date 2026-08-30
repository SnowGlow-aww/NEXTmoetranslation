package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"moesekai/server/internal/lyricsextractionplan"
	"moesekai/server/internal/lyricsimportreceipt"
	"moesekai/server/internal/lyricsrecovery"
	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/lyricsstaging"
)

const (
	releaseValidationReceiptSchemaVersion = lyricsimportreceipt.ValidationReceiptSchemaVersion
	releaseValidationReceiptKind          = lyricsimportreceipt.ValidationReceiptKind

	maxValidationTreeFiles    = 300_000
	maxValidationTreeBytes    = int64(128 << 30)
	maxValidationTreeFileSize = int64(64 << 20)
)

type validationFileBinding = lyricsimportreceipt.ValidationFileBinding
type validationTreeBinding = lyricsimportreceipt.ValidationTreeBinding
type validationPlanBinding = lyricsimportreceipt.ValidationPlanBinding
type validationCatalogBinding = lyricsimportreceipt.ValidationCatalogBinding
type validationAcquisitionSetBinding = lyricsimportreceipt.ValidationAcquisitionSetBinding
type validationEvidencePackBinding = lyricsimportreceipt.ValidationEvidencePackBinding
type validationRootBinding = lyricsimportreceipt.ValidationRootBinding
type validationImportManifestBinding = lyricsimportreceipt.ValidationImportManifestBinding
type validationImportEvidenceBinding = lyricsimportreceipt.ValidationImportEvidenceBinding
type releaseValidationReceipt = lyricsimportreceipt.ValidationReceipt

type validatedReleaseBundle struct {
	Validation           releaseValidationReceipt
	ValidationFileSHA256 string
	Bindings             releaseBindings
}

func buildReleaseValidationReceipt(
	opts freshOptions,
	plan lyricsextractionplan.RecoveryPlan,
	planFileSHA256 string,
	planBytes int64,
	catalogBytes int64,
	set lyricsrecovery.AcquisitionSet,
	setFileSHA256 string,
	setBytes int64,
	root lyricsrootmanifest.Manifest,
	rootFileSHA256 string,
	rootBytes int64,
	manifest lyricsstaging.Manifest,
	manifestFileSHA256 string,
	manifestBytes int64,
	importEvidence lyricsstaging.PrivateEvidenceReceipt,
	importEvidenceFileSHA256 string,
	importEvidenceBytes int64,
	ledger validationTreeBinding,
	providerOutcomes validationTreeBinding,
	songResults validationTreeBinding,
	evidencePack validationTreeBinding,
	acquisitionCount int,
) (releaseValidationReceipt, error) {
	receipt := releaseValidationReceipt{
		SchemaVersion: releaseValidationReceiptSchemaVersion,
		Kind:          releaseValidationReceiptKind,
		Plan: validationPlanBinding{
			File:   validationFileBinding{Path: opts.PlanPath, SHA256: planFileSHA256, ByteCount: planBytes},
			PlanID: plan.PlanID, PlanSHA256: planFileSHA256, SourceRoot: opts.SourceRoot,
			SourceSnapshotSHA256: plan.SourceSnapshot.SHA256, SourceFileCount: len(plan.SourceSnapshot.Files),
		},
		Catalog: validationCatalogBinding{
			File:        validationFileBinding{Path: opts.CatalogPath, SHA256: plan.Catalog.SourceSHA256, ByteCount: catalogBytes},
			RecordCount: plan.Catalog.RecordCount, IdentitySHA256: plan.Catalog.IdentitySHA256,
			MusicIDsSHA256: plan.Catalog.MusicIDsSHA256, IdentityPolicyVersion: plan.Catalog.IdentityPolicyVersion,
		},
		Ledger: ledger,
		AcquisitionSet: validationAcquisitionSetBinding{
			File:      validationFileBinding{Path: opts.AcquisitionSetPath, SHA256: setFileSHA256, ByteCount: setBytes},
			SetSHA256: set.SetSHA256,
		},
		ProviderOutcomes: providerOutcomes,
		SongResults:      songResults,
		EvidencePack: validationEvidencePackBinding{
			Tree: evidencePack, PackSHA256: root.EvidencePack.PackSHA256,
			SelectionSHA256: root.EvidencePack.SelectionSHA256,
			EvidenceCount:   root.EvidencePack.ItemCount, ShardCount: root.EvidencePack.ShardCount,
		},
		RootManifest: validationRootBinding{
			File:   validationFileBinding{Path: opts.RootManifestPath, SHA256: rootFileSHA256, ByteCount: rootBytes},
			RootID: root.RootID, RootSHA256: root.RootSHA256,
		},
		ImportManifest: validationImportManifestBinding{
			File:        validationFileBinding{Path: opts.ImportManifestPath, SHA256: manifestFileSHA256, ByteCount: manifestBytes},
			BatchSHA256: manifest.BatchSHA256, ItemCount: len(manifest.Items),
		},
		ImportEvidence: validationImportEvidenceBinding{
			File:          validationFileBinding{Path: opts.ImportEvidencePath, SHA256: importEvidenceFileSHA256, ByteCount: importEvidenceBytes},
			ReceiptSHA256: importEvidence.ReceiptSHA256, EvidenceCount: len(importEvidence.IndexEvidence),
		},
		AcquisitionCount:     acquisitionCount,
		ProviderOutcomeCount: root.Coverage.ProviderOutcomeRefCount,
		ReceiptPath:          opts.ValidationReceiptPath,
	}
	digest, err := releaseValidationReceiptDigest(receipt)
	if err != nil {
		return releaseValidationReceipt{}, err
	}
	receipt.ReceiptSHA256 = digest
	if err := validateReleaseValidationReceipt(receipt, opts.ValidationReceiptPath); err != nil {
		return releaseValidationReceipt{}, err
	}
	return receipt, nil
}

func validateReleaseValidationReceipt(receipt releaseValidationReceipt, path string) error {
	if receipt.SchemaVersion != releaseValidationReceiptSchemaVersion || receipt.Kind != releaseValidationReceiptKind ||
		receipt.ReceiptPath != path || !canonicalAbsolutePath(receipt.ReceiptPath) ||
		receipt.Plan.PlanID == "" || receipt.Plan.PlanSHA256 != receipt.Plan.File.SHA256 ||
		receipt.Catalog.File.Path != releaseCatalogPath || receipt.Catalog.File.SHA256 != releaseCatalogSHA256 ||
		receipt.Catalog.RecordCount != releaseCatalogTargetCount || receipt.Catalog.MusicIDsSHA256 != releaseCatalogMusicIDsSHA256 ||
		receipt.ImportManifest.ItemCount != releaseCatalogTargetCount || receipt.SongResults.FileCount != releaseCatalogTargetCount ||
		receipt.ProviderOutcomeCount != receipt.ProviderOutcomes.FileCount ||
		receipt.EvidencePack.EvidenceCount != receipt.ImportEvidence.EvidenceCount ||
		receipt.EvidencePack.ShardCount+1 != receipt.EvidencePack.Tree.FileCount ||
		receipt.AcquisitionCount <= 0 || receipt.ProviderOutcomeCount <= 0 ||
		!validValidationFile(receipt.Plan.File) || !canonicalAbsolutePath(receipt.Plan.SourceRoot) ||
		!lowerSHA256Pattern.MatchString(receipt.Plan.SourceSnapshotSHA256) || receipt.Plan.SourceFileCount <= 0 ||
		!validValidationFile(receipt.Catalog.File) || !lowerSHA256Pattern.MatchString(receipt.Catalog.IdentitySHA256) ||
		receipt.Catalog.IdentityPolicyVersion == "" || !validValidationTree(receipt.Ledger) ||
		!validValidationFile(receipt.AcquisitionSet.File) || !lowerSHA256Pattern.MatchString(receipt.AcquisitionSet.SetSHA256) ||
		!validValidationTree(receipt.ProviderOutcomes) || !validValidationTree(receipt.SongResults) ||
		!validValidationTree(receipt.EvidencePack.Tree) || !lowerSHA256Pattern.MatchString(receipt.EvidencePack.PackSHA256) ||
		!lowerSHA256Pattern.MatchString(receipt.EvidencePack.SelectionSHA256) || receipt.EvidencePack.EvidenceCount <= 0 ||
		receipt.EvidencePack.ShardCount <= 0 || !validValidationFile(receipt.RootManifest.File) ||
		receipt.RootManifest.RootID == "" || !lowerSHA256Pattern.MatchString(receipt.RootManifest.RootSHA256) ||
		!validValidationFile(receipt.ImportManifest.File) || !lowerSHA256Pattern.MatchString(receipt.ImportManifest.BatchSHA256) ||
		!validValidationFile(receipt.ImportEvidence.File) || !lowerSHA256Pattern.MatchString(receipt.ImportEvidence.ReceiptSHA256) ||
		receipt.ImportEvidence.EvidenceCount <= 0 || !lowerSHA256Pattern.MatchString(receipt.ReceiptSHA256) {
		return errors.New("release validation receipt does not satisfy the exact closed 698 bundle contract")
	}
	paths := []string{
		receipt.Plan.File.Path, receipt.Plan.SourceRoot, receipt.Catalog.File.Path, receipt.Ledger.Path,
		receipt.AcquisitionSet.File.Path, receipt.ProviderOutcomes.Path, receipt.SongResults.Path,
		receipt.EvidencePack.Tree.Path, receipt.RootManifest.File.Path, receipt.ImportManifest.File.Path,
		receipt.ImportEvidence.File.Path, receipt.ReceiptPath,
	}
	seen := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		if _, duplicate := seen[candidate]; duplicate {
			return errors.New("release validation receipt aliases distinct bundle inputs")
		}
		seen[candidate] = struct{}{}
	}
	directories := []string{
		receipt.Plan.SourceRoot, receipt.Ledger.Path, receipt.ProviderOutcomes.Path,
		receipt.SongResults.Path, receipt.EvidencePack.Tree.Path,
	}
	for _, directory := range directories {
		for _, candidate := range paths {
			if candidate != directory && pathInsideDirectory(directory, candidate) {
				return errors.New("release validation receipt nests a distinct bundle input inside a bound tree")
			}
		}
	}
	digest, err := releaseValidationReceiptDigest(receipt)
	if err != nil || digest != receipt.ReceiptSHA256 {
		return errors.New("release validation receipt digest does not match")
	}
	return nil
}

func pathInsideDirectory(directory, candidate string) bool {
	relative, err := filepath.Rel(directory, candidate)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validValidationFile(binding validationFileBinding) bool {
	return canonicalAbsolutePath(binding.Path) && lowerSHA256Pattern.MatchString(binding.SHA256) && binding.ByteCount > 0
}

func validValidationTree(binding validationTreeBinding) bool {
	return canonicalAbsolutePath(binding.Path) && lowerSHA256Pattern.MatchString(binding.SHA256) &&
		binding.FileCount > 0 && binding.FileCount <= maxValidationTreeFiles &&
		binding.ByteCount > 0 && binding.ByteCount <= maxValidationTreeBytes
}

func releaseValidationReceiptDigest(receipt releaseValidationReceipt) (string, error) {
	return lyricsimportreceipt.ValidationReceiptDigest(receipt)
}

func marshalReleaseValidationReceipt(receipt releaseValidationReceipt) ([]byte, error) {
	if err := validateReleaseValidationReceipt(receipt, receipt.ReceiptPath); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func publishReleaseValidationReceipt(receipt releaseValidationReceipt) error {
	body, err := marshalReleaseValidationReceipt(receipt)
	if err != nil {
		return err
	}
	parent := filepath.Dir(receipt.ReceiptPath)
	if err := validatePrivateDirectory(parent, "validation receipt parent"); err != nil {
		return err
	}
	file, err := os.OpenFile(receipt.ReceiptPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("release validation receipt path already exists; historical validation receipts are immutable")
	}
	if err != nil {
		return fmt.Errorf("create release validation receipt: %w", err)
	}
	created := true
	defer func() {
		if created {
			_ = file.Close()
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write release validation receipt: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync release validation receipt: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close release validation receipt: %w", err)
	}
	created = false
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	loaded, _, err := loadReleaseValidationReceipt(receipt.ReceiptPath)
	if err != nil {
		return err
	}
	if loaded.ReceiptSHA256 != receipt.ReceiptSHA256 {
		return errors.New("published release validation receipt changed")
	}
	return nil
}

func loadReleaseValidationReceipt(path string) (releaseValidationReceipt, string, error) {
	body, fileSHA256, err := readPinnedRegular(path, "release validation receipt", maxReceiptBytes, 0o600)
	if err != nil {
		return releaseValidationReceipt{}, "", err
	}
	receipt, err := lyricsimportreceipt.DecodeValidationReceipt(body, path)
	if err != nil {
		return releaseValidationReceipt{}, "", err
	}
	if err := validateReleaseValidationReceipt(receipt, path); err != nil {
		return releaseValidationReceipt{}, "", err
	}
	canonical, err := marshalReleaseValidationReceipt(receipt)
	if err != nil || !bytes.Equal(canonical, body) {
		return releaseValidationReceipt{}, "", errors.New("release validation receipt is not the canonical producer encoding")
	}
	return receipt, fileSHA256, nil
}

func loadValidatedReleaseBundle(validationPath, rootPath, manifestPath string) (validatedReleaseBundle, error) {
	validation, validationFileSHA256, err := loadReleaseValidationReceipt(validationPath)
	if err != nil {
		return validatedReleaseBundle{}, err
	}
	if rootPath != validation.RootManifest.File.Path || manifestPath != validation.ImportManifest.File.Path {
		return validatedReleaseBundle{}, errors.New("release paths do not match the exact validation receipt")
	}
	bindings, err := loadReleaseBindings(rootPath, manifestPath)
	if err != nil {
		return validatedReleaseBundle{}, err
	}
	if bindings.RootFileSHA256 != validation.RootManifest.File.SHA256 ||
		bindings.RootFileBytes != validation.RootManifest.File.ByteCount ||
		bindings.ManifestFileSHA256 != validation.ImportManifest.File.SHA256 ||
		bindings.ManifestFileBytes != validation.ImportManifest.File.ByteCount ||
		bindings.Root.RootID != validation.RootManifest.RootID ||
		bindings.Root.RootSHA256 != validation.RootManifest.RootSHA256 ||
		bindings.Root.Plan.PlanID != validation.Plan.PlanID || bindings.Root.Plan.SHA256 != validation.Plan.PlanSHA256 ||
		bindings.Root.Catalog.SourceSHA256 != validation.Catalog.File.SHA256 ||
		bindings.Root.Catalog.RecordCount != validation.Catalog.RecordCount ||
		bindings.Root.Catalog.IdentitySHA256 != validation.Catalog.IdentitySHA256 ||
		bindings.Root.Catalog.MusicIDsSHA256 != validation.Catalog.MusicIDsSHA256 ||
		bindings.Root.Catalog.IdentityPolicyVersion != validation.Catalog.IdentityPolicyVersion ||
		bindings.Root.Coverage.UniqueAcquisitionCount != validation.AcquisitionCount ||
		bindings.Root.Coverage.ProviderOutcomeRefCount != validation.ProviderOutcomeCount ||
		bindings.Root.Coverage.UniqueEvidenceCount != validation.EvidencePack.EvidenceCount ||
		bindings.Root.EvidencePack.PackSHA256 != validation.EvidencePack.PackSHA256 ||
		bindings.Root.EvidencePack.SelectionSHA256 != validation.EvidencePack.SelectionSHA256 ||
		bindings.Root.EvidencePack.ItemCount != validation.EvidencePack.EvidenceCount ||
		bindings.Root.EvidencePack.ShardCount != validation.EvidencePack.ShardCount ||
		bindings.Manifest.BatchSHA256 != validation.ImportManifest.BatchSHA256 ||
		len(bindings.Manifest.Items) != validation.ImportManifest.ItemCount {
		return validatedReleaseBundle{}, errors.New("release files do not match the exact validated content-addressed bundle")
	}
	return validatedReleaseBundle{Validation: validation, ValidationFileSHA256: validationFileSHA256, Bindings: bindings}, nil
}

func validateBoundImportEvidence(
	bundle validatedReleaseBundle,
	path, fileSHA256 string,
	byteCount int64,
	receipt lyricsstaging.PrivateEvidenceReceipt,
) error {
	binding := bundle.Validation.ImportEvidence
	if path != binding.File.Path || fileSHA256 != binding.File.SHA256 || byteCount != binding.File.ByteCount ||
		receipt.ReceiptSHA256 != binding.ReceiptSHA256 || len(receipt.IndexEvidence) != binding.EvidenceCount {
		return errors.New("import evidence does not match the exact validation receipt")
	}
	return nil
}

func hashPrivateTree(path, label string) (validationTreeBinding, error) {
	if err := validatePrivateDirectory(path, label); err != nil {
		return validationTreeBinding{}, err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("moesekai-release-validation-private-tree-v1\x00"))
	fileCount := 0
	byteCount := int64(0)
	var walk func(string, string) error
	walk = func(directoryPath, relative string) error {
		if err := validatePrivateDirectory(directoryPath, label); err != nil {
			return err
		}
		directory, err := os.Open(directoryPath)
		if err != nil {
			return err
		}
		opened, err := directory.Stat()
		if err != nil {
			_ = directory.Close()
			return err
		}
		entries, err := directory.ReadDir(-1)
		if err != nil {
			_ = directory.Close()
			return err
		}
		sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
		_, _ = digest.Write([]byte("D\x00" + relative + "\x00"))
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
			name := entry.Name()
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
				_ = directory.Close()
				return fmt.Errorf("%s contains an invalid entry name", label)
			}
			entryPath := filepath.Join(directoryPath, name)
			entryRelative := name
			if relative != "" {
				entryRelative = filepath.Join(relative, name)
			}
			info, err := os.Lstat(entryPath)
			if err != nil {
				_ = directory.Close()
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				_ = directory.Close()
				return fmt.Errorf("%s contains a symlink", label)
			}
			if info.IsDir() {
				if err := walk(entryPath, entryRelative); err != nil {
					_ = directory.Close()
					return err
				}
				continue
			}
			if !validDirectRegular(info, 0o600) || info.Size() <= 0 || info.Size() > maxValidationTreeFileSize {
				_ = directory.Close()
				return fmt.Errorf("%s contains an invalid private regular file", label)
			}
			fileSHA256, size, _, err := hashPinnedRegular(entryPath, label+" file", maxValidationTreeFileSize, 0o600)
			if err != nil {
				_ = directory.Close()
				return err
			}
			fileCount++
			if fileCount > maxValidationTreeFiles || size > maxValidationTreeBytes-byteCount {
				_ = directory.Close()
				return fmt.Errorf("%s exceeds its aggregate validation bound", label)
			}
			byteCount += size
			_, _ = digest.Write([]byte("F\x00" + entryRelative + "\x00" + strconv.FormatInt(size, 10) + "\x00" + fileSHA256 + "\x00"))
		}
		if _, err := directory.Seek(0, 0); err != nil {
			_ = directory.Close()
			return err
		}
		afterEntries, err := directory.ReadDir(-1)
		if err != nil {
			_ = directory.Close()
			return err
		}
		afterNames := make([]string, len(afterEntries))
		for index, entry := range afterEntries {
			afterNames[index] = entry.Name()
		}
		sort.Strings(afterNames)
		current, currentErr := os.Lstat(directoryPath)
		closeErr := directory.Close()
		if currentErr != nil || closeErr != nil || !os.SameFile(opened, current) || !equalStringSlices(names, afterNames) {
			return errors.Join(fmt.Errorf("%s changed while being content-addressed", label), currentErr, closeErr)
		}
		return nil
	}
	if err := walk(path, ""); err != nil {
		return validationTreeBinding{}, err
	}
	if fileCount == 0 || byteCount == 0 {
		return validationTreeBinding{}, fmt.Errorf("%s is empty", label)
	}
	return validationTreeBinding{Path: path, SHA256: hex.EncodeToString(digest.Sum(nil)), FileCount: fileCount, ByteCount: byteCount}, nil
}

func validationFileFromPath(path, label string, maximum int64, allowedPerms ...os.FileMode) (validationFileBinding, error) {
	digest, size, _, err := hashPinnedRegular(path, label, maximum, allowedPerms...)
	if err != nil {
		return validationFileBinding{}, err
	}
	return validationFileBinding{Path: path, SHA256: digest, ByteCount: size}, nil
}
