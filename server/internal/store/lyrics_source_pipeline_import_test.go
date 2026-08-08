package store

import (
	"context"

	"errors"
	"fmt"

	"strings"
	"testing"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func TestApprovedLyricsSourceImportCreatesOriginalOnlyDraftAndPublishes(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", ProducerMetadata: "制作者", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{
			{VocalID: 1, VocalType: "sekai", CharacterType: "game_character", CharacterID: 19},
			{VocalID: 1, VocalType: "sekai", CharacterType: "game_character", CharacterID: 25},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{
		{PerformerID: 19, JapaneseName: "東雲 絵名"},
		{PerformerID: 25, JapaneseName: "MEIKO"},
	}); err != nil {
		t.Fatal(err)
	}
	identity, err := s.CatalogMusicIdentity(10)
	if err != nil {
		t.Fatal(err)
	}
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("leased=%+v err=%v", leased, err)
	}
	fetchedAt := time.Now().UTC().Truncate(time.Millisecond)
	fixed := pipelineFixedRevision(candidate, fetchedAt, []byte("== Lyrics ==\n合成歌詞"), nil)
	fixed.Extraction = lyricssource.Extraction{
		Version:              lyricssource.LyricsVersion{Kind: "sekai", Label: "25-ji, Nightcord de. Version"},
		Performers:           []lyricssource.Performer{{PerformerID: "ena", Name: "Ena Shinonome"}, {PerformerID: "meiko", Name: "MEIKO"}},
		RubyGeneratorVersion: "kagome-ipadic-v1",
		Lines: []lyricssource.StructuredLine{
			{Japanese: "合成", Segments: []lyricssource.LyricsSegment{{
				Text: "合成", PerformerIDs: []string{"ena"}, Ruby: []lyricssource.RubySpan{{Text: "合成"}},
			}}},
			{Japanese: "歌詞", StanzaBreakBefore: true, Segments: []lyricssource.LyricsSegment{{
				Text: "歌詞", Ruby: []lyricssource.RubySpan{{Text: "歌詞"}},
			}}, TrailingPerformerIDs: []string{"meiko", "ena"}},
		},
	}
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed:    fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
		ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		ExpectedVersion: review.Version, Actor: "admin", IdempotencyKey: "approved-import-test-0001",
	})
	if err != nil || approved.State != LyricsSourceReviewStateApproved {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	imported, changed, err := s.ImportApprovedLyricsSource(context.Background(), review.ReviewID, "admin")
	if err != nil || !changed || imported.Revision != 1 || imported.Status != "draft" || len(imported.Lines) != 2 {
		t.Fatalf("imported=%+v changed=%t err=%v", imported, changed, err)
	}
	if imported.SourceFetchedAt != fetchedAt.Format(time.RFC3339Nano) {
		t.Fatalf("imported sourceFetchedAt=%q want=%q", imported.SourceFetchedAt, fetchedAt.Format(time.RFC3339Nano))
	}
	if imported.Lines[0].Chinese != "" || imported.Lines[0].English != "" ||
		len(imported.Lines[0].Segments) != 1 || strings.Join(intsToStrings(imported.Lines[0].Segments[0].PerformerIDs), ",") != "19" ||
		strings.Join(intsToStrings(imported.Lines[1].Segments[0].PerformerIDs), ",") != "25,19" {
		t.Fatalf("imported lines=%+v", imported.Lines)
	}
	published, err := s.PublishLyrics(imported.MusicID, imported.Revision, "admin")
	if err != nil || published.Status != "published" {
		t.Fatalf("published=%+v err=%v", published, err)
	}
	_, details, err := s.PublishedLyrics()
	if err != nil || details[10].Lines[0].Chinese != "" || details[10].Lines[0].English != "" {
		t.Fatalf("public original-only=%+v err=%v", details[10], err)
	}
	var publications int
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics_publications WHERE music_id=10`).Scan(&publications); err != nil || publications != 1 {
		t.Fatalf("publications=%d err=%v", publications, err)
	}
	replayed, changed, err := s.ImportApprovedLyricsSource(context.Background(), review.ReviewID, "admin")
	if err != nil || changed || replayed.Revision != imported.Revision || replayed.Status != "published" {
		t.Fatalf("approved source replay=%+v changed=%t err=%v", replayed, changed, err)
	}
}

func TestApprovedLyricsSourceImportRejectsPendingReviewWithoutWrites(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("leased=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n合成歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ImportApprovedLyricsSource(context.Background(), review.ReviewID, "admin"); !errors.Is(err, ErrLyricsSourceReviewNotApproved) {
		t.Fatalf("pending import error=%v", err)
	}
	var lyrics, publications int
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics`).Scan(&lyrics); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics_publications`).Scan(&publications); err != nil {
		t.Fatal(err)
	}
	if lyrics != 0 || publications != 0 {
		t.Fatalf("pending import wrote lyrics=%d publications=%d", lyrics, publications)
	}
}

func createApprovedLyricsSourceImportReview(t *testing.T, s *Store, fixed lyricssource.FixedRevision, key string) model.LyricsSourceReviewItem {
	t.Helper()
	identity, err := s.CatalogMusicIdentity(10)
	if err != nil {
		t.Fatal(err)
	}
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "approved-import-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("leased=%+v err=%v", leased, err)
	}
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed: fixed, Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
		ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		ExpectedVersion: review.Version, Actor: "admin", IdempotencyKey: key,
	})
	if err != nil || approved.State != LyricsSourceReviewStateApproved {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	return approved
}

func approvedLyricsSourceFixedRevision(sourceID, sourceName string) lyricssource.FixedRevision {
	fixed := pipelineFixedRevision(
		pipelineProviderCandidate(),
		time.Now().UTC().Truncate(time.Millisecond),
		[]byte("== Lyrics ==\n合成歌詞"),
		nil,
	)
	fixed.Extraction = lyricssource.Extraction{
		Version:    lyricssource.LyricsVersion{Kind: "sekai", Label: "Project SEKAI Version"},
		Performers: []lyricssource.Performer{{PerformerID: sourceID, Name: sourceName}}, RubyGeneratorVersion: "kagome-ipadic-v1",
		Lines: []lyricssource.StructuredLine{{Japanese: "合成歌詞", Segments: []lyricssource.LyricsSegment{{
			Text: "合成歌詞", PerformerIDs: []string{sourceID}, Ruby: []lyricssource.RubySpan{{Text: "合成歌詞"}},
		}}}},
	}
	return fixed
}

func TestApprovedLyricsSourceImportRejectsSameTitleGroupDriftWithoutWrites(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{VocalID: 1, VocalType: "sekai"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{{PerformerID: 21, JapaneseName: "初音ミク", EnglishName: "Miku"}}); err != nil {
		t.Fatal(err)
	}
	review := createApprovedLyricsSourceImportReview(t, s, approvedLyricsSourceFixedRevision("miku", "Miku"), "approved-group-drift-0001")
	initial, err := loadApprovedLyricsSourceImport(context.Background(), s.db, review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 11, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", LyricsVersion: "game_size", LyricsVersionKnown: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.importApprovedLyricsSourceSnapshot(context.Background(), review.ReviewID, "admin", initial); !errors.Is(err, ErrLyricsSourceReviewNotApproved) {
		t.Fatalf("same-title group drift error=%v", err)
	}
	var lyrics int
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics`).Scan(&lyrics); err != nil || lyrics != 0 {
		t.Fatalf("same-title group drift wrote lyrics=%d err=%v", lyrics, err)
	}
}

