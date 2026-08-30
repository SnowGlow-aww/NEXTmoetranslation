package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	backupEncryptionKeyEnv  = "MOESEKAI_BACKUP_ENCRYPTION_KEY"
	backupEnvelopeFilename  = "backup.enc"
	backupEnvelopeMediaType = "application/octet-stream"

	backupEnvelopeMagic            = "MOEBAK01"
	backupEnvelopeVersion          = byte(1)
	backupEnvelopeKeyWrapAlgorithm = byte(1) // AES-256-GCM
	backupEnvelopeDataAlgorithm    = byte(1) // AES-256-GCM
	backupEnvelopeFlags            = byte(0)

	backupEnvelopeKeyIDSize             = 16
	backupEnvelopeNonceSize             = 12
	backupEnvelopeDataKeySize           = 32
	backupEnvelopeAuthenticationTagSize = 16

	backupEnvelopeVersionOffset       = len(backupEnvelopeMagic)
	backupEnvelopeKeyWrapOffset       = backupEnvelopeVersionOffset + 1
	backupEnvelopeDataAlgorithmOffset = backupEnvelopeKeyWrapOffset + 1
	backupEnvelopeFlagsOffset         = backupEnvelopeDataAlgorithmOffset + 1
	backupEnvelopeKeyIDOffset         = backupEnvelopeFlagsOffset + 1
	backupEnvelopeWrapNonceOffset     = backupEnvelopeKeyIDOffset + backupEnvelopeKeyIDSize
	backupEnvelopeDataNonceOffset     = backupEnvelopeWrapNonceOffset + backupEnvelopeNonceSize
	backupEnvelopePlaintextSizeOffset = backupEnvelopeDataNonceOffset + backupEnvelopeNonceSize
	backupEnvelopePrefixSize          = backupEnvelopePlaintextSizeOffset + 8
	backupEnvelopeWrappedKeySize      = backupEnvelopeDataKeySize + backupEnvelopeAuthenticationTagSize
	backupEnvelopeHeaderSize          = backupEnvelopePrefixSize + backupEnvelopeWrappedKeySize
	maxBackupEnvelopeBytes            = backupEnvelopeHeaderSize + maxS3ResponseBytes + backupEnvelopeAuthenticationTagSize
)

var ErrBackupEnvelopeAuthentication = errors.New("backup envelope authentication failed")

func loadBackupEncryptionKey() ([]byte, error) {
	raw, present := os.LookupEnv(backupEncryptionKeyEnv)
	if !present || raw == "" {
		return nil, nil
	}
	if raw != strings.TrimSpace(raw) {
		return nil, fmt.Errorf("invalid %s: surrounding whitespace is not allowed", backupEncryptionKeyEnv)
	}
	key, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || base64.StdEncoding.EncodeToString(key) != raw || len(key) != backupEnvelopeDataKeySize {
		clear(key)
		return nil, fmt.Errorf("invalid %s: must be the canonical base64 encoding of exactly 32 bytes", backupEncryptionKeyEnv)
	}
	return key, nil
}

func encryptBackupEnvelope(plaintext, keyEncryptionKey []byte) ([]byte, error) {
	if len(keyEncryptionKey) != backupEnvelopeDataKeySize {
		return nil, errors.New("backup key-encryption key must be 32 bytes")
	}
	if len(plaintext) > maxS3ResponseBytes {
		return nil, fmt.Errorf("backup plaintext exceeds %d bytes", maxS3ResponseBytes)
	}
	keyBlock, err := aes.NewCipher(keyEncryptionKey)
	if err != nil {
		return nil, err
	}
	keyAEAD, err := cipher.NewGCM(keyBlock)
	if err != nil {
		return nil, err
	}
	if keyAEAD.NonceSize() != backupEnvelopeNonceSize || keyAEAD.Overhead() != backupEnvelopeAuthenticationTagSize {
		return nil, errors.New("unexpected AES-GCM parameters")
	}

	dataKey := make([]byte, backupEnvelopeDataKeySize)
	defer clear(dataKey)
	wrapNonce := make([]byte, backupEnvelopeNonceSize)
	dataNonce := make([]byte, backupEnvelopeNonceSize)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return nil, fmt.Errorf("generate backup data key: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, wrapNonce); err != nil {
		return nil, fmt.Errorf("generate backup key nonce: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, dataNonce); err != nil {
		return nil, fmt.Errorf("generate backup data nonce: %w", err)
	}

	prefix := make([]byte, backupEnvelopePrefixSize)
	copy(prefix, backupEnvelopeMagic)
	prefix[backupEnvelopeVersionOffset] = backupEnvelopeVersion
	prefix[backupEnvelopeKeyWrapOffset] = backupEnvelopeKeyWrapAlgorithm
	prefix[backupEnvelopeDataAlgorithmOffset] = backupEnvelopeDataAlgorithm
	prefix[backupEnvelopeFlagsOffset] = backupEnvelopeFlags
	keyID := backupEnvelopeKeyID(keyEncryptionKey)
	copy(prefix[backupEnvelopeKeyIDOffset:backupEnvelopeWrapNonceOffset], keyID[:])
	copy(prefix[backupEnvelopeWrapNonceOffset:backupEnvelopeDataNonceOffset], wrapNonce)
	copy(prefix[backupEnvelopeDataNonceOffset:backupEnvelopePlaintextSizeOffset], dataNonce)
	binary.BigEndian.PutUint64(prefix[backupEnvelopePlaintextSizeOffset:backupEnvelopePrefixSize], uint64(len(plaintext)))

	wrappedKey := keyAEAD.Seal(nil, wrapNonce, dataKey, backupEnvelopeWrapAAD(prefix))
	if len(wrappedKey) != backupEnvelopeWrappedKeySize {
		return nil, errors.New("unexpected wrapped backup data-key size")
	}
	header := make([]byte, 0, backupEnvelopeHeaderSize)
	header = append(header, prefix...)
	header = append(header, wrappedKey...)

	dataBlock, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}
	dataAEAD, err := cipher.NewGCM(dataBlock)
	if err != nil {
		return nil, err
	}
	ciphertext := dataAEAD.Seal(nil, dataNonce, plaintext, backupEnvelopeDataAAD(header))
	envelope := make([]byte, 0, len(header)+len(ciphertext))
	envelope = append(envelope, header...)
	envelope = append(envelope, ciphertext...)
	return envelope, nil
}

