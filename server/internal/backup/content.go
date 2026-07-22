package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"moesekai/server/internal/db"
	"moesekai/server/internal/files"
	"moesekai/server/internal/store"
)

const translationContentSchemaVersion = 1

var backupSnapshotCreatedHook func() error

type contentManifest struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Files         []contentManifestFile `json:"files"`
}

type contentManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Count  int    `json:"count"`
}

type translationContent struct {
	Entries []store.EntryLocalizationRecord
	Events  store.EventContentExport
	Lyrics  store.LyricsContentExport
}

func (m *Manager) materializeTranslationContent(parent string) (string, error) {
	return materializeTranslationContentFromStore(parent, m.store)
}

func materializeTranslationContentFromStore(parent string, source *store.Store) (string, error) {
	dir := filepath.Join(parent, "translation-content")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	entries, err := source.ExportEntryLocalizations()
	if err != nil {
		return "", err
	}
	events, err := source.ExportEventContent()
	if err != nil {
		return "", err
	}
	lyrics, err := source.ExportLyricsContent()
	if err != nil {
		return "", err
	}
	files := []struct {
		name  string
		value any
		count int
	}{
		{"entries.json", entries, len(entries)},
		{"event-stories.json", events, eventContentCount(events)},
		{"lyrics.json", lyrics, lyricsContentCount(lyrics)},
	}
	manifest := contentManifest{SchemaVersion: translationContentSchemaVersion, Files: []contentManifestFile{}}
	for _, file := range files {
		body, err := json.MarshalIndent(file.value, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, file.name), body, 0o644); err != nil {
			return "", err
		}
		sum := sha256.Sum256(body)
		manifest.Files = append(manifest.Files, contentManifestFile{
			Path: file.name, SHA256: hex.EncodeToString(sum[:]), Count: file.count,
		})
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestJSON, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

func (m *Manager) materializeBackupPayload(parent string) (string, string, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", err
	}
	snapshotPath := filepath.Join(parent, "backup-snapshot.db")
	_ = os.Remove(snapshotPath)
	if err := m.store.Snapshot(snapshotPath); err != nil {
		return "", "", err
	}
	defer func() {
		_ = os.Remove(snapshotPath)
		_ = os.Remove(snapshotPath + "-wal")
		_ = os.Remove(snapshotPath + "-shm")
	}()
	if backupSnapshotCreatedHook != nil {
		if err := backupSnapshotCreatedHook(); err != nil {
			return "", "", err
		}
	}
	snapshotDB, err := db.Open(snapshotPath)
	if err != nil {
		return "", "", err
	}
	defer snapshotDB.Close()
	snapshotStore := store.New(snapshotDB)
	snapshotEvents := store.NewEventStore(snapshotDB)
	snapshotGenerator := files.NewGenerator(snapshotStore, snapshotEvents, parent)
	translations, err := materializeTranslationsWithGenerator(parent, snapshotGenerator)
	if err != nil {
		return "", "", err
	}
	content, err := materializeTranslationContentFromStore(parent, snapshotStore)
	if err != nil {
		return "", "", err
	}
	return translations, content, nil
}

func readTranslationContent(dir string) (translationContent, bool, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if os.IsNotExist(err) {
		return translationContent{}, false, nil
	}
	if err != nil {
		return translationContent{}, false, err
	}
	var manifest contentManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return translationContent{}, true, fmt.Errorf("translation content manifest: %w", err)
	}
	if manifest.SchemaVersion != translationContentSchemaVersion {
		return translationContent{}, true, fmt.Errorf("unsupported translation content schemaVersion %d", manifest.SchemaVersion)
	}
	expected := map[string]bool{"entries.json": true, "event-stories.json": true, "lyrics.json": true}
	seen := map[string]bool{}
	content := translationContent{}
	for _, file := range manifest.Files {
		if !expected[file.Path] || seen[file.Path] || filepath.Base(file.Path) != file.Path {
			return translationContent{}, true, fmt.Errorf("invalid translation content manifest path %q", file.Path)
		}
		seen[file.Path] = true
		body, err := os.ReadFile(filepath.Join(dir, file.Path))
		if err != nil {
			return translationContent{}, true, err
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != file.SHA256 {
			return translationContent{}, true, fmt.Errorf("translation content checksum mismatch: %s", file.Path)
		}
		count := 0
		switch file.Path {
		case "entries.json":
			if err := json.Unmarshal(body, &content.Entries); err != nil {
				return translationContent{}, true, err
			}
			count = len(content.Entries)
		case "event-stories.json":
			if err := json.Unmarshal(body, &content.Events); err != nil {
				return translationContent{}, true, err
			}
			count = eventContentCount(content.Events)
		case "lyrics.json":
			if err := json.Unmarshal(body, &content.Lyrics); err != nil {
				return translationContent{}, true, err
			}
			count = lyricsContentCount(content.Lyrics)
		}
		if count != file.Count {
			return translationContent{}, true, fmt.Errorf("translation content count mismatch: %s", file.Path)
		}
	}
	if len(seen) != len(expected) {
		return translationContent{}, true, fmt.Errorf("translation content manifest is incomplete")
	}
	return content, true, nil
}

func eventContentCount(content store.EventContentExport) int {
	return len(content.Segments) + len(content.Localizations) + len(content.LocaleMeta)
}

func lyricsContentCount(content store.LyricsContentExport) int {
	return len(content.Music) + len(content.Performers) + len(content.Documents) +
		len(content.Lines) + len(content.Segments) + len(content.Publications)
}

func (m *Manager) importTranslationContent(content translationContent, present bool) error {
	if !present {
		return nil
	}
	return m.store.ImportTranslationContent(content.Entries, content.Events, content.Lyrics)
}
