package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"moesekai/server/internal/model"
)

func TestPublicLyricsV2PreservesWholeLinePerformerAttributionSeparately(t *testing.T) {
	s := setupLyricsStore(t)
	input, document := publicLyricsV2SekaiFixture(10)
	for lineIndex := range document.Full.Lines {
		line := &document.Full.Lines[lineIndex]
		for segmentIndex := range line.Segments {
			line.Segments[segmentIndex].PerformerIDs = []string{}
			input.Lines[lineIndex].Segments[segmentIndex].PerformerIDs = []int{1}
		}
		line.TrailingPerformerIDs = []string{"歌唱者-21"}
	}

	saved := savePublicLyricsV2Fixture(t, s, input, document)
	if _, err := s.PublishLyrics(saved.MusicID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	_, details, err := s.PublishedLyrics()
	if err != nil {
		t.Fatal(err)
	}
	for lineIndex, line := range details[saved.MusicID].Lines {
		if !reflect.DeepEqual(line.TrailingPerformerIDs, []int{1}) {
			t.Fatalf("line %d trailing performers=%v, want [1]", lineIndex+1, line.TrailingPerformerIDs)
		}
		for segmentIndex, segment := range line.Segments {
			if segment.PerformerIDs == nil || len(segment.PerformerIDs) != 0 {
				t.Fatalf("line %d segment %d flattened whole-line attribution=%v", lineIndex+1, segmentIndex+1, segment.PerformerIDs)
			}
		}
	}
}

func TestBuildRecoveryPublicLyricsCandidateProjectsAvailabilityUnion(t *testing.T) {
	content, batchSHA := recoveryPublicUnionFixture(t)
	candidate, err := buildRecoveryPublicLyricsCandidate(content, batchSHA)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.BatchSHA256 != batchSHA || candidate.RootSHA256 != content.RecoveryBatches[0].RootSHA256 ||
		candidate.Index.Version != 2 || len(candidate.Index.Songs) != 7 || len(candidate.Details) != 2 {
		t.Fatalf("recovery Public candidate=%+v", candidate)
	}

	wantStates := []PublicLyricsAvailabilityState{
		PublicLyricsStateComplete,
		PublicLyricsStateGameOnly,
		PublicLyricsStateSatisfiedNoLyrics,
		PublicLyricsStateAmbiguous,
		PublicLyricsStateMissing,
		PublicLyricsStateIncomplete,
		PublicLyricsStateFailed,
	}
	for index, item := range candidate.Index.Songs {
		wantMusicID := (index + 1) * 10
		if item.MusicID != wantMusicID || item.State != wantStates[index] || item.Revision <= 0 || item.UpdatedAt == "" {
			t.Fatalf("index item %d=%+v", index, item)
		}
	}
	complete := candidate.Details[10]
	if complete.State != PublicLyricsStateComplete || !reflect.DeepEqual(complete.AvailableVersions, []string{"full"}) ||
		len(complete.Lines) == 0 {
		t.Fatalf("complete detail=%+v", complete)
	}
	game := candidate.Details[20]
	if game.State != PublicLyricsStateGameOnly || !reflect.DeepEqual(game.AvailableVersions, []string{"game"}) ||
		game.GameProjection != nil || len(game.Lines) == 0 || len(game.Attributions) != 1 {
		t.Fatalf("Game-only detail=%+v", game)
	}
	if !reflect.DeepEqual(game.Lines[0].TrailingPerformerIDs, []int{1}) {
		t.Fatalf("Game-only trailing performers=%v", game.Lines[0].TrailingPerformerIDs)
	}
	for segmentIndex, segment := range game.Lines[0].Segments {
		if segment.PerformerIDs == nil || len(segment.PerformerIDs) != 0 {
			t.Fatalf("Game-only segment %d flattened whole-line attribution=%v", segmentIndex+1, segment.PerformerIDs)
		}
	}
	if candidate.Index.Songs[2].NoLyricsReason != model.LyricsAvailabilityNoLyricsCatalogInstrumental {
		t.Fatalf("satisfied no-lyrics index=%+v", candidate.Index.Songs[2])
	}
	for _, musicID := range []int{30, 40, 50, 60, 70} {
		if _, exists := candidate.Details[musicID]; exists {
			t.Fatalf("text-free state unexpectedly emitted detail for music %d", musicID)
		}
	}

	assertRecoveryPublicJSONHasNoPrivateOrRomajiFields(t, candidate.Index)
	for _, detail := range candidate.Details {
		assertRecoveryPublicJSONHasNoPrivateOrRomajiFields(t, detail)
	}
}

func TestBuildRecoveryPublicLyricsCandidateRequiresExactBatchAndFailsClosed(t *testing.T) {
	content, batchSHA := recoveryPublicUnionFixture(t)

	if _, err := buildRecoveryPublicLyricsCandidate(content, strings.Repeat("f", 64)); err == nil ||
		!strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing exact batch error=%v", err)
	}

	duplicated := cloneLyricsContentExport(t, content)
	duplicated.RecoveryBatches = append(duplicated.RecoveryBatches, duplicated.RecoveryBatches[0])
	if _, err := buildRecoveryPublicLyricsCandidate(duplicated, batchSHA); err == nil ||
		!strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate exact batch error=%v", err)
	}

	staleAvailability := cloneLyricsContentExport(t, content)
	for index := range staleAvailability.AvailabilityDocuments {
		if staleAvailability.AvailabilityDocuments[index].MusicID == 50 {
			staleAvailability.AvailabilityDocuments[index].State = string(model.LyricsAvailabilityStateFailed)
			break
		}
	}
	if _, err := buildRecoveryPublicLyricsCandidate(staleAvailability, batchSHA); err == nil ||
		!strings.Contains(err.Error(), "availability state changed") {
		t.Fatalf("stale availability error=%v", err)
	}

	missingGameContribution := cloneLyricsContentExport(t, content)
	filtered := missingGameContribution.RecoveryContributions[:0]
	for _, contribution := range missingGameContribution.RecoveryContributions {
		if contribution.MusicID != 20 || contribution.Component != "game_text" {
			filtered = append(filtered, contribution)
		}
	}
	missingGameContribution.RecoveryContributions = filtered
	if _, err := buildRecoveryPublicLyricsCandidate(missingGameContribution, batchSHA); err == nil ||
		!strings.Contains(err.Error(), "contributions") {
		t.Fatalf("missing Game contribution error=%v", err)
	}
}

