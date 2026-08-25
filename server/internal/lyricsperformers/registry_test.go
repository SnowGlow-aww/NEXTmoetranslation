package lyricsperformers

import (
	"regexp"
	"testing"
)

func TestAuditedExternalRegistryIsStableAndClosed(t *testing.T) {
	all := All()
	if len(all) != 19 {
		t.Fatalf("audited external performer count=%d, want 19", len(all))
	}
	colorPattern := regexp.MustCompile(`^#[0-9A-F]{6}$`)
	seenNumeric := map[int]bool{}
	seenSource := map[string]bool{}
	for _, performer := range all {
		if performer.NumericID < 1001 || performer.SourceID == "" || performer.Name == "" ||
			performer.Color != "" && !colorPattern.MatchString(performer.Color) {
			t.Fatalf("invalid external performer=%+v", performer)
		}
		if seenNumeric[performer.NumericID] || seenSource[performer.SourceID] {
			t.Fatalf("duplicate external performer identity=%+v", performer)
		}
		seenNumeric[performer.NumericID] = true
		seenSource[performer.SourceID] = true
		bySource, found := BySourceID(performer.SourceID)
		if !found || bySource.NumericID != performer.NumericID {
			t.Fatalf("source lookup for %q=%+v found=%t", performer.SourceID, bySource, found)
		}
		byNumeric, found := ByNumericID(performer.NumericID)
		if !found || byNumeric.SourceID != performer.SourceID {
			t.Fatalf("numeric lookup for %d=%+v found=%t", performer.NumericID, byNumeric, found)
		}
		for _, alias := range append([]string{performer.SourceID, performer.Name}, performer.Aliases...) {
			resolved, found := ByAlias(alias)
			if !found || resolved.SourceID != performer.SourceID {
				t.Fatalf("alias lookup for %q=%+v found=%t", alias, resolved, found)
			}
		}
	}
	if _, found := ByAlias("unreviewed external singer"); found {
		t.Fatal("unreviewed external singer entered the closed registry")
	}

	all[0].Aliases[0] = "mutated"
	if resolved, found := ByAlias("GUMI"); !found || resolved.SourceID != "外部歌唱者-01" {
		t.Fatal("All returned aliases that mutate the registry")
	}
}
