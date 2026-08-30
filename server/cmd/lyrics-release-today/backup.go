package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

const (
	encryptedBackupReceiptSchemaVersion = 2
	encryptedBackupReceiptKind          = "moesekai-lyrics-encrypted-offline-backup-v2"
)

type encryptedBackupReceipt struct {
	SchemaVersion                int    `json:"schemaVersion"`
	Kind                         string `json:"kind"`
	ValidationReceiptSHA256      string `json:"validationReceiptSha256"`
	RootSHA256                   string `json:"rootSha256"`
	ImportBatchSHA256            string `json:"importBatchSha256"`
	PlaintextDatabaseSHA256      string `json:"plaintextDatabaseSha256"`
	PlaintextDatabaseStateSHA256 string `json:"plaintextDatabaseStateSha256"`
	CiphertextSHA256             string `json:"ciphertextSha256"`
	CiphertextByteCount          int64  `json:"ciphertextByteCount"`
	EncryptionFormat             string `json:"encryptionFormat"`
	CreatedAt                    string `json:"createdAt"`
	VerifiedAt                   string `json:"verifiedAt"`
	IntegrityCheck               string `json:"integrityCheck"`
	RestoreCheck                 string `json:"restoreCheck"`
	Offline                      bool   `json:"offline"`
	OffsiteCopyCount             int    `json:"offsiteCopyCount"`
	ReceiptSHA256                string `json:"receiptSha256"`
}

type backupResult struct {
	RootSHA256        string
	ImportBatchSHA256 string
	CiphertextSHA256  string
	OffsiteCopyCount  int
}

func runCheckBackup(arguments []string) (backupResult, error) {
	var receiptPath, validationReceiptPath, rootPath, manifestPath, ciphertextPath string
	flags := flag.NewFlagSet("check-backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&receiptPath, "receipt", "", "encrypted offline-backup receipt")
	flags.StringVar(&validationReceiptPath, "validation-receipt", "", "exact fresh-validation receipt")
	flags.StringVar(&rootPath, "root-manifest", "", "validated final root manifest")
	flags.StringVar(&manifestPath, "import-manifest", "", "validated import manifest")
	flags.StringVar(&ciphertextPath, "ciphertext", "", "encrypted offline backup ciphertext")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return backupResult{}, errors.New("check-backup requires only -receipt, -validation-receipt, -root-manifest, -import-manifest, and -ciphertext")
	}
	for _, path := range []string{receiptPath, validationReceiptPath, rootPath, manifestPath, ciphertextPath} {
		if !canonicalAbsolutePath(path) {
			return backupResult{}, errors.New("check-backup paths must be explicit canonical absolute paths")
		}
	}
	result, _, _, err := checkEncryptedBackup(receiptPath, validationReceiptPath, rootPath, manifestPath, ciphertextPath)
	return result, err
}

func checkEncryptedBackup(
	receiptPath, validationReceiptPath, rootPath, manifestPath, ciphertextPath string,
) (backupResult, encryptedBackupReceipt, validatedReleaseBundle, error) {
	bundle, err := loadValidatedReleaseBundle(validationReceiptPath, rootPath, manifestPath)
	if err != nil {
		return backupResult{}, encryptedBackupReceipt{}, validatedReleaseBundle{}, err
	}
	body, _, err := readPinnedRegular(receiptPath, "encrypted backup receipt", maxReceiptBytes, 0o600)
	if err != nil {
		return backupResult{}, encryptedBackupReceipt{}, validatedReleaseBundle{}, err
	}
	var receipt encryptedBackupReceipt
	if err := decodeStrictJSON(body, &receipt, "encrypted backup receipt"); err != nil {
		return backupResult{}, encryptedBackupReceipt{}, validatedReleaseBundle{}, err
	}
	if err := validateEncryptedBackupReceipt(receipt, bundle); err != nil {
		return backupResult{}, encryptedBackupReceipt{}, validatedReleaseBundle{}, err
	}
	ciphertextSHA256, ciphertextBytes, prefix, err := hashPinnedRegular(
		ciphertextPath, "encrypted backup ciphertext", maxCiphertextBytes, 0o600,
	)
	if err != nil {
		return backupResult{}, encryptedBackupReceipt{}, validatedReleaseBundle{}, err
	}
	if ciphertextSHA256 != receipt.CiphertextSHA256 || ciphertextBytes != receipt.CiphertextByteCount {
		return backupResult{}, encryptedBackupReceipt{}, validatedReleaseBundle{}, errors.New("encrypted backup ciphertext does not match its receipt")
	}
	if err := validateCiphertextPrefix(receipt.EncryptionFormat, prefix); err != nil {
		return backupResult{}, encryptedBackupReceipt{}, validatedReleaseBundle{}, err
	}
	result := backupResult{
		RootSHA256: bundle.Bindings.Root.RootSHA256, ImportBatchSHA256: bundle.Bindings.Manifest.BatchSHA256,
		CiphertextSHA256: ciphertextSHA256, OffsiteCopyCount: receipt.OffsiteCopyCount,
	}
	return result, receipt, bundle, nil
}

