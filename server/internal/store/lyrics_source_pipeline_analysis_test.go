package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"strings"
	"testing"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func TestLyricsSourceAnalysisCanonicalizesPerformerReferencesAndRubyBeforeSQLAndHash(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{VocalID: 1, VocalType: "sekai", CharacterType: "game_character", CharacterID: 21}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{{PerformerID: 21, JapaneseName: "初音ミク"}}); err != nil {
		t.Fatal(err)
	}
	identity, err := s.CatalogMusicIdentity(10)
	if err != nil {
		t.Fatal(err)
	}
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "fetch-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	const englishLyric = "Jo-jo-jo-journey"
	const sourcePerformerID = "source-miku-ref"
	const sourcePerformerName = "Hatsune Miku"
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n"+englishLyric), nil)
	fixed.Extraction = lyricssource.Extraction{
		Version: lyricssource.LyricsVersion{Kind: "sekai", Label: "Project SEKAI Version"},
		Performers: []lyricssource.Performer{{
			PerformerID: sourcePerformerID, Name: sourcePerformerName, Color: "#33CCBB",
		}},
		RubyGeneratorVersion: "sekaipedia-romaji-kana-v1",
		Lines: []lyricssource.StructuredLine{{
			Japanese: englishLyric,
			Segments: []lyricssource.LyricsSegment{{
				Text: englishLyric, PerformerIDs: []string{sourcePerformerID},
				Ruby: []lyricssource.RubySpan{{Text: englishLyric}},
			}},
			TrailingPerformerIDs: []string{sourcePerformerID},
		}},
	}
	evidence := []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed: fixed, Evidence: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}

	var performersJSON, rubyGeneratorVersion, linesJSON, selectedVersionJSON string
	var storedLinesSHA, analysisSHA, artifactSHA string
	if err := database.QueryRow(`SELECT a.performers_json,a.ruby_generator_version,a.extracted_lines_json,
		a.selected_version_json,a.extracted_lines_sha256,a.analysis_sha256,s.artifact_sha256
		FROM lyrics_source_analyses a JOIN lyrics_source_artifacts s ON s.artifact_id=a.artifact_id
		WHERE a.analysis_id=?`, review.AnalysisID).Scan(
		&performersJSON, &rubyGeneratorVersion, &linesJSON, &selectedVersionJSON,
		&storedLinesSHA, &analysisSHA, &artifactSHA,
	); err != nil {
		t.Fatal(err)
	}
	persistedText := strings.ToLower(strings.Join([]string{performersJSON, rubyGeneratorVersion, linesJSON}, "\n"))
	for _, prohibited := range []string{sourcePerformerID, strings.ToLower(sourcePerformerName), "sekaipedia-romaji"} {
		if strings.Contains(persistedText, strings.ToLower(prohibited)) {
			t.Fatalf("SQL persistence retained source-local value %q: %s", prohibited, persistedText)
		}
	}
	var performers []model.LyricsSourcePerformer
	var lines []model.LyricsSourceExtractedLine
	var selectedVersion model.LyricsSourceVersion
	if err := json.Unmarshal([]byte(performersJSON), &performers); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(linesJSON), &lines); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(selectedVersionJSON), &selectedVersion); err != nil {
		t.Fatal(err)
	}
	if len(performers) != 1 || performers[0].PerformerID != "歌唱者-21" || performers[0].Name != "初音ミク" ||
		performers[0].Color != "#33CCBB" || rubyGeneratorVersion != "sekaipedia-ruby-kana-v1" || len(lines) != 1 ||
		lines[0].Japanese != englishLyric || len(lines[0].Segments) != 1 ||
		strings.Join(lines[0].Segments[0].PerformerIDs, ",") != "歌唱者-21" ||
		strings.Join(lines[0].TrailingPerformerIDs, ",") != "歌唱者-21" {
		t.Fatalf("canonical SQL performers=%+v ruby=%q lines=%+v", performers, rubyGeneratorVersion, lines)
	}
	if got := model.LyricsSourceExtractedLinesSHA256(lines); got != storedLinesSHA {
		t.Fatalf("stored extracted-lines hash=%q want=%q", storedLinesSHA, got)
	}
	envelope := lyricsSourceAnalysisEnvelope{
		Version: 2, ArtifactSHA256: artifactSHA, MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint,
		MatchingPolicyVersion:    model.LyricsMatchingPolicyVersion,
		RestrictionPolicyVersion: model.LyricsRestrictionPolicyVersion, ExtractorVersion: model.LyricsExtractorVersion,
		MatchOutcome: "matched", RestrictionOutcome: "clear", ExtractionOutcome: "extracted",
		MatchingEvidence: evidence, RestrictionRuleIDs: []string{}, SelectedVersion: selectedVersion,
		Performers: performers, RubyGeneratorVersion: rubyGeneratorVersion,
		ExtractedLines: lines, ExtractedLinesSHA256: storedLinesSHA,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if want := hex.EncodeToString(digest[:]); analysisSHA != want {
		t.Fatalf("analysis hash=%q want canonical hash=%q", analysisSHA, want)
	}

	detail, err := s.GetLyricsSourceReviewDetail(context.Background(), review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	detailBody, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	lowerDetail := strings.ToLower(string(detailBody))
	if strings.Contains(lowerDetail, sourcePerformerID) || strings.Contains(lowerDetail, strings.ToLower(sourcePerformerName)) ||
		strings.Contains(lowerDetail, "sekaipedia-romaji") || !strings.Contains(string(detailBody), englishLyric) {
		t.Fatalf("canonical store readback=%s", detailBody)
	}
}

func TestLyricsSourceAnalysisPersistenceAndAPIReadbackOmitUnknownPerformerWithoutChangingEnglishLyrics(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{VocalID: 1, VocalType: "sekai", CharacterType: "game_character", CharacterID: 21}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{{PerformerID: 21, JapaneseName: "初音ミク"}}); err != nil {
		t.Fatal(err)
	}
	identity, err := s.CatalogMusicIdentity(10)
	if err != nil {
		t.Fatal(err)
	}
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "fetch-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\nJo-jo-jo-journey"), nil)
	fixed.Extraction = lyricssource.Extraction{
		Version:              lyricssource.LyricsVersion{Kind: "sekai", Label: "Project SEKAI Version"},
		Performers:           []lyricssource.Performer{{PerformerID: "mikito-p", Name: "Mikito-P", Color: "#33CCBB"}},
		RubyGeneratorVersion: "sekaipedia-romaji-kana-v1",
		Lines: []lyricssource.StructuredLine{{
			Japanese: "Jo-jo-jo-journey",
			Segments: []lyricssource.LyricsSegment{{
				Text: "Jo-jo-jo-journey", PerformerIDs: []string{"mikito-p"},
				Ruby: []lyricssource.RubySpan{{Text: "Jo-jo-jo-journey"}},
			}},
			TrailingPerformerIDs: []string{"mikito-p"},
		}},
	}
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed: fixed, Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var performersJSON, rubyGeneratorVersion, linesJSON string
	if err := database.QueryRow(`SELECT performers_json,ruby_generator_version,extracted_lines_json
		FROM lyrics_source_analyses WHERE analysis_id=?`, review.AnalysisID).
		Scan(&performersJSON, &rubyGeneratorVersion, &linesJSON); err != nil {
		t.Fatal(err)
	}
	stored := strings.ToLower(strings.Join([]string{performersJSON, rubyGeneratorVersion, linesJSON}, "\n"))
	if performersJSON != "[]" || rubyGeneratorVersion != "sekaipedia-ruby-kana-v1" || strings.Contains(stored, "mikito") ||
		strings.Contains(stored, "sekaipedia-romaji") || !strings.Contains(linesJSON, "Jo-jo-jo-journey") {
		t.Fatalf("unsafe analysis persistence performers=%s ruby=%q lines=%s", performersJSON, rubyGeneratorVersion, linesJSON)
	}
	detail, err := s.GetLyricsSourceReviewDetail(context.Background(), review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)), "mikito") || strings.Contains(string(body), "sekaipedia-romaji") ||
		!strings.Contains(string(body), "Jo-jo-jo-journey") || detail.Analysis == nil || len(detail.Analysis.Performers) != 0 ||
		len(detail.Analysis.ExtractedLines[0].Segments[0].PerformerIDs) != 0 {
		t.Fatalf("unsafe source-review readback=%s", body)
	}

	unsafePerformers, _ := json.Marshal([]model.LyricsSourcePerformer{{PerformerID: "mikito-p", Name: "Mikito-P"}})
	unsafeLines := []model.LyricsSourceExtractedLine{{
		Japanese: "Jo-jo-jo-journey",
		Segments: []model.LyricsSourceSegment{{
			Text: "Jo-jo-jo-journey", PerformerIDs: []string{"mikito-p"},
			Ruby: []model.LyricsSourceRubySpan{{Text: "Jo-jo-jo-journey"}},
		}},
		TrailingPerformerIDs: []string{"mikito-p"},
	}}
	unsafeLinesJSON, _ := json.Marshal(unsafeLines)
	if _, err := database.Exec(`DROP TRIGGER lyrics_source_analyses_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE lyrics_source_analyses SET performers_json=?,extracted_lines_json=? WHERE analysis_id=?`,
		string(unsafePerformers), string(unsafeLinesJSON), review.AnalysisID); err != nil {
		t.Fatal(err)
	}
	historical, err := s.GetLyricsSourceReviewDetail(context.Background(), review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	historicalBody, err := json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(historicalBody)), "mikito") || !strings.Contains(string(historicalBody), "Jo-jo-jo-journey") ||
		historical.Analysis == nil || len(historical.Analysis.Performers) != 0 {
		t.Fatalf("historical unsafe source-review readback=%s", historicalBody)
	}
}