func TestRecoveryPublicLyricsReadsValidatedExactBatchWithoutPublishing(t *testing.T) {
	content := recoveryContentBackupFixture(t)
	s := setupLyricsStore(t)
	if err := s.ImportTranslationContent(nil, EventContentExport{}, content); err != nil {
		t.Fatal(err)
	}
	batchSHA := content.RecoveryBatches[0].BatchSHA256
	var before int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM song_lyrics_publications`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.RecoveryPublicLyrics(batchSHA)
	if err != nil {
		t.Fatal(err)
	}
	var after int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM song_lyrics_publications`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != 0 || after != before || candidate.BatchSHA256 != batchSHA ||
		len(candidate.Index.Songs) != len(content.Music) || len(candidate.Details) != 1 {
		t.Fatalf("read-only recovery candidate before=%d after=%d candidate=%+v", before, after, candidate)
	}
	if _, err := s.RecoveryPublicLyrics(strings.Repeat("f", 64)); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("exact missing batch error=%v", err)
	}
	if _, err := s.RecoveryPublicLyrics(strings.ToUpper(batchSHA)); err == nil ||
		!strings.Contains(err.Error(), "exact lowercase") {
		t.Fatalf("uppercase batch error=%v", err)
	}
}

func recoveryPublicUnionFixture(t *testing.T) (LyricsContentExport, string) {
	t.Helper()
	content := recoveryContentBackupFixture(t)
	batchSHA := content.RecoveryBatches[0].BatchSHA256
	createdAt := content.RecoveryBatches[0].CreatedAt

	completeItem := content.RecoveryItems[0]
	if completeItem.State != "complete" {
		completeItem = content.RecoveryItems[1]
	}
	content.Music = content.Music[:2]
	for musicID := 30; musicID <= 70; musicID += 10 {
		content.Music = append(content.Music, CatalogMusicBackupRecord{
			MusicID: musicID, TitleJA: "状態試験曲" + string(rune('A'+musicID/10)),
		})
	}
	sort.Slice(content.Music, func(left, right int) bool { return content.Music[left].MusicID < content.Music[right].MusicID })
	content.RecoveryBatches[0].CatalogCount = len(content.Music)

	content.RecoveryItems = []LyricsRecoveryItemBackupRecord{completeItem}
	content.AvailabilityDocuments = []LyricsAvailabilityDocumentBackupRecord{}
	content.RecoveryContributions = append([]LyricsRecoveryContributionBackupRecord(nil), content.RecoveryContributions...)

	_, source := publicLyricsV2SekaiFixture(20)
	gameRef := model.LyricsSourceComponentRef{RenditionKey: source.FixedIdentities[0].RenditionKey}
	game := source.Full
	for lineIndex := range game.Lines {
		for segmentIndex := range game.Lines[lineIndex].Segments {
			game.Lines[lineIndex].Segments[segmentIndex].PerformerIDs = []string{}
		}
		game.Lines[lineIndex].TrailingPerformerIDs = []string{"歌唱者-21"}
	}
	gameDocument := model.LyricsAvailabilityDocument{
		SchemaVersion:   model.LyricsAvailabilityDocumentSchemaVersion,
		State:           model.LyricsAvailabilityStateGameOnly,
		ReasonCode:      model.LyricsSourceVersionReasonTaggedGameOnlyFullFromVocaloid,
		FixedIdentities: []model.LyricsSourceFixedIdentity{source.FixedIdentities[0]},
		Provenance: model.LyricsAvailabilityComponentProvenance{
			GameText: &gameRef, PerformerSegmentation: &gameRef, Ruby: &gameRef, VersionEvidence: &gameRef,
		},
		Game: &game,
	}
	appendRecoveryPublicAvailability(t, &content, batchSHA, createdAt, 20, gameDocument)
	for _, component := range []string{"game_text", "performer_segmentation", "ruby", "version_evidence"} {
		content.RecoveryContributions = append(content.RecoveryContributions, LyricsRecoveryContributionBackupRecord{
			BatchSHA256: batchSHA, MusicID: 20, Component: component, RenditionKey: gameRef.RenditionKey,
		})
	}

	appendRecoveryPublicAvailability(t, &content, batchSHA, createdAt, 30, model.LyricsAvailabilityDocument{
		SchemaVersion: model.LyricsAvailabilityDocumentSchemaVersion,
		State:         model.LyricsAvailabilityStateSatisfiedNoLyrics, NoLyricsReason: model.LyricsAvailabilityNoLyricsCatalogInstrumental,
		FixedIdentities: []model.LyricsSourceFixedIdentity{},
	})
	for musicID, state := range map[int]model.LyricsAvailabilityState{
		40: model.LyricsAvailabilityStateAmbiguous,
		50: model.LyricsAvailabilityStateMissing,
		60: model.LyricsAvailabilityStateIncomplete,
		70: model.LyricsAvailabilityStateFailed,
	} {
		appendRecoveryPublicAvailability(t, &content, batchSHA, createdAt, musicID, model.LyricsAvailabilityDocument{
			SchemaVersion: model.LyricsAvailabilityDocumentSchemaVersion,
			State:         state, ReasonCode: model.LyricsSourceVersionReasonVersionConflict,
			FixedIdentities: []model.LyricsSourceFixedIdentity{},
		})
	}
	sort.Slice(content.RecoveryItems, func(left, right int) bool {
		return content.RecoveryItems[left].MusicID < content.RecoveryItems[right].MusicID
	})
	sort.Slice(content.AvailabilityDocuments, func(left, right int) bool {
		return content.AvailabilityDocuments[left].MusicID < content.AvailabilityDocuments[right].MusicID
	})
	return content, batchSHA
}