func TestApprovedLyricsSourceImportSnapshotsCompareUnexportedState(t *testing.T) {
	base := approvedLyricsSourceImport{
		reviewID: 1, analysisID: 2, musicID: 10, catalogFingerprint: strings.Repeat("a", 64),
		mediaWikiSHA1: strings.Repeat("b", 40), selectedVersion: model.LyricsSourceVersion{Kind: "sekai", Label: "SEKAI"},
		extractedLines: []model.LyricsSourceExtractedLine{{Japanese: "歌詞"}},
		associations:   []model.LyricsSourceAssociation{{MusicID: 10, CatalogFingerprint: strings.Repeat("a", 64), Kind: model.LyricsCatalogTargetFullTarget}},
	}
	if !sameApprovedLyricsSourceImport(base, base) {
		t.Fatal("identical approved-source snapshots compared unequal")
	}
	for name, mutate := range map[string]func(*approvedLyricsSourceImport){
		"scalar": func(snapshot *approvedLyricsSourceImport) { snapshot.analysisID++ },
		"nested line": func(snapshot *approvedLyricsSourceImport) {
			snapshot.extractedLines = []model.LyricsSourceExtractedLine{{Japanese: "別の歌詞"}}
		},
		"classification": func(snapshot *approvedLyricsSourceImport) {
			snapshot.associations = append([]model.LyricsSourceAssociation(nil), snapshot.associations...)
			snapshot.associations[0].Kind = model.LyricsCatalogTargetGameSizeEvidence
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if sameApprovedLyricsSourceImport(base, changed) {
				t.Fatal("different approved-source snapshots compared equal")
			}
		})
	}
}