func validateEncryptedBackupReceipt(receipt encryptedBackupReceipt, bundle validatedReleaseBundle) error {
	createdAt, err := canonicalTimestamp(receipt.CreatedAt)
	if err != nil {
		return fmt.Errorf("encrypted backup createdAt: %w", err)
	}
	verifiedAt, err := canonicalTimestamp(receipt.VerifiedAt)
	if err != nil || verifiedAt.Before(createdAt) {
		return errors.New("encrypted backup verifiedAt must be canonical and no earlier than createdAt")
	}
	if receipt.SchemaVersion != encryptedBackupReceiptSchemaVersion || receipt.Kind != encryptedBackupReceiptKind ||
		receipt.ValidationReceiptSHA256 != bundle.Validation.ReceiptSHA256 ||
		receipt.RootSHA256 != bundle.Bindings.Root.RootSHA256 ||
		receipt.ImportBatchSHA256 != bundle.Bindings.Manifest.BatchSHA256 ||
		!lowerSHA256Pattern.MatchString(receipt.PlaintextDatabaseSHA256) ||
		!lowerSHA256Pattern.MatchString(receipt.PlaintextDatabaseStateSHA256) ||
		!lowerSHA256Pattern.MatchString(receipt.CiphertextSHA256) ||
		receipt.CiphertextByteCount <= 0 || receipt.CiphertextByteCount > maxCiphertextBytes ||
		(receipt.EncryptionFormat != "age-v1" && receipt.EncryptionFormat != "openpgp-armored") ||
		receipt.IntegrityCheck != "ok" || receipt.RestoreCheck != "ok" || !receipt.Offline ||
		receipt.OffsiteCopyCount < 1 || !lowerSHA256Pattern.MatchString(receipt.ReceiptSHA256) {
		return errors.New("encrypted backup receipt does not satisfy the closed verified offline-backup contract")
	}
	digest, err := encryptedBackupReceiptDigest(receipt)
	if err != nil || digest != receipt.ReceiptSHA256 {
		return errors.New("encrypted backup receipt digest does not match")
	}
	return nil
}

func encryptedBackupReceiptDigest(receipt encryptedBackupReceipt) (string, error) {
	receipt.ReceiptSHA256 = ""
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(encryptedBackupReceiptKind + "\x00"))
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateCiphertextPrefix(format string, prefix []byte) error {
	if bytes.HasPrefix(prefix, []byte("SQLite format 3\x00")) {
		return errors.New("backup ciphertext is an unencrypted SQLite database")
	}
	switch format {
	case "age-v1":
		if !bytes.HasPrefix(prefix, []byte("age-encryption.org/v1\n")) &&
			!bytes.HasPrefix(prefix, []byte("-----BEGIN AGE ENCRYPTED FILE-----\n")) {
			return errors.New("backup ciphertext does not have the declared age v1 envelope")
		}
	case "openpgp-armored":
		if !bytes.HasPrefix(prefix, []byte("-----BEGIN PGP MESSAGE-----\n")) {
			return errors.New("backup ciphertext does not have the declared armored OpenPGP envelope")
		}
	default:
		return errors.New("unsupported encrypted backup format")
	}
	return nil
}
