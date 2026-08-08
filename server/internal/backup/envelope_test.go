package backup

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"moesekai/server/internal/config"
)

func TestBackupEnvelopeRoundTripRejectsWrongKeyAndTampering(t *testing.T) {
	key := bytes.Repeat([]byte{0x41}, backupEnvelopeDataKeySize)
	wrongKey := bytes.Repeat([]byte{0x42}, backupEnvelopeDataKeySize)
	plaintext := []byte("private-translation-content-and-provenance-sentinel-2026-07-31")

	first, err := encryptBackupEnvelope(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encryptBackupEnvelope(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("random data keys and nonces produced identical envelopes")
	}
	if bytes.HasPrefix(first, []byte{0x1f, 0x8b}) || bytes.HasPrefix(first, []byte("{")) || bytes.Contains(first, plaintext) {
		t.Fatal("encrypted envelope exposed plaintext or a directly parseable archive format")
	}
	decrypted, err := decryptBackupEnvelope(first, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted bytes = %q, want %q", decrypted, plaintext)
	}
	clear(decrypted)

	if _, err := decryptBackupEnvelope(first, wrongKey); !errors.Is(err, ErrBackupEnvelopeAuthentication) {
		t.Fatalf("wrong-key error = %v", err)
	}
	for _, test := range []struct {
		name   string
		offset int
	}{
		{name: "authenticated key-wrap metadata", offset: backupEnvelopeWrapNonceOffset},
		{name: "wrapped data key", offset: backupEnvelopePrefixSize},
		{name: "payload ciphertext", offset: backupEnvelopeHeaderSize},
	} {
		t.Run(test.name, func(t *testing.T) {
			tampered := append([]byte(nil), first...)
			tampered[test.offset] ^= 0x80
			if _, err := decryptBackupEnvelope(tampered, key); !errors.Is(err, ErrBackupEnvelopeAuthentication) {
				t.Fatalf("tamper error = %v", err)
			}
		})
	}
}

func TestBackupAllRejectsMissingOrInvalidEncryptionKeyBeforeRemoteIO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git sentinel uses a POSIX shell script")
	}
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "missing"},
		{name: "invalid", value: "not-canonical-base64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := setupLegacyBackup(t)
			t.Setenv(backupEncryptionKeyEnv, test.value)

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			bin := t.TempDir()
			marker := filepath.Join(t.TempDir(), "git-invoked")
			fakeGit := filepath.Join(bin, "git")
			if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\n: > \"$GIT_REMOTE_MARKER\"\nexit 97\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GIT_REMOTE_MARKER", marker)

			for key, value := range map[string]string{
				config.KeyBackupS3Enabled:   "true",
				config.KeyBackupS3Endpoint:  server.URL,
				config.KeyBackupS3Region:    "test-region",
				config.KeyBackupS3Bucket:    "test-bucket",
				config.KeyBackupS3AccessKey: "test-access",
				config.KeyBackupS3SecretKey: "test-secret",
				config.KeyBackupGitEnabled:  "true",
				config.KeyBackupGitRepoURL:  "https://example.invalid/private-backup.git",
				config.KeyBackupGitBranch:   "encrypted-backup",
			} {
				if err := h.cfg.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}

			results, err := h.manager.BackupAll()
			if err == nil || (!strings.Contains(err.Error(), "backup encryption key") && !strings.Contains(err.Error(), backupEncryptionKeyEnv)) {
				t.Fatalf("BackupAll error = %v", err)
			}
			if !strings.HasPrefix(results["s3"], "error:") || !strings.HasPrefix(results["git"], "error:") {
				t.Fatalf("BackupAll results = %#v", results)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("missing/invalid key allowed %d S3 requests", got)
			}
			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("missing/invalid key invoked remote git: %v", statErr)
			}
		})
	}
}
