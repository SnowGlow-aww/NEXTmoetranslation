package db

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const legacyV2FixtureSHA256 = "2eb61967a5f5b96a4961c0258984d6d5bb2f7b813379872d9d50a427704b8877"

func TestLegacyV2SQLiteFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "legacy-v2.db")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != legacyV2FixtureSHA256 {
		t.Fatalf("legacy fixture checksum = %s, want %s", got, legacyV2FixtureSHA256)
	}

	copyPath := filepath.Join(t.TempDir(), "legacy-v2.db")
	if err := copyFixture(fixture, copyPath); err != nil {
		t.Fatal(err)
	}
	database, err := Open(copyPath)
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer database.Close()

	columns := tableColumnNames(t, database, "entries")
	wantColumns := "category,field,jp_key,cn_text,source,ids_json,updated_at,updated_by"
	if strings.Join(columns, ",") != wantColumns {
		t.Fatalf("legacy entries columns = %v", columns)
	}
	for _, absent := range []string{"schema_migrations", "entry_localizations", "event_story_segments", "lyrics_documents"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, absent).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy fixture unexpectedly contains %s", absent)
		}
	}

	var text, source, ids, updatedBy string
	var updatedAt int64
	if err := database.QueryRow(`SELECT cn_text, source, ids_json, updated_at, updated_by
		FROM entries WHERE category='cards' AND field='prefix' AND jp_key='旧キー'`).
		Scan(&text, &source, &ids, &updatedAt, &updatedBy); err != nil {
		t.Fatal(err)
	}
	if text != "旧翻译" || source != "human" || ids != `["legacy-1"]` || updatedAt != 1690000000 || updatedBy != "legacy-editor" {
		t.Fatalf("legacy entry changed: text=%q source=%q ids=%q at=%d by=%q", text, source, ids, updatedAt, updatedBy)
	}

	var eventSource, title, titleSource, line, lineSource, speaker string
	if err := database.QueryRow(`SELECT s.source, e.title, e.title_source, l.cn_text, l.source, l.speaker_name
		FROM event_stories s
		JOIN event_story_episodes e ON e.event_id=s.event_id
		JOIN event_story_lines l ON l.event_id=e.event_id AND l.episode_no=e.episode_no
		WHERE s.event_id=7 AND l.jp_key='台词'`).
		Scan(&eventSource, &title, &titleSource, &line, &lineSource, &speaker); err != nil {
		t.Fatal(err)
	}
	if eventSource != "official_cn" || title != "旧标题" || titleSource != "human" || line != "旧台词" || lineSource != "pinned" || speaker != "旧角色" {
		t.Fatalf("legacy event data changed: %q %q %q %q %q %q", eventSource, title, titleSource, line, lineSource, speaker)
	}
}

func tableColumnNames(t *testing.T, database *DB, table string) []string {
	t.Helper()
	rows, err := database.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func copyFixture(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
