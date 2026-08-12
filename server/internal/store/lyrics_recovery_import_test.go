package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"moesekai/server/internal/lyricsrecoveryimport"
	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/model"
)

func TestLyricsImportRuntimeSchemasAllowReviewedV27ThroughV28Contiguously(t *testing.T) {
	validators := map[string]func(context.Context, *sql.Tx) error{
		"recovery": validateRecoveryImportRuntimeSchema,
		"staged":   validateStagedImportRuntimeSchema,
	}
	cases := []struct {
		name      string
		mutate    func(*testing.T, *sql.Tx)
		wantError bool
	}{
		{name: "current v28"},
		{name: "v27 input runtime", mutate: func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version=28`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "gap before v28", wantError: true, mutate: func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version=27`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreviewed v29", wantError: true, mutate: func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at)
				VALUES (29,'future_migration',?,1)`, strings.Repeat("f", 64)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for validatorName, validate := range validators {
		for _, test := range cases {
			t.Run(validatorName+"/"+test.name, func(t *testing.T) {
				s := setupLyricsStore(t)
				tx, err := s.db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if test.mutate != nil {
					test.mutate(t, tx)
				}
				err = validate(context.Background(), tx)
				if test.wantError {
					if err == nil || !strings.Contains(err.Error(), "contiguous schema-v27 through schema-v28 runtime") {
						t.Fatalf("runtime schema gate error=%v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("runtime schema gate: %v", err)
				}
			})
		}
	}
}

func TestRecoveryEditableReplayRevisionUsesStoredRevision(t *testing.T) {
	requested := model.SongLyrics{MusicID: 42, Revision: 0, Lines: []model.LyricLine{{ID: "line-1", Japanese: "同じ"}}}
	current := requested
	current.Lines = append([]model.LyricLine(nil), requested.Lines...)
	current.Revision = 7
	if got, err := recoveryEditableReplayRevision(42, requested, current); err != nil || got != 7 {
		t.Fatalf("replay revision=%d err=%v want=7", got, err)
	}
	current.Lines[0].Japanese = "変更"
	if _, err := recoveryEditableReplayRevision(42, requested, current); err == nil {
		t.Fatal("editable lyrics drift was accepted during replay")
	}
}

func TestRecoveryCategoriesJSONAlwaysPersistsAnArray(t *testing.T) {
	for name, test := range map[string]struct {
		categories []string
		want       string
	}{
		"nil":   {categories: nil, want: "[]"},
		"empty": {categories: []string{}, want: "[]"},
		"values": {
			categories: []string{"Songs", "Project SEKAI"},
			want:       `["Songs","Project SEKAI"]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := recoveryCategoriesJSON(test.categories)
			if err != nil || got != test.want {
				t.Fatalf("categories JSON=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
}

func TestRecoveryImportCatalogTargetMatchesStateClosedSet(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	fullTarget := model.CatalogLyricsTarget{
		MusicID: 42, CatalogFingerprint: fingerprint,
		Disposition: model.LyricsCatalogTargetFullTarget, TargetMusicID: 42,
		AssociationMusicIDs: []int{},
	}
	for _, state := range []lyricsrootmanifest.CoverageState{
		lyricsrootmanifest.CoverageComplete,
		lyricsrootmanifest.CoverageGameOnly,
		lyricsrootmanifest.CoverageAmbiguous,
		lyricsrootmanifest.CoverageMissing,
		lyricsrootmanifest.CoverageIncomplete,
		lyricsrootmanifest.CoverageFailed,
	} {
		item := lyricsrecoveryimport.Item{
			MusicID: 42, CatalogFingerprint: fingerprint, TargetMusicID: 42,
			AssociationMusicIDs: []int{}, State: state,
		}
		if !recoveryImportCatalogTargetMatches(item, fullTarget) {
			t.Fatalf("state %q rejected its exact Full catalog target", state)
		}
	}

	instrumentalReview := model.CatalogLyricsTarget{
		MusicID: 42, CatalogFingerprint: fingerprint,
		Disposition: model.LyricsCatalogTargetReview, ReasonCode: "instrumental_no_vocals",
		AssociationMusicIDs: []int{},
	}
	noLyrics := lyricsrecoveryimport.Item{
		MusicID: 42, CatalogFingerprint: fingerprint, TargetMusicID: 42,
		AssociationMusicIDs: []int{}, State: lyricsrootmanifest.CoverageSatisfiedNoLyrics,
	}
	if !recoveryImportCatalogTargetMatches(noLyrics, instrumentalReview) {
		t.Fatal("reviewed catalog instrumental was rejected for satisfied no-lyrics")
	}
}

func TestRecoveryImportCatalogTargetMatchesRejectsCrossStateOrIdentityDrift(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	complete := lyricsrecoveryimport.Item{
		MusicID: 42, CatalogFingerprint: fingerprint, TargetMusicID: 42,
		AssociationMusicIDs: []int{}, State: lyricsrootmanifest.CoverageComplete,
	}
	noLyrics := complete
	noLyrics.State = lyricsrootmanifest.CoverageSatisfiedNoLyrics
	fullTarget := model.CatalogLyricsTarget{
		MusicID: 42, CatalogFingerprint: fingerprint,
		Disposition: model.LyricsCatalogTargetFullTarget, TargetMusicID: 42,
		AssociationMusicIDs: []int{},
	}
	instrumentalReview := model.CatalogLyricsTarget{
		MusicID: 42, CatalogFingerprint: fingerprint,
		Disposition: model.LyricsCatalogTargetReview, ReasonCode: "instrumental_no_vocals",
		AssociationMusicIDs: []int{},
	}

	tests := map[string]struct {
		item   lyricsrecoveryimport.Item
		target model.CatalogLyricsTarget
	}{
		"complete cannot consume instrumental review": {item: complete, target: instrumentalReview},
		"no-lyrics cannot consume Full target":        {item: noLyrics, target: fullTarget},
		"no-lyrics reason is closed": {item: noLyrics, target: func() model.CatalogLyricsTarget {
			target := instrumentalReview
			target.ReasonCode = "medley_composite_source"
			return target
		}()},
		"catalog fingerprint drift": {item: complete, target: func() model.CatalogLyricsTarget {
			target := fullTarget
			target.CatalogFingerprint = strings.Repeat("b", 64)
			return target
		}()},
		"Full target drift": {item: complete, target: func() model.CatalogLyricsTarget {
			target := fullTarget
			target.TargetMusicID = 43
			return target
		}()},
		"Full association drift": {item: complete, target: func() model.CatalogLyricsTarget {
			target := fullTarget
			target.AssociationMusicIDs = []int{43}
			return target
		}()},
		"review target must remain unelected": {item: noLyrics, target: func() model.CatalogLyricsTarget {
			target := instrumentalReview
			target.TargetMusicID = 42
			return target
		}()},
		"unknown state": {item: func() lyricsrecoveryimport.Item {
			item := complete
			item.State = lyricsrootmanifest.CoverageState("future")
			return item
		}(), target: fullTarget},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if recoveryImportCatalogTargetMatches(test.item, test.target) {
				t.Fatal("unsafe recovery catalog target was accepted")
			}
		})
	}
}