func appendRecoveryPublicAvailability(t *testing.T, content *LyricsContentExport, batchSHA string, createdAt int64,
	musicID int, document model.LyricsAvailabilityDocument,
) {
	t.Helper()
	if err := model.ValidateLyricsAvailabilityDocument(document); err != nil {
		t.Fatalf("music %d availability fixture: %v", musicID, err)
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	documentSHA := hex.EncodeToString(digest[:])
	resultSHA := recoveryBackupTestSHA("public-result:" + string(document.State))
	content.RecoveryItems = append(content.RecoveryItems, LyricsRecoveryItemBackupRecord{
		BatchSHA256: batchSHA, MusicID: musicID, JapaneseTitle: "状態試験曲",
		TargetMusicID: musicID, AssociationMusicIDsJSON: "[]", State: string(document.State),
		ResultSHA256: resultSHA, AvailabilityDocumentSHA256: documentSHA, CreatedAt: createdAt,
	})
	content.AvailabilityDocuments = append(content.AvailabilityDocuments, LyricsAvailabilityDocumentBackupRecord{
		AvailabilityDocumentID: int64(len(content.AvailabilityDocuments) + 1), BatchSHA256: batchSHA,
		MusicID: musicID, SchemaVersion: document.SchemaVersion, State: string(document.State),
		ReasonCode: string(document.ReasonCode), NoLyricsReason: document.NoLyricsReason,
		DocumentJSON: string(body), DocumentSHA256: documentSHA, ResultSHA256: resultSHA, CreatedAt: createdAt,
	})
}

func assertRecoveryPublicJSONHasNoPrivateOrRomajiFields(t *testing.T, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{
		"romaji", "romanization", "romanized", "rawbytes", "documentjson", "fixedidentityjson",
		"privatereview", "sourceurl", "sourcepageid", "sourcerevisionid", "sourcesha1", "sourcefetchedat",
		"revisiontimestamp", "compositionrenditionkey", "versionreason", "sourcenote", "licensenote",
		"indexevidencerefs", "full_text", "game_text", "performer_segmentation", "version_evidence",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("public JSON leaked %q: %s", forbidden, body)
		}
	}
}
