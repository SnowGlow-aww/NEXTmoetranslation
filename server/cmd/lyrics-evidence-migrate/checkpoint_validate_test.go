package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseMainFileMustResolveToPinnedDescriptorInode(t *testing.T) {
	root := t.TempDir()
	pinnedPath := filepath.Join(root, "checkpoint.sqlite")
	if err := os.WriteFile(pinnedPath, []byte("pinned"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinnedFile, err := os.Open(pinnedPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pinnedFile.Close() })
	pinnedInfo, err := pinnedFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &sourceCheckpoint{
		file:            pinnedFile,
		fileInfo:        pinnedInfo,
		operationalPath: fmt.Sprintf("/dev/fd/%d", pinnedFile.Fd()),
	}
	if !checkpoint.databaseMainFileMatchesPinnedDescriptor(pinnedPath) {
		t.Fatal("same-inode database path was not accepted as the pinned descriptor target")
	}

	otherPath := filepath.Join(root, "other.sqlite")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"", "relative.sqlite", otherPath} {
		if checkpoint.databaseMainFileMatchesPinnedDescriptor(candidate) {
			t.Fatalf("non-pinned database path was accepted: %q", candidate)
		}
	}
}