func TestLyricsSourceAnalysisConflictErrorDoesNotEchoPerformerValues(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{VocalID: 1, VocalType: "sekai", CharacterType: "game_character", CharacterID: 21}},
	}}); err != nil {
		t.Fatal(err)
	}
	identity, err := s.CatalogMusicIdentity(10)
	if err != nil {
		t.Fatal(err)
	}
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "fetch-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n歌う"), nil)
	fixed.Extraction = lyricssource.Extraction{
		Version:              lyricssource.LyricsVersion{Kind: "sekai", Label: "Project SEKAI Version"},
		Performers:           []lyricssource.Performer{{PerformerID: "miku", Name: "Hoshino Ichika"}},
		RubyGeneratorVersion: "kagome-ipadic-v1",
		Lines: []lyricssource.StructuredLine{{
			Japanese: "歌う", Segments: []lyricssource.LyricsSegment{{
				Text: "歌う", PerformerIDs: []string{"miku"}, Ruby: []lyricssource.RubySpan{{Text: "歌う"}},
			}}, TrailingPerformerIDs: []string{"miku"},
		}},
	}
	_, err = s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed: fixed, Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}},
	})
	if err == nil {
		t.Fatal("conflicting performer values were accepted")
	}
	lowerError := strings.ToLower(err.Error())
	for _, prohibited := range []string{"miku", "hoshino", "ichika"} {
		if strings.Contains(lowerError, prohibited) {
			t.Fatal("store error chain echoed prohibited performer metadata")
		}
	}
	for _, table := range []string{"lyrics_source_artifacts", "lyrics_source_analyses", "lyrics_source_review_items"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("conflicting performer wrote %s=%d err=%v", table, count, err)
		}
	}
}