func decryptBackupEnvelope(envelope, keyEncryptionKey []byte) ([]byte, error) {
	if len(keyEncryptionKey) != backupEnvelopeDataKeySize {
		return nil, ErrBackupEnvelopeAuthentication
	}
	minimumSize := backupEnvelopeHeaderSize + backupEnvelopeAuthenticationTagSize
	if len(envelope) < minimumSize {
		return nil, errors.New("backup envelope is truncated")
	}
	if len(envelope) > maxBackupEnvelopeBytes {
		return nil, fmt.Errorf("backup envelope exceeds %d bytes", maxBackupEnvelopeBytes)
	}
	if !bytes.Equal(envelope[:len(backupEnvelopeMagic)], []byte(backupEnvelopeMagic)) {
		return nil, errors.New("unrecognized backup envelope magic")
	}
	if envelope[backupEnvelopeVersionOffset] != backupEnvelopeVersion {
		return nil, fmt.Errorf("unsupported backup envelope version %d", envelope[backupEnvelopeVersionOffset])
	}
	if envelope[backupEnvelopeKeyWrapOffset] != backupEnvelopeKeyWrapAlgorithm ||
		envelope[backupEnvelopeDataAlgorithmOffset] != backupEnvelopeDataAlgorithm ||
		envelope[backupEnvelopeFlagsOffset] != backupEnvelopeFlags {
		return nil, errors.New("unsupported backup envelope algorithms or flags")
	}
	plaintextSize := binary.BigEndian.Uint64(envelope[backupEnvelopePlaintextSizeOffset:backupEnvelopePrefixSize])
	if plaintextSize > uint64(maxS3ResponseBytes) {
		return nil, fmt.Errorf("backup envelope plaintext exceeds %d bytes", maxS3ResponseBytes)
	}
	expectedSize := uint64(backupEnvelopeHeaderSize+backupEnvelopeAuthenticationTagSize) + plaintextSize
	if uint64(len(envelope)) != expectedSize {
		return nil, errors.New("backup envelope length does not match authenticated metadata")
	}

	keyID := backupEnvelopeKeyID(keyEncryptionKey)
	if subtle.ConstantTimeCompare(envelope[backupEnvelopeKeyIDOffset:backupEnvelopeWrapNonceOffset], keyID[:]) != 1 {
		return nil, ErrBackupEnvelopeAuthentication
	}
	keyBlock, err := aes.NewCipher(keyEncryptionKey)
	if err != nil {
		return nil, ErrBackupEnvelopeAuthentication
	}
	keyAEAD, err := cipher.NewGCM(keyBlock)
	if err != nil {
		return nil, ErrBackupEnvelopeAuthentication
	}
	prefix := envelope[:backupEnvelopePrefixSize]
	wrapNonce := envelope[backupEnvelopeWrapNonceOffset:backupEnvelopeDataNonceOffset]
	wrappedKey := envelope[backupEnvelopePrefixSize:backupEnvelopeHeaderSize]
	dataKey, err := keyAEAD.Open(nil, wrapNonce, wrappedKey, backupEnvelopeWrapAAD(prefix))
	if err != nil || len(dataKey) != backupEnvelopeDataKeySize {
		clear(dataKey)
		return nil, ErrBackupEnvelopeAuthentication
	}
	defer clear(dataKey)

	dataBlock, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, ErrBackupEnvelopeAuthentication
	}
	dataAEAD, err := cipher.NewGCM(dataBlock)
	if err != nil {
		return nil, ErrBackupEnvelopeAuthentication
	}
	dataNonce := envelope[backupEnvelopeDataNonceOffset:backupEnvelopePlaintextSizeOffset]
	plaintext, err := dataAEAD.Open(nil, dataNonce, envelope[backupEnvelopeHeaderSize:], backupEnvelopeDataAAD(envelope[:backupEnvelopeHeaderSize]))
	if err != nil || uint64(len(plaintext)) != plaintextSize {
		clear(plaintext)
		return nil, ErrBackupEnvelopeAuthentication
	}
	return plaintext, nil
}

func backupEnvelopeKeyID(key []byte) [backupEnvelopeKeyIDSize]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte("moesekai-backup-key-id-v1\x00"))
	_, _ = digest.Write(key)
	full := digest.Sum(nil)
	var id [backupEnvelopeKeyIDSize]byte
	copy(id[:], full[:backupEnvelopeKeyIDSize])
	clear(full)
	return id
}

func backupEnvelopeWrapAAD(prefix []byte) []byte {
	return backupEnvelopeAAD("moesekai-backup-key-wrap-v1\x00", prefix)
}

func backupEnvelopeDataAAD(header []byte) []byte {
	return backupEnvelopeAAD("moesekai-backup-data-v1\x00", header)
}

func backupEnvelopeAAD(domain string, metadata []byte) []byte {
	aad := make([]byte, 0, len(domain)+len(metadata))
	aad = append(aad, domain...)
	aad = append(aad, metadata...)
	return aad
}
