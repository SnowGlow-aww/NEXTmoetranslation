package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishExactRecoveryImportReceiptCreatesAndReplaysExactBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery-import-receipt.json")
	body := []byte("{\n  \"schemaVersion\": 1\n}\n")
	if err := publishExactRecoveryImportReceipt(path, body); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode=%v", info.Mode())
	}
	if err := publishExactRecoveryImportReceipt(path, append([]byte(nil), body...)); err != nil {
		t.Fatalf("exact receipt replay: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, body) {
		t.Fatalf("stored receipt changed err=%v", err)
	}
}

func TestPublishExactRecoveryImportReceiptRejectsConflictAndSymlink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "recovery-import-receipt.json")
	if err := publishExactRecoveryImportReceipt(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := publishExactRecoveryImportReceipt(path, []byte("second")); err == nil {
		t.Fatal("conflicting receipt bytes were accepted")
	}
	link := filepath.Join(directory, "receipt-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := publishExactRecoveryImportReceipt(link, []byte("first")); err == nil {
		t.Fatal("receipt symlink was accepted")
	}
}

func TestDigestBytesIsStable(t *testing.T) {
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := digestBytes([]byte("abc")); got != want {
		t.Fatalf("digest=%s want=%s", got, want)
	}
}