func TestApprovedLyricsSourceImportDoesNotReviveUnknownPerformerThroughCatalogAliases(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{VocalID: 1, VocalType: "sekai"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{
		{PerformerID: 30, JapaneseName: "歌手甲", EnglishName: "Shared Alias"},
		{PerformerID: 31, JapaneseName: "歌手乙", EnglishName: "Other Alias"},
	}); err != nil {
		t.Fatal(err)
	}
	review := createApprovedLyricsSourceImportReview(t, s, approvedLyricsSourceFixedRevision("external-singer", "Shared Alias"), "approved-alias-remap-0001")
	initial, err := loadApprovedLyricsSourceImport(context.Background(), s.db, review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{
		{PerformerID: 30, JapaneseName: "歌手甲", EnglishName: "Old Alias"},
		{PerformerID: 31, JapaneseName: "歌手乙", EnglishName: "Shared Alias"},
	}); err != nil {
		t.Fatal(err)
	}
	imported, changed, err := s.importApprovedLyricsSourceSnapshot(context.Background(), review.ReviewID, "admin", initial)
	if err != nil || !changed {
		t.Fatalf("alias-remapped import=%+v changed=%t err=%v", imported, changed, err)
	}
	if got := imported.Lines[0].Segments[0].PerformerIDs; got == nil || len(got) != 0 {
		t.Fatalf("unknown performer was revived through a later catalog alias: %v", got)
	}
}

func TestApprovedLyricsSourceImportMapsAuditedOutsidePerformer(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{
			VocalID: 1, VocalType: "sekai", CharacterType: "outside_character", CharacterID: 1,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	review := createApprovedLyricsSourceImportReview(
		t,
		s,
		approvedLyricsSourceFixedRevision("gumi", "GUMI"),
		"approved-outside-performer-0001",
	)
	imported, changed, err := s.ImportApprovedLyricsSource(context.Background(), review.ReviewID, "admin")
	if err != nil || !changed {
		t.Fatalf("outside-only import=%+v changed=%t err=%v", imported, changed, err)
	}
	performerIDs := imported.Lines[0].Segments[0].PerformerIDs
	if len(performerIDs) != 1 || performerIDs[0] != 1001 {
		t.Fatalf("audited outside performer projection=%v", performerIDs)
	}
	var performerJSON string
	if err := database.QueryRow(`SELECT performer_ids_json FROM song_lyric_segments WHERE music_id=10`).Scan(&performerJSON); err != nil {
		t.Fatal(err)
	}
	if performerJSON != "[1001]" {
		t.Fatalf("audited outside persisted performers=%q", performerJSON)
	}
	if code, details, _ := validateLyrics(imported, map[int]bool{1001: true}, true); code != "" {
		t.Fatalf("audited outside publication code=%q details=%v", code, details)
	}
}

func intsToStrings(values []int) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = fmt.Sprintf("%d", value)
	}
	return result
}