func TestLyricsSourceAnalysisRejectsArbitraryRubyGeneratorWithoutEcho(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "fetch-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	const arbitraryGenerator = "provider-secret-romanizer-v9"
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n歌う"),
		[]lyricssource.ExtractedLine{{Japanese: "歌う"}})
	fixed.Extraction.RubyGeneratorVersion = arbitraryGenerator
	_, err = s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed: fixed, Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}},
	})
	if err == nil {
		t.Fatal("arbitrary ruby generator was accepted")
	}
	if strings.Contains(err.Error(), arbitraryGenerator) || strings.Contains(strings.ToLower(err.Error()), "romanizer") {
		t.Fatalf("store error echoed arbitrary ruby generator: %v", err)
	}
	for _, table := range []string{"lyrics_source_artifacts", "lyrics_source_analyses", "lyrics_source_review_items"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("arbitrary generator wrote %s=%d err=%v", table, count, err)
		}
	}
}

func TestLyricsSourceReviewDetailAndSharedBackupExcludePrivateRawArtifact(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	rawSentinel := "PRIVATE-WIKITEXT-SENTINEL"
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n"+rawSentinel),
		[]lyricssource.ExtractedLine{{Japanese: "API-visible extracted line"}})
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetLyricsSourceReviewDetail(context.Background(), review.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{rawSentinel, "rawWikitext", "raw_wikitext", "artifactId", "analysisId", "artifactSha256", "analysisSha256", "extractedLinesSha256"} {
		if strings.Contains(string(detailJSON), forbidden) {
			t.Fatalf("detail leaked private field %q: %s", forbidden, detailJSON)
		}
	}
	exported, err := s.ExportLyricsContent()
	if err != nil {
		t.Fatal(err)
	}
	exportJSON, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exportJSON), rawSentinel) || strings.Contains(string(exportJSON), "lyrics_source_") {
		t.Fatalf("shared backup leaked private source data: %s", exportJSON)
	}
	for _, table := range []string{"lyrics_source_artifacts", "lyrics_source_analyses", "lyrics_source_review_items"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("private table %s count=%d err=%v", table, count, err)
		}
	}
}
