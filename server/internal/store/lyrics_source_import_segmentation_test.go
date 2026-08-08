package store

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func TestApprovedVocaloidFullImportNeverAppliesCatalogPerformerFallbackAndReplays(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
		MusicID: 10, JapaneseTitle: "Vocaloid限定曲", ProducerMetadata: "制作者",
		Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true,
		Vocals: []model.CatalogVocalSignal{{
			VocalID: 1, VocalType: "original_song", CharacterType: "game_character", CharacterID: 21,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPerformerCatalog([]PerformerCatalogRecord{{
		PerformerID: 21, JapaneseName: "初音ミク", EnglishName: "Miku",
	}}); err != nil {
		t.Fatal(err)
	}
	identity, err := s.CatalogMusicIdentity(10)
	if err != nil {
		t.Fatal(err)
	}

	wikitext := []byte("== Lyrics ==\n初音歌う")
	wikitextSHA1 := sha1.Sum(wikitext)
	rawSHA256 := sha256.Sum256(wikitext)
	rawSHA256Hex := hex.EncodeToString(rawSHA256[:])
	fetchedAt := time.Date(2026, time.July, 31, 12, 34, 56, 123000000, time.UTC)
	fetchedAtText := fetchedAt.Format(time.RFC3339Nano)
	canonicalURL := "https://vocaloid.fandom.com/wiki/Vocaloid%E9%99%90%E5%AE%9A%E6%9B%B2?oldid=34"
	evidenceID := lyricssource.MediaWikiRevisionAcquisitionEvidenceID(
		model.LyricsSourceProviderVocaloidFandom, "fetch:vocaloid-fandom:12", fetchedAtText, rawSHA256Hex,
	)
	evidence := lyricssource.IndexEvidence{
		EvidenceID: evidenceID, SHA256: rawSHA256Hex,
		Kind:     lyricssource.IndexEvidenceKindMediaWikiRevision,
		Provider: model.LyricsSourceProviderVocaloidFandom, Origin: model.LyricsSourceOriginVocaloidFandom,
		PageID: 12, RevisionID: 34, MediaWikiSHA1: hex.EncodeToString(wikitextSHA1[:]), Title: "Vocaloid限定曲",
		CanonicalURL: canonicalURL, Categories: []string{"Lyrics", "Songs"},
		FetchedAt: fetchedAtText, Raw: append([]byte{}, wikitext...),
		RawSHA256: rawSHA256Hex,
	}
	candidate := lyricssource.Candidate{
		Provider: evidence.Provider, Origin: evidence.Origin, PageID: evidence.PageID, RevisionID: evidence.RevisionID,
		SHA1: evidence.MediaWikiSHA1, Title: evidence.Title, CanonicalURL: evidence.CanonicalURL,
		Categories: append([]string{}, evidence.Categories...), Section: "Lyrics/Vocaloid Version", RenditionKey: "full-vocaloid",
		VersionReason:     model.LyricsSourceVersionReasonUntaggedFullOnly,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidence.EvidenceID, SHA256: evidence.SHA256}},
		IndexEvidence:     []lyricssource.IndexEvidence{evidence},
	}
	job, _, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Provider: model.LyricsSourceProviderVocaloidFandom, Kind: model.LyricsDiscoveryJobFetchRevision,
		Target: model.LyricsDiscoveryJobTarget{
			MusicID: 10, PageID: candidate.PageID, RevisionID: candidate.RevisionID, ExpectedSHA1: candidate.SHA1,
			CatalogFingerprint: identity.CatalogFingerprint, PolicyVersion: model.LyricsMatchingPolicyVersion,
			FixedCandidate: legacyLyricsDiscoveryCandidateIdentity(&candidate),
		},
		FixedCandidate: &candidate, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "vocaloid-import-worker", Duration: time.Minute, Provider: candidate.Provider,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("Vocaloid claim=%+v err=%v", leased, err)
	}
	fixed := lyricssource.FixedRevision{
		Provider: candidate.Provider, Origin: candidate.Origin,
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
		PageTitle: candidate.Title, CanonicalURL: candidate.CanonicalURL, Categories: append([]string{}, candidate.Categories...),
		FetchedAt: fetchedAt, Wikitext: append([]byte{}, wikitext...),
		Lines: []lyricssource.ExtractedLine{{Japanese: "初音歌う"}},
		Extraction: lyricssource.Extraction{
			Version:    lyricssource.LyricsVersion{Kind: "vocaloid", Label: "Vocaloid Version"},
			Performers: []lyricssource.Performer{}, RubyGeneratorVersion: "kagome-ipadic-v1",
			Lines: []lyricssource.StructuredLine{{
				Japanese: "初音歌う", Segments: []lyricssource.LyricsSegment{{
					Text: "初音歌う", PerformerIDs: []string{}, Ruby: []lyricssource.RubySpan{{Text: "初音歌う"}},
				}}, TrailingPerformerIDs: []string{},
			}},
		},
		Section: candidate.Section, RenditionKey: candidate.RenditionKey, VersionReason: candidate.VersionReason,
		IndexEvidenceRefs: append([]model.LyricsSourceIndexEvidenceRef{}, candidate.IndexEvidenceRefs...),
		IndexEvidence:     []lyricssource.IndexEvidence{evidence},
	}
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		Fixed: fixed, Evidence: []model.LyricsSourceEvidence{{
			RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, _, err := s.DecideLyricsSourceReview(context.Background(), LyricsSourceReviewDecisionParams{
		ReviewID: review.ReviewID, Gate: LyricsSourceReviewGateOverall, Decision: LyricsSourceReviewDecisionApproved,
		ExpectedVersion: review.Version, Actor: "admin", IdempotencyKey: "approved-vocaloid-import-0001",
	})
	if err != nil || approved.State != LyricsSourceReviewStateApproved {
		t.Fatalf("Vocaloid approval=%+v err=%v", approved, err)
	}

	imported, changed, err := s.ImportApprovedLyricsSource(context.Background(), review.ReviewID, "admin")
	if err != nil || !changed || len(imported.Lines) != 1 || len(imported.Lines[0].Segments) != 1 {
		t.Fatalf("Vocaloid import=%+v changed=%t err=%v", imported, changed, err)
	}
	segment := imported.Lines[0].Segments[0]
	if segment.Text != imported.Lines[0].Japanese || segment.PerformerIDs == nil || len(segment.PerformerIDs) != 0 ||
		imported.SourceFetchedAt != fetchedAt.Format(time.RFC3339Nano) {
		t.Fatalf("Vocaloid imported segment=%+v fetchedAt=%q", segment, imported.SourceFetchedAt)
	}
	var performerJSON string
	if err := database.QueryRow(`SELECT performer_ids_json FROM song_lyric_segments WHERE music_id=10`).Scan(&performerJSON); err != nil {
		t.Fatal(err)
	}
	if performerJSON != "[]" {
		t.Fatalf("Vocaloid persisted performers=%q", performerJSON)
	}
	replay, changed, err := s.ImportApprovedLyricsSource(context.Background(), review.ReviewID, "admin")
	if err != nil || changed || replay.Revision != imported.Revision ||
		len(replay.Lines[0].Segments[0].PerformerIDs) != 0 || strings.TrimSpace(replay.Lines[0].Segments[0].Text) == "" {
		t.Fatalf("Vocaloid replay=%+v changed=%t err=%v", replay, changed, err)
	}
}
