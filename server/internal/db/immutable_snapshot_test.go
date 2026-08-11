package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeImmutableSnapshotFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO entries(category,field,jp_key,cn_text) VALUES('test','title','固定','固定译文')`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Checkpoint(context.Background()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range immutableSQLiteSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fixture retained sidecar %s: %v", suffix, err)
		}
	}
	return path
}

func TestImmutableSnapshotIsByteStableAndQueryOnly(t *testing.T) {
	path := writeImmutableSnapshotFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := OpenImmutableSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Path() != path || snapshot.Size() != int64(len(before)) || len(snapshot.SHA256()) != 64 {
		t.Fatalf("snapshot identity path=%q size=%d sha=%q", snapshot.Path(), snapshot.Size(), snapshot.SHA256())
	}
	var title string
	if err := snapshot.Database.QueryRow(`SELECT cn_text FROM entries WHERE category='test' AND field='title' AND jp_key='固定'`).Scan(&title); err != nil {
		snapshot.Close()
		t.Fatal(err)
	}
	if title != "固定译文" {
		t.Fatalf("title=%q", title)
	}
	if _, err := snapshot.Database.Exec(`INSERT INTO entries(category,field,jp_key,cn_text) VALUES('test','title','拒否','拒绝')`); err == nil {
		snapshot.Close()
		t.Fatal("query-only snapshot accepted a write")
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("immutable snapshot database bytes changed")
	}
	for _, suffix := range immutableSQLiteSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("immutable snapshot created sidecar %s: %v", suffix, err)
		}
	}
}

func TestImmutableSnapshotRejectsAliasesAndSidecars(t *testing.T) {
	path := writeImmutableSnapshotFixture(t)
	alias := filepath.Join(filepath.Dir(path), "alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenImmutableSnapshot(context.Background(), alias); err == nil || !strings.Contains(err.Error(), "direct regular file") {
		t.Fatalf("alias error=%v", err)
	}
	if err := os.WriteFile(path+"-wal", []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenImmutableSnapshot(context.Background(), path); err == nil || !strings.Contains(err.Error(), "-wal") {
		t.Fatalf("sidecar error=%v", err)
	}
}
