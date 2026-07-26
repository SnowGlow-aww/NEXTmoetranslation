package files

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"moesekai/server/internal/db"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

func openLegacyGenerator(tb testing.TB) (*Generator, *db.DB) {
	tb.Helper()
	database, err := db.Open(filepath.Join(tb.TempDir(), "legacy-public.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { database.Close() })
	s := store.New(database)
	es := store.NewEventStore(database)
	if _, err := s.ImportCategory("cards", model.Category{
		"prefix": {
			"こんにちは":     {Text: "你好", Source: model.SourceCN, Ids: []string{"1", "2"}},
			"A & B < C": {Text: "甲 & 乙 < 丙", Source: model.SourceHuman},
		},
	}); err != nil {
		tb.Fatal(err)
	}
	canonical, digest, err := store.CanonicalizeEventScenario(map[string]any{
		"ScenarioId": "scenario-1", "Snippets": []any{}, "TalkData": []any{},
		"SpecialEffectData": []any{}, "AppearCharacters": []any{},
	}, "scenario-1")
	if err != nil {
		tb.Fatal(err)
	}
	if err := es.ImportOrdered(42, model.EventStoryMeta{
		Source: "official_cn", Version: "1.0", LastUpdated: 1700000000,
	}, []store.OrderedEpisode{{
		EpisodeNo: "1", ScenarioID: "scenario-1", ScenarioCanonicalJSON: canonical, ScenarioSHA256: digest,
		Title:       "标题 & <",
		TitleSource: model.SourceHuman,
		TalkKeys:    []string{"zebra", "apple", "mango & lime"},
		TalkData: map[string]string{
			"zebra":        "斑马",
			"apple":        "苹果",
			"mango & lime": "芒果 & 青柠 <",
		},
		TalkSources: map[string]string{
			"zebra": model.SourceCN, "apple": model.SourceHuman, "mango & lime": model.SourcePinned,
		},
		SpeakerNames: map[string]string{"apple": "角色"},
	}}); err != nil {
		tb.Fatal(err)
	}
	return NewGenerator(s, es, ""), database
}

func TestLegacyPublicGoldenFiles(t *testing.T) {
	gen, _ := openLegacyGenerator(t)
	tests := []struct {
		name    string
		fixture string
		build   func() ([]byte, error)
	}{
		{"flat category", "cards.json", func() ([]byte, error) { return gen.CategoryFlatJSON("cards") }},
		{"full category", "cards.full.json", func() ([]byte, error) { return gen.CategoryFullJSON("cards") }},
		{"event story", "event_42.json", func() ([]byte, error) { return gen.EventStoryJSON(42) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.build()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.HasSuffix(got, []byte("\n")) {
				t.Fatal("legacy generated JSON unexpectedly has a trailing newline")
			}
			want, err := os.ReadFile(filepath.Join("testdata", "legacy", tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			want = bytes.TrimSuffix(want, []byte("\n"))
			if !bytes.Equal(got, want) {
				t.Fatalf("legacy fixture %s mismatch\ngot:\n%s\nwant:\n%s", tt.fixture, got, want)
			}
		})
	}
}

func TestLegacyGenerationLatencyDistribution(t *testing.T) {
	gen, _ := openLegacyGenerator(t)
	const samples = 400
	durations := make([]time.Duration, 0, samples)
	for range samples {
		started := time.Now()
		if _, err := gen.CategoryFlatJSON("cards"); err != nil {
			t.Fatal(err)
		}
		if _, err := gen.CategoryFullJSON("cards"); err != nil {
			t.Fatal(err)
		}
		if _, err := gen.EventStoryJSON(42); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 := durations[(samples*50)/100]
	p95 := durations[(samples*95)/100]
	if p50 <= 0 || p95 < p50 {
		t.Fatalf("invalid latency distribution: p50=%s p95=%s", p50, p95)
	}
	t.Logf("legacy flat+full+event generation (%d samples): p50=%s p95=%s", samples, p50, p95)
}

func BenchmarkLegacyPublicGeneration(b *testing.B) {
	gen, _ := openLegacyGenerator(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := gen.CategoryFlatJSON("cards"); err != nil {
			b.Fatal(err)
		}
		if _, err := gen.CategoryFullJSON("cards"); err != nil {
			b.Fatal(err)
		}
		if _, err := gen.EventStoryJSON(42); err != nil {
			b.Fatal(err)
		}
	}
}
