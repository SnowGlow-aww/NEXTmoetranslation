package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moesekai/server/internal/db"
)

func TestRunRejectsImplicitOrRelativeCandidatePaths(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"--database", "relative.db", "--batch-sha256", strings.Repeat("a", 64), "--output-directory", "/tmp/output"},
		{"--database", "/tmp/source.db", "--batch-sha256", strings.Repeat("a", 64), "--output-directory", "relative"},
		{"--database", "/tmp/source.db", "--batch-sha256", strings.Repeat("a", 64), "--output-directory", "/tmp/output", "--v2-compat-output-directory", "relative"},
		{"--database", "/tmp/source.db", "--batch-sha256", strings.Repeat("a", 64), "--output-directory", "/tmp/output", "--v2-compat-output-directory", "/tmp/output"},
		{"--database", "/tmp/source.db", "--batch-sha256", strings.Repeat("a", 64), "--output-directory", "/tmp/output", "--v2-compat-output-directory", "/tmp/output/v2"},
	} {
		if err := run(context.Background(), arguments, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments unexpectedly accepted: %v", arguments)
		}
	}
}

func TestRunRejectsHistoricalMixedLyricsStorageOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed-recovery.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO catalog_music(music_id,title_ja) VALUES (700,'混在テスト')`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO song_lyrics(music_id) VALUES (700)`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TRIGGER song_lyrics_source_v3_reject_legacy_insert`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO song_lyrics_source_documents
		(music_id,schema_version,reason_code,document_json,document_sha256,manifest_batch_sha256,created_at)
		VALUES (700,3,'','{}',?, ?,1)`, strings.Repeat("a", 64), strings.Repeat("b", 64)); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Checkpoint(t.Context()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(filepath.Dir(path), "candidate")
	err = run(context.Background(), []string{
		"--database", path,
		"--batch-sha256", strings.Repeat("c", 64),
		"--output-directory", output,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "mixed lyrics storage ownership for music 700") {
		t.Fatalf("historical mixed lyrics storage was accepted: %v", err)
	}
	if _, statErr := os.Lstat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed mixed-storage candidate created output: %v", statErr)
	}
}

func TestRunRejectsUnreviewedV30RuntimeWithoutCreatingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future-recovery.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at)
		VALUES (30,'future_migration',?,1)`, strings.Repeat("f", 64)); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Checkpoint(t.Context()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(filepath.Dir(path), "candidate")
	err = run(context.Background(), []string{
		"--database", path,
		"--batch-sha256", strings.Repeat("c", 64),
		"--output-directory", output,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "contiguous schema v27 through v29 history") {
		t.Fatalf("future runtime schema error=%v", err)
	}
	if _, statErr := os.Lstat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed future-schema candidate created output: %v", statErr)
	}
}

func TestRunReadsV29DatabaseWithoutChangingBytesOrCreatingSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Checkpoint(context.Background()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(filepath.Dir(path), "candidate")
	err = run(context.Background(), []string{
		"--database", path,
		"--batch-sha256", strings.Repeat("a", 64),
		"--output-directory", output,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing exact batch error=%v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only candidate command changed database bytes")
	}
	if _, statErr := os.Lstat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed candidate created output: %v", statErr)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, statErr := os.Lstat(path + suffix); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("read-only candidate command left sidecar %s: %v", suffix, statErr)
		}
	}
}
