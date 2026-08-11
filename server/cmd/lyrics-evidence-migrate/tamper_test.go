package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moesekai/server/internal/lyricssource"
)

func TestMigrationRejectsTamperedPartialSymlinkedAliasedAndWrongModeInputs(t *testing.T) {
	t.Run("wrong source mode", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		if err := os.Chmod(fixture.path, 0o640); err != nil {
			t.Fatal(err)
		}
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("wrong source parent mode", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		if err := os.Chmod(filepath.Dir(fixture.path), 0o750); err != nil {
			t.Fatal(err)
		}
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("symlink source", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		linkParent := t.TempDir()
		link := filepath.Join(linkParent, "checkpoint-link.sqlite")
		if err := os.Symlink(fixture.path, link); err != nil {
			t.Fatal(err)
		}
		fixture.path = link
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("symlink source ancestor", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		linkParent := t.TempDir()
		linkedDirectory := filepath.Join(linkParent, "checkpoint-parent")
		if err := os.Symlink(filepath.Dir(fixture.path), linkedDirectory); err != nil {
			t.Fatal(err)
		}
		fixture.path = filepath.Join(linkedDirectory, filepath.Base(fixture.path))
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("indirect symlink source ancestor", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		realParent := filepath.Dir(fixture.path)
		nested := filepath.Join(realParent, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		moved := filepath.Join(nested, filepath.Base(fixture.path))
		if err := os.Rename(fixture.path, moved); err != nil {
			t.Fatal(err)
		}
		linkParent := t.TempDir()
		linkedDirectory := filepath.Join(linkParent, "checkpoint-parent")
		if err := os.Symlink(realParent, linkedDirectory); err != nil {
			t.Fatal(err)
		}
		fixture.path = filepath.Join(linkedDirectory, "nested", filepath.Base(moved))
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("hard-link alias", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		if err := os.Link(fixture.path, filepath.Join(filepath.Dir(fixture.path), "checkpoint-alias.sqlite")); err != nil {
			t.Fatal(err)
		}
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("unexpected schema object", func(t *testing.T) {
		fixture := mutateCheckpointFixture(t, createCheckpointFixture(t, 1), `CREATE TABLE injected(value TEXT) STRICT`)
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("schema SQL whitespace drift", func(t *testing.T) {
		fixture := mutateCheckpointFixture(t, createCheckpointFixture(t, 1),
			`PRAGMA writable_schema=ON`,
			`UPDATE sqlite_schema SET sql=replace(sql,'CREATE TABLE checkpoint_metadata (','CREATE TABLE checkpoint_metadata  (') WHERE name='checkpoint_metadata'`,
			`PRAGMA writable_schema=OFF`)
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("noncanonical evidence JSON", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		database := openWritableFixture(t, fixture.path)
		var body []byte
		if err := database.QueryRow(`SELECT evidence_json FROM evidence LIMIT 1`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		body = append(body, ' ')
		if _, err := database.Exec(`UPDATE evidence SET evidence_json=?,evidence_sha256=?`, body, sha256Hex(body)); err != nil {
			t.Fatal(err)
		}
		closeWritableFixture(t, database)
		fixture = refreshFixtureSHA(t, fixture)
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("key to envelope mismatch", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		database := openWritableFixture(t, fixture.path)
		var body []byte
		if err := database.QueryRow(`SELECT evidence_json FROM evidence LIMIT 1`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var envelope lyricssource.IndexEvidence
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope.EvidenceID = strings.Repeat("a", len(envelope.EvidenceID))
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE evidence SET evidence_json=?,evidence_sha256=?`, body, sha256Hex(body)); err != nil {
			t.Fatal(err)
		}
		closeWritableFixture(t, database)
		fixture = refreshFixtureSHA(t, fixture)
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("raw SHA mismatch", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		database := openWritableFixture(t, fixture.path)
		var body []byte
		if err := database.QueryRow(`SELECT evidence_json FROM evidence LIMIT 1`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var envelope lyricssource.IndexEvidence
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope.RawSHA256 = strings.Repeat("f", 64)
		envelope.SHA256 = envelope.RawSHA256
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE evidence SET evidence_json=?,evidence_sha256=?`, body, sha256Hex(body)); err != nil {
			t.Fatal(err)
		}
		closeWritableFixture(t, database)
		fixture = refreshFixtureSHA(t, fixture)
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("raw length mismatch", func(t *testing.T) {
		fixture := mutateCheckpointFixture(t, createCheckpointFixture(t, 1),
			`UPDATE evidence SET raw_byte_count=raw_byte_count+1`)
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("partial evidence set", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 2)
		database := openWritableFixture(t, fixture.path)
		var key string
		if err := database.QueryRow(`SELECT evidence_id FROM evidence ORDER BY evidence_id LIMIT 1`).Scan(&key); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`DELETE FROM result_evidence WHERE evidence_id=?`, key); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`DELETE FROM evidence WHERE evidence_id=?`, key); err != nil {
			t.Fatal(err)
		}
		closeWritableFixture(t, database)
		fixture = refreshFixtureSHA(t, fixture)
		assertMigrationRejectedBeforeDestination(t, fixture, filepath.Join(t.TempDir(), "destination"), fixture.evidenceCount)
	})

	t.Run("destination aliases source", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		assertMigrationRejectedBeforeDestination(t, fixture, fixture.path, fixture.evidenceCount)
	})

	t.Run("symlink destination root", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		parent := t.TempDir()
		real := filepath.Join(parent, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "destination")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := executeMigration(t.Context(), commandOptions{
			checkpointPath: fixture.path, destinationRoot: link,
			expectedCheckpointSHA: fixture.sha256, expectedEvidenceCount: fixture.evidenceCount,
		}); err == nil {
			t.Fatal("migration accepted a symlink destination root")
		}
	})

	t.Run("symlink destination ancestor", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		parent := t.TempDir()
		realParent := filepath.Join(parent, "real-parent")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		linkedParent := filepath.Join(parent, "linked-parent")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(linkedParent, "destination")
		assertMigrationRejectedBeforeDestination(t, fixture, destination, fixture.evidenceCount)
		if _, err := os.Lstat(filepath.Join(realParent, "destination")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected symlink-ancestor destination changed its resolved target: %v", err)
		}
	})

	t.Run("indirect symlink destination ancestor", func(t *testing.T) {
		fixture := createCheckpointFixture(t, 1)
		parent := t.TempDir()
		realParent := filepath.Join(parent, "real-parent")
		nested := filepath.Join(realParent, "nested")
		if err := os.MkdirAll(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		linkedParent := filepath.Join(parent, "linked-parent")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(linkedParent, "nested", "destination")
		assertMigrationRejectedBeforeDestination(t, fixture, destination, fixture.evidenceCount)
		if _, err := os.Lstat(filepath.Join(nested, "destination")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected indirect-symlink destination changed its resolved target: %v", err)
		}
	})
}

func TestPinnedCheckpointDigestRejectsInPlaceByteChange(t *testing.T) {
	fixture := createCheckpointFixture(t, 1)
	checkpoint, err := openSourceCheckpoint(t.Context(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer checkpoint.Close()

	file, err := os.OpenFile(fixture.path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var changed [1]byte
	if _, err := file.ReadAt(changed[:], 72); err != nil {
		file.Close()
		t.Fatal(err)
	}
	changed[0] ^= 0xff
	if _, err := file.WriteAt(changed[:], 72); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.verifyDigest("after synthetic in-place mutation"); err == nil {
		t.Fatal("pinned checkpoint accepted changed bytes on the same inode")
	}
}

func assertMigrationRejectedBeforeDestination(t *testing.T, fixture checkpointFixture, destination string, expectedCount int) {
	t.Helper()
	_, err := executeMigration(t.Context(), commandOptions{
		checkpointPath: fixture.path, destinationRoot: destination,
		expectedCheckpointSHA: fixture.sha256, expectedEvidenceCount: expectedCount,
	})
	if err == nil {
		t.Fatal("migration accepted a rejected checkpoint or alias")
	}
	if destination != fixture.path {
		if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("rejected migration created destination: %v", statErr)
		}
	}
}

func openWritableFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}

func closeWritableFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
