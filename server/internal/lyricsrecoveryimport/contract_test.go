package lyricsrecoveryimport

import (
	"bytes"
	"strings"
	"testing"

	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/model"
)

func textFreeManifestFixture(t *testing.T) Manifest {
	t.Helper()
	states := []lyricsrootmanifest.CoverageState{
		lyricsrootmanifest.CoverageSatisfiedNoLyrics,
		lyricsrootmanifest.CoverageAmbiguous,
		lyricsrootmanifest.CoverageMissing,
		lyricsrootmanifest.CoverageIncomplete,
	}
	items := make([]Item, len(states))
	for index, state := range states {
		availabilityState := model.LyricsAvailabilityState(state)
		reason := model.LyricsSourceVersionReasonVersionConflict
		noLyricsReason := ""
		if state == lyricsrootmanifest.CoverageSatisfiedNoLyrics {
			reason = ""
			noLyricsReason = model.LyricsAvailabilityNoLyricsCatalogInstrumental
		}
		document := model.LyricsAvailabilityDocument{
			SchemaVersion: model.LyricsAvailabilityDocumentSchemaVersion,
			State:         availabilityState, ReasonCode: reason, NoLyricsReason: noLyricsReason,
			FixedIdentities: []model.LyricsSourceFixedIdentity{},
		}
		documentSHA, err := AvailabilityDocumentSHA256(document)
		if err != nil {
			t.Fatal(err)
		}
		items[index] = Item{
			MusicID: index + 1, JapaneseTitle: "試験曲", CatalogFingerprint: strings.Repeat(string('a'+rune(index)), 64),
			TargetMusicID: index + 1, AssociationMusicIDs: []int{}, State: state,
			ResultSHA256: strings.Repeat(string('1'+rune(index)), 64),
			Availability: &document, AvailabilityDocumentSHA256: documentSHA,
		}
	}
	musicIDsSHA, err := lyricsrootmanifest.OrderedMusicIDsSHA256([]int{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	coverage := lyricsrootmanifest.Coverage{
		Total: 4, SatisfiedNoLyrics: 1, Ambiguous: 1, Missing: 1, Incomplete: 1,
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Root: RootBinding{
			SchemaVersion: lyricsrootmanifest.SchemaVersionV2, RootID: "root-text-free-fixture",
			RootSHA256: strings.Repeat("f", 64), CatalogCount: 4, MusicIDsSHA256: musicIDsSHA, Coverage: coverage,
		},
		Items: items,
	}
	digest, err := manifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.BatchSHA256 = digest
	return manifest
}

func TestTextFreeRecoveryImportManifestRoundTrip(t *testing.T) {
	manifest := textFreeManifestFixture(t)
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	body, err := MarshalCanonical(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCanonical(body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalCanonical(decoded)
	if err != nil || !bytes.Equal(body, second) {
		t.Fatalf("canonical round trip err=%v", err)
	}
	lower := bytes.ToLower(body)
	for _, forbidden := range [][]byte{[]byte("romaji"), []byte("romanization"), []byte(`"full"`), []byte(`"game"`)} {
		if bytes.Contains(lower, forbidden) {
			t.Fatalf("text-free manifest leaked %q", forbidden)
		}
	}
}

func TestRecoveryImportManifestRejectsStateAndDigestDrift(t *testing.T) {
	manifest := textFreeManifestFixture(t)
	manifest.Root.Coverage.Missing++
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("coverage drift was accepted")
	}

	manifest = textFreeManifestFixture(t)
	manifest.Items[2].Availability.State = model.LyricsAvailabilityStateFailed
	manifest.BatchSHA256 = ""
	digest, _ := manifestDigest(manifest)
	manifest.BatchSHA256 = digest
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("availability state drift was accepted")
	}

	manifest = textFreeManifestFixture(t)
	manifest.Items[0].AvailabilityDocumentSHA256 = strings.Repeat("0", 64)
	manifest.BatchSHA256 = ""
	digest, _ = manifestDigest(manifest)
	manifest.BatchSHA256 = digest
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("availability digest drift was accepted")
	}
}
