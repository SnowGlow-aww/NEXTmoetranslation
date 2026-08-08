package store

import (
	"context"

	"encoding/json"

	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moesekai/server/internal/db"
	"moesekai/server/internal/lyricsdiscovery"
	"moesekai/server/internal/lyricssource"
	"moesekai/server/internal/model"
)

func openLyricsSourcePipelineStore(t *testing.T) (*Store, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "lyrics-source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database), database
}

func pipelineFixedCandidate() *model.LyricsSourceCandidateIdentity {
	return &model.LyricsSourceCandidateIdentity{
		PageID: 12, RevisionID: 34, SHA1: strings.Repeat("a", 40), Title: "合成試験曲",
		CanonicalURL: "https://vocaloid.fandom.com/wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=34",
		Categories:   []string{"Songs"},
	}
}

func pipelineProviderCandidate() lyricssource.Candidate {
	return testRevisionCandidate(
		model.LyricsSourceProviderVocaloidFandom,
		12,
		34,
		"合成試験曲",
		[]string{"Songs"},
		"Lyrics",
		"full-vocaloid",
		model.LyricsSourceVersionReasonUntaggedFullOnly,
		[]byte("provider-aware pipeline index evidence: vocaloid fandom"),
	)
}

func providerAwarePipelineCandidate(provider model.LyricsSourceProvider) lyricssource.Candidate {
	section, renditionKey := "Lyrics", "full-vocaloid"
	if provider == model.LyricsSourceProviderMoegirl {
		section, renditionKey = "歌词", "full-sekai"
	}
	return testRevisionCandidate(
		provider,
		12,
		34,
		"合成試験曲",
		[]string{"Songs"},
		section,
		renditionKey,
		model.LyricsSourceVersionReasonUntaggedFullOnly,
		[]byte("provider-aware pipeline index evidence: "+string(provider)),
	)
}

func pipelineFixedRevision(
	candidate lyricssource.Candidate,
	fetchedAt time.Time,
	wikitext []byte,
	lines []lyricssource.ExtractedLine,
) lyricssource.FixedRevision {
	structuredLines := make([]lyricssource.StructuredLine, len(lines))
	for index, line := range lines {
		structuredLines[index] = lyricssource.StructuredLine{
			Japanese: line.Japanese, StanzaBreakBefore: line.StanzaBreakBefore,
			Segments: []lyricssource.LyricsSegment{{
				Text: line.Japanese, PerformerIDs: []string{}, Ruby: []lyricssource.RubySpan{{Text: line.Japanese}},
			}},
			TrailingPerformerIDs: []string{},
		}
	}
	fixed := lyricssource.FixedRevision{
		FetchedAt: fetchedAt.UTC(),
		Wikitext:  append([]byte{}, wikitext...),
		Lines:     append([]lyricssource.ExtractedLine{}, lines...),
		Extraction: lyricssource.Extraction{
			Version:    lyricssource.LyricsVersion{Kind: "vocaloid", Label: "Vocaloid Version"},
			Performers: []lyricssource.Performer{}, RubyGeneratorVersion: "kagome-ipadic-v1",
			Lines: structuredLines,
		},
	}
	attachTestCandidateToFixed(&fixed, candidate)
	fixed.FixedIdentities = []model.LyricsSourceFixedIdentity{{
		Provider: candidate.Provider, Origin: candidate.Origin,
		PageID: candidate.PageID, RevisionID: candidate.RevisionID, SHA1: candidate.SHA1,
		Title: candidate.Title, CanonicalURL: candidate.CanonicalURL,
		FetchedAt: fixed.FetchedAt.Format(time.RFC3339Nano), Categories: append([]string{}, candidate.Categories...),
		Section: candidate.Section, RenditionKey: candidate.RenditionKey, VersionReason: candidate.VersionReason,
		IndexEvidenceRefs: append([]model.LyricsSourceIndexEvidenceRef{}, candidate.IndexEvidenceRefs...),
	}}
	return fixed
}

func enqueuePipelineFetchJob(
	t *testing.T,
	s *Store,
	musicID int,
	catalogFingerprint string,
	candidate lyricssource.Candidate,
) model.LyricsDiscoveryJob {
	t.Helper()
	job, _, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Provider: candidate.Provider,
		Kind:     model.LyricsDiscoveryJobFetchRevision,
		Target: model.LyricsDiscoveryJobTarget{
			MusicID: musicID, PageID: candidate.PageID, RevisionID: candidate.RevisionID,
			ExpectedSHA1: candidate.SHA1, CatalogFingerprint: catalogFingerprint,
			PolicyVersion:  model.LyricsMatchingPolicyVersion,
			FixedCandidate: legacyLyricsDiscoveryCandidateIdentity(&candidate),
		},
		FixedCandidate: &candidate,
		MaxAttempts:    3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func seedFullLyricsSourceCatalog(t *testing.T, s *Store, musicID int) CatalogMusicIdentity {
	t.Helper()
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{MusicID: musicID, JapaneseTitle: "合成試験曲",
		ProducerMetadata: "制作者", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者",
		LyricsVersion: "full", LyricsVersionKnown: true}}); err != nil {
		t.Fatal(err)
	}
	identity, err := s.CatalogMusicIdentity(musicID)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestValidateCanonicalLyricsSourceURLRequiresExactManagedIdentity(t *testing.T) {
	const revisionID = 456
	for name, test := range map[string]struct {
		title   string
		rawURL  string
		wantErr bool
	}{
		"ASCII canonical":          {title: "Song Title", rawURL: "https://vocaloid.fandom.com/wiki/Song_Title?oldid=456"},
		"Unicode canonical":        {title: "初音ミク/Song", rawURL: "https://vocaloid.fandom.com/wiki/%E5%88%9D%E9%9F%B3%E3%83%9F%E3%82%AF/Song?oldid=456"},
		"reserved title canonical": {title: "Song ?#% Title", rawURL: "https://vocaloid.fandom.com/wiki/Song_%3F%23%25_Title?oldid=456"},
		"explicit port":            {title: "Song", rawURL: "https://vocaloid.fandom.com:443/wiki/Song?oldid=456", wantErr: true},
		"non wiki path":            {title: "Song", rawURL: "https://vocaloid.fandom.com/api.php?oldid=456", wantErr: true},
		"wrong title":              {title: "Song", rawURL: "https://vocaloid.fandom.com/wiki/Other?oldid=456", wantErr: true},
		"wrong revision":           {title: "Song", rawURL: "https://vocaloid.fandom.com/wiki/Song?oldid=457", wantErr: true},
		"extra query":              {title: "Song", rawURL: "https://vocaloid.fandom.com/wiki/Song?oldid=456&diff=prev", wantErr: true},
		"userinfo":                 {title: "Song", rawURL: "https://user@vocaloid.fandom.com/wiki/Song?oldid=456", wantErr: true},
		"fragment":                 {title: "Song", rawURL: "https://vocaloid.fandom.com/wiki/Song?oldid=456#Lyrics", wantErr: true},
		"surrounding whitespace":   {title: "Song", rawURL: " https://vocaloid.fandom.com/wiki/Song?oldid=456 ", wantErr: true},
		"raw Unicode path":         {title: "初音ミク/Song", rawURL: "https://vocaloid.fandom.com/wiki/初音ミク/Song?oldid=456", wantErr: true},
		"lowercase escapes":        {title: "初音ミク/Song", rawURL: "https://vocaloid.fandom.com/wiki/%e5%88%9d%e9%9f%b3%e3%83%9f%e3%82%af/Song?oldid=456", wantErr: true},
		"encoded slash":            {title: "初音ミク/Song", rawURL: "https://vocaloid.fandom.com/wiki/%E5%88%9D%E9%9F%B3%E3%83%9F%E3%82%AF%2FSong?oldid=456", wantErr: true},
		"encoded oldid":            {title: "Song", rawURL: "https://vocaloid.fandom.com/wiki/Song?oldid=%34%35%36", wantErr: true},
		"space encoding":           {title: "Song Title", rawURL: "https://vocaloid.fandom.com/wiki/Song%20Title?oldid=456", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateCanonicalLyricsSourceURL(test.rawURL, test.title, revisionID)
			if (err != nil) != test.wantErr {
				t.Fatalf("validate URL error=%v wantErr=%t", err, test.wantErr)
			}
		})
	}
}

func TestCandidateAndArtifactValidatorsShareCanonicalURLPolicy(t *testing.T) {
	const validURL = "https://vocaloid.fandom.com/wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=34"
	candidate := model.LyricsSourceCandidateIdentity{PageID: 12, RevisionID: 34, SHA1: strings.Repeat("a", 40),
		Title: "合成試験曲", CanonicalURL: validURL, Categories: []string{"Songs"}}
	if err := validateLyricsSourceCandidateIdentity(candidate); err != nil {
		t.Fatalf("valid candidate URL rejected: %v", err)
	}
	fixed := lyricssource.FixedRevision{
		Provider: model.LyricsSourceProviderVocaloidFandom, Origin: model.LyricsSourceOriginVocaloidFandom,
		PageID: 12, RevisionID: 34, SHA1: strings.Repeat("a", 40), PageTitle: "合成試験曲",
		CanonicalURL: validURL, FetchedAt: time.Now().UTC(), Wikitext: []byte("== Lyrics ==\n合成歌詞"),
	}
	if _, err := canonicalLyricsSourceArtifact(model.LyricsDiscoveryJob{ID: 1}, fixed, time.Now().UTC()); err != nil {
		t.Fatalf("valid artifact URL rejected: %v", err)
	}
	for name, rawURL := range map[string]string{
		"port":                 "https://vocaloid.fandom.com:443/wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=34",
		"path":                 "https://vocaloid.fandom.com/not-wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=34",
		"noncanonical Unicode": "https://vocaloid.fandom.com/wiki/合成試験曲?oldid=34",
	} {
		t.Run(name, func(t *testing.T) {
			candidate.CanonicalURL = rawURL
			if err := validateLyricsSourceCandidateIdentity(candidate); err == nil {
				t.Fatal("candidate validator accepted noncanonical URL")
			}
			fixed.CanonicalURL = rawURL
			if _, err := canonicalLyricsSourceArtifact(model.LyricsDiscoveryJob{ID: 1}, fixed, time.Now().UTC()); lyricsdiscovery.Classify(err).Code != lyricsdiscovery.CodeInvalidResult {
				t.Fatalf("artifact validator error=%v", err)
			}
		})
	}
}

func TestCompleteLyricsDiscoveryResultRejectsUnsafeCandidateIdentityAtomically(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	discover, _, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Kind: model.LyricsDiscoveryJobDiscover, Target: model.LyricsDiscoveryJobTarget{MusicID: 10,
			CatalogFingerprint: identity.CatalogFingerprint, PolicyVersion: LyricsDiscoveryShadowPolicyVersion}, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "discover-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobDiscover, Now: time.Now().UTC()})
	if err != nil || leased.ID != discover.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	candidate := pipelineProviderCandidate()
	candidate.CanonicalURL = "https://evil.example/wiki/Song?oldid=34"
	result := lyricsdiscovery.Result{Outcome: lyricsdiscovery.OutcomeCandidatesFound, CandidateCount: 1,
		Artifact: mustTestCandidateArtifact(t, []lyricssource.Candidate{candidate})}
	if err := s.CompleteLyricsDiscoveryResult(context.Background(), CompleteLyricsDiscoveryResultParams{JobID: leased.ID,
		LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), ShadowResult: result,
		Candidates: []lyricssource.Candidate{candidate}, IndexEvidence: candidate.IndexEvidence}); err == nil {
		t.Fatal("unsafe candidate identity completed discovery")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM lyrics_discovery_shadow_results`,
		`SELECT COUNT(*) FROM lyrics_discovery_jobs WHERE kind='fetch_revision'`,
		`SELECT COUNT(*) FROM lyrics_source_index_evidence`,
		`SELECT COUNT(*) FROM lyrics_discovery_result_index_evidence`,
		`SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence`,
	} {
		var count int
		if err := database.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("atomic rollback query=%q count=%d err=%v", query, count, err)
		}
	}
}

func TestCompleteLyricsDiscoveryResultAtomicallyEnqueuesExactFetch(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	discover, _, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Kind: model.LyricsDiscoveryJobDiscover, Target: model.LyricsDiscoveryJobTarget{MusicID: 10,
			CatalogFingerprint: identity.CatalogFingerprint, PolicyVersion: LyricsDiscoveryShadowPolicyVersion}, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "discover-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobDiscover, Now: time.Now().UTC()})
	if err != nil || leased.ID != discover.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	candidate := pipelineProviderCandidate()
	result := lyricsdiscovery.Result{Outcome: lyricsdiscovery.OutcomeCandidatesFound, CandidateCount: 1,
		Artifact: mustTestCandidateArtifact(t, []lyricssource.Candidate{candidate})}
	if err := s.CompleteLyricsDiscoveryResult(context.Background(), CompleteLyricsDiscoveryResultParams{JobID: leased.ID,
		LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), ShadowResult: result,
		Candidates: []lyricssource.Candidate{candidate}, IndexEvidence: candidate.IndexEvidence}); err != nil {
		t.Fatal(err)
	}
	var state, expectedSHA, fixedCandidateJSON string
	var pageID, revisionID int
	if err := database.QueryRow(`SELECT state FROM lyrics_discovery_jobs WHERE job_id=?`, leased.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatalf("discover state=%q err=%v", state, err)
	}
	if err := database.QueryRow(`SELECT page_id, revision_id, expected_sha1, fixed_candidate_json FROM lyrics_discovery_jobs WHERE kind='fetch_revision'`).
		Scan(&pageID, &revisionID, &expectedSHA, &fixedCandidateJSON); err != nil || pageID != 12 || revisionID != 34 || expectedSHA != candidate.SHA1 {
		t.Fatalf("fetch identity=%d/%d/%q err=%v", pageID, revisionID, expectedSHA, err)
	}
	persisted, err := decodeLyricsDiscoveryFixedCandidate(fixedCandidateJSON)
	wantCandidate := stripLyricsCandidateIndexEvidence(candidate)
	if err != nil || persisted == nil || fmt.Sprint(*persisted) != fmt.Sprint(wantCandidate) {
		t.Fatalf("fixed candidate=%+v want=%+v json=%q err=%v", persisted, wantCandidate, fixedCandidateJSON, err)
	}
	var evidenceParents, resultLinks, jobLinks int
	if err := database.QueryRow(`SELECT
		(SELECT COUNT(*) FROM lyrics_source_index_evidence),
		(SELECT COUNT(*) FROM lyrics_discovery_result_index_evidence),
		(SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence)`).Scan(
		&evidenceParents, &resultLinks, &jobLinks,
	); err != nil || evidenceParents != 1 || resultLinks != 1 || jobLinks != 1 {
		t.Fatalf("exact evidence parents=%d resultLinks=%d jobLinks=%d err=%v",
			evidenceParents, resultLinks, jobLinks, err)
	}
}

func TestLyricsSourceFetchClaimCarriesCompleteFixedCandidateIdentity(t *testing.T) {
	s, _ := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	adapter, err := NewLyricsSourceFetchAdapter(s)
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := adapter.ClaimFetch(context.Background(), lyricsdiscovery.ClaimRequest{
		WorkerID: "fetch-worker", LeaseDuration: time.Minute, Now: time.Now().UTC(),
	})
	if err != nil || !ok {
		t.Fatalf("claim job=%+v ok=%t err=%v", job, ok, err)
	}
	if fmt.Sprint(job.FixedCandidate) != fmt.Sprint(candidate) {
		t.Fatalf("claimed fixed candidate=%+v want=%+v", job.FixedCandidate, candidate)
	}
}

func TestSearchCompletionAndFetchClaimPreserveProviderCandidateAndPerformerPolicy(t *testing.T) {
	for _, provider := range []model.LyricsSourceProvider{
		model.LyricsSourceProviderVocaloidFandom,
		model.LyricsSourceProviderMoegirl,
	} {
		t.Run(string(provider), func(t *testing.T) {
			s, database := openLyricsSourcePipelineStore(t)
			if err := s.UpsertMusicCatalog([]MusicCatalogRecord{{
				MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者",
				LyricsVersion: "full", LyricsVersionKnown: true,
				Vocals: []model.CatalogVocalSignal{{VocalID: 1, VocalType: "sekai"}},
			}}); err != nil {
				t.Fatal(err)
			}
			identity, err := s.CatalogMusicIdentity(10)
			if err != nil {
				t.Fatal(err)
			}
			discover, _, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
				Kind: model.LyricsDiscoveryJobDiscover,
				Target: model.LyricsDiscoveryJobTarget{MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint,
					PolicyVersion: LyricsDiscoveryShadowPolicyVersion},
				MaxAttempts: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
				Owner: "discover-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobDiscover, Now: time.Now().UTC(),
			})
			if err != nil || leased.ID != discover.ID {
				t.Fatalf("claim=%+v err=%v", leased, err)
			}
			candidate := providerAwarePipelineCandidate(provider)
			result := lyricsdiscovery.Result{Outcome: lyricsdiscovery.OutcomeCandidatesFound, CandidateCount: 1,
				Artifact: mustTestCandidateArtifact(t, []lyricssource.Candidate{candidate})}
			if err := s.CompleteLyricsDiscoveryResult(context.Background(), CompleteLyricsDiscoveryResultParams{
				JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version,
				CompletedAt: time.Now().UTC(), ShadowResult: result, Candidates: []lyricssource.Candidate{candidate},
				IndexEvidence: candidate.IndexEvidence,
			}); err != nil {
				t.Fatal(err)
			}
			var storedProvider, status, candidateJSON string
			if err := database.QueryRow(`SELECT provider,provenance_status,fixed_candidate_json FROM lyrics_discovery_jobs WHERE kind='fetch_revision'`).
				Scan(&storedProvider, &status, &candidateJSON); err != nil {
				t.Fatal(err)
			}
			if storedProvider != string(provider) || status != "candidate_complete" {
				t.Fatalf("provider=%q status=%q", storedProvider, status)
			}
			persisted, err := decodeLyricsDiscoveryFixedCandidate(candidateJSON)
			wantPersisted := stripLyricsCandidateIndexEvidence(candidate)
			if err != nil || persisted == nil || fmt.Sprint(*persisted) != fmt.Sprint(wantPersisted) {
				t.Fatalf("persisted candidate=%+v want=%+v err=%v", persisted, wantPersisted, err)
			}
			adapter, err := NewLyricsSourceFetchAdapter(s)
			if err != nil {
				t.Fatal(err)
			}
			job, ok, err := adapter.ClaimFetch(context.Background(), lyricsdiscovery.ClaimRequest{
				WorkerID: "fetch-worker", LeaseDuration: time.Minute, Now: time.Now().UTC(),
			})
			if err != nil || !ok {
				t.Fatalf("fetch claim=%+v ok=%t err=%v", job, ok, err)
			}
			if fmt.Sprint(job.FixedCandidate) != fmt.Sprint(candidate) ||
				job.PerformerSegmentationPolicy != lyricssource.PerformerSegmentationSekaiEligible {
				t.Fatalf("claimed candidate=%+v policy=%q", job.FixedCandidate, job.PerformerSegmentationPolicy)
			}
		})
	}
}

func TestProviderAwareFetchCompletionPersistsExactProviderArtifact(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := providerAwarePipelineCandidate(model.LyricsSourceProviderMoegirl)
	enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	adapter, err := NewLyricsSourceFetchAdapter(s)
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := adapter.ClaimFetch(context.Background(), lyricsdiscovery.ClaimRequest{
		WorkerID: "fetch-worker", LeaseDuration: time.Minute, Now: time.Now().UTC(),
	})
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%t err=%v", job, ok, err)
	}
	fetchedAt := time.Now().UTC().Truncate(time.Millisecond)
	fixed := pipelineFixedRevision(candidate, fetchedAt, []byte("== 歌词 ==\n合成歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
	if err := adapter.CompleteFetch(context.Background(), lyricsdiscovery.FetchCompletion{
		JobID: job.ID, LeaseToken: job.LeaseToken, WorkerID: "fetch-worker", CompletedAt: time.Now().UTC(),
		Result: lyricsdiscovery.FetchResult{Fixed: fixed, Evidence: []model.LyricsSourceEvidence{{
			RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact provider revision",
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	var artifactProvider, origin, provenanceStatus, analysisProvider, state string
	if err := database.QueryRow(`SELECT a.provider,a.source_origin,a.provenance_status,n.provider,j.state
		FROM lyrics_source_artifacts a
		JOIN lyrics_source_analyses n ON n.artifact_id=a.artifact_id
		JOIN lyrics_discovery_job_outputs o ON o.analysis_id=n.analysis_id
		JOIN lyrics_discovery_jobs j ON j.job_id=o.job_id`).
		Scan(&artifactProvider, &origin, &provenanceStatus, &analysisProvider, &state); err != nil {
		t.Fatal(err)
	}
	if artifactProvider != string(candidate.Provider) || analysisProvider != string(candidate.Provider) ||
		origin != candidate.Origin || provenanceStatus != "complete" || state != string(model.LyricsDiscoveryJobSucceeded) {
		t.Fatalf("artifact provider=%q origin=%q status=%q analysis=%q job=%q", artifactProvider, origin, provenanceStatus, analysisProvider, state)
	}
	var renditions int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_renditions WHERE provider='moegirl' AND rendition_key=?`,
		candidate.RenditionKey).Scan(&renditions); err != nil || renditions != 1 {
		t.Fatalf("provider renditions=%d err=%v", renditions, err)
	}
}

func TestProviderAwareCandidateReviewSelectionRecoversExactFetchClaim(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	discover, _, err := s.EnqueueLyricsDiscoveryJob(context.Background(), EnqueueLyricsDiscoveryJobParams{
		Kind: model.LyricsDiscoveryJobDiscover,
		Target: model.LyricsDiscoveryJobTarget{MusicID: 10, CatalogFingerprint: identity.CatalogFingerprint,
			PolicyVersion: LyricsDiscoveryShadowPolicyVersion},
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{
		Owner: "discover-worker", Duration: time.Minute, Kind: model.LyricsDiscoveryJobDiscover, Now: time.Now().UTC(),
	})
	if err != nil || leased.ID != discover.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fandom := providerAwarePipelineCandidate(model.LyricsSourceProviderVocaloidFandom)
	moegirl := testRevisionCandidate(
		model.LyricsSourceProviderMoegirl,
		13,
		35,
		"合成試験曲",
		[]string{"Songs"},
		"歌词",
		"full-sekai",
		model.LyricsSourceVersionReasonUntaggedFullOnly,
		[]byte("provider-aware pipeline index evidence: moegirl review selection"),
	)
	candidates := []lyricssource.Candidate{fandom, moegirl}
	artifact := mustTestCandidateArtifact(t, candidates)
	compactCandidates, indexEvidence, err := decodeLyricsDiscoveryArtifact(artifact, len(candidates))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteLyricsDiscoveryResult(context.Background(), CompleteLyricsDiscoveryResultParams{
		JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(),
		ShadowResult: lyricsdiscovery.Result{Outcome: lyricsdiscovery.OutcomeAmbiguous, CandidateCount: len(candidates), Artifact: artifact},
		Candidates:   compactCandidates, IndexEvidence: indexEvidence,
	}); err != nil {
		t.Fatal(err)
	}
	var reviewID, version int64
	if err := database.QueryRow(`SELECT review_id,version FROM lyrics_source_review_items WHERE kind='candidate_selection'`).Scan(&reviewID, &version); err != nil {
		t.Fatal(err)
	}
	selected := legacyLyricsDiscoveryCandidateIdentity(&moegirl)
	if _, _, err := s.SelectLyricsSourceCandidate(context.Background(), LyricsSourceCandidateSelectionParams{
		ReviewID: reviewID, CandidateIdentity: selected, ExpectedVersion: version, Actor: "reviewer",
		IdempotencyKey: "provider-selection-0001", DecidedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewLyricsSourceFetchAdapter(s)
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := adapter.ClaimFetch(context.Background(), lyricsdiscovery.ClaimRequest{
		WorkerID: "fetch-worker", LeaseDuration: time.Minute, Now: time.Now().UTC(),
	})
	if err != nil || !ok || fmt.Sprint(job.FixedCandidate) != fmt.Sprint(moegirl) {
		t.Fatalf("selected fetch claim=%+v ok=%t err=%v", job, ok, err)
	}
}

func TestCompleteLyricsFetchRejectsCatalogGroupingDriftAtomically(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	presenceJSON, _ := json.Marshal(model.CatalogEvidencePresence{Lyricist: true, Composer: true, Arranger: true, LyricsVersion: true})
	if _, err := database.Exec(`INSERT INTO catalog_music
		(music_id,title_ja,producer_metadata,lyricist,composer,arranger,lyrics_version,lyrics_evidence_presence_json,
		 vocal_signals_json,lyrics_catalog_fingerprint,lyrics_catalog_policy_version)
		VALUES (11,'合成試験曲','制作者','制作者','制作者','制作者','full',?,'[]',?,?)`, string(presenceJSON),
		strings.Repeat("b", 64), model.LyricsCatalogIdentityPolicyVersion); err != nil {
		t.Fatal(err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n合成歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
	_, err = s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if lyricsdiscovery.Classify(err).Code != lyricsdiscovery.CodeSourceDrift {
		t.Fatalf("grouping drift err=%v", err)
	}
	for _, table := range []string{"lyrics_source_artifacts", "lyrics_source_analyses", "lyrics_source_associations", "lyrics_source_review_items", "lyrics_discovery_job_outputs"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func TestCompleteLyricsFetchRejectsCompleteCandidateMetadataDriftAtomically(t *testing.T) {
	for name, mutate := range map[string]func(*lyricssource.FixedRevision){
		"page title": func(fixed *lyricssource.FixedRevision) { fixed.PageTitle = "改名された曲" },
		"canonical URL": func(fixed *lyricssource.FixedRevision) {
			fixed.CanonicalURL = "https://vocaloid.fandom.com/wiki/Other?oldid=34"
		},
		"categories": func(fixed *lyricssource.FixedRevision) { fixed.Categories = []string{"Lyrics", "Songs"} },
		"restriction category": func(fixed *lyricssource.FixedRevision) {
			fixed.Categories = []string{"Lyrics may not be reprinted", "Songs"}
		},
	} {
		t.Run(name, func(t *testing.T) {
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
			fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n合成歌詞"),
				[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
			mutate(&fixed)
			_, err = s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{
				JobID: leased.ID, LeaseOwner: leased.LeaseOwner, ExpectedVersion: leased.Version,
				CompletedAt: time.Now().UTC(), Fixed: fixed,
				Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}},
			})
			if lyricsdiscovery.Classify(err).Code != lyricsdiscovery.CodeSourceDrift {
				t.Fatalf("metadata drift error=%v", err)
			}
			for _, table := range []string{"lyrics_source_artifacts", "lyrics_source_analyses", "lyrics_source_review_items", "lyrics_discovery_job_outputs"} {
				var count int
				if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestCompleteLyricsFetchAcceptsElectedGameSizeAnchorAndAssociatesWork(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	if err := s.UpsertMusicCatalog([]MusicCatalogRecord{
		{MusicID: 11, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者", LyricsVersion: "game_size", LyricsVersionKnown: true},
		{MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者", LyricsVersion: "game_size", LyricsVersionKnown: true},
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
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n完全な外部歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "完全な外部歌詞"}})
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if err != nil || review.MusicID != 10 || review.Kind != LyricsSourceReviewKindArtifact {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	rows, err := database.Query(`SELECT music_id, kind FROM lyrics_source_associations ORDER BY music_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int]string{}
	for rows.Next() {
		var musicID int
		var kind string
		if err := rows.Scan(&musicID, &kind); err != nil {
			t.Fatal(err)
		}
		got[musicID] = kind
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[10] != "full_target" || got[11] != "game_size_evidence" {
		t.Fatalf("associations=%v", got)
	}
	var anchors, authoritative int
	if err := database.QueryRow(`SELECT COUNT(*) FROM lyrics_source_associations WHERE kind='full_target'`).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics`).Scan(&authoritative); err != nil {
		t.Fatal(err)
	}
	if anchors != 1 || authoritative != 0 {
		t.Fatalf("anchors=%d authoritative lyrics=%d", anchors, authoritative)
	}
}

func TestCompleteLyricsFetchAssociationsRequireExactlyOneAnchor(t *testing.T) {
	for name, kinds := range map[string][]string{
		"no anchor":        {"game_size_evidence", "game_size_evidence"},
		"multiple anchors": {"full_target", "full_target"},
	} {
		t.Run(name, func(t *testing.T) {
			s, database := openLyricsSourcePipelineStore(t)
			if err := s.UpsertMusicCatalog([]MusicCatalogRecord{
				{MusicID: 10, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者", LyricsVersion: "game_size", LyricsVersionKnown: true},
				{MusicID: 11, JapaneseTitle: "合成試験曲", Lyricist: "制作者", Composer: "制作者", Arranger: "制作者", LyricsVersion: "game_size", LyricsVersionKnown: true},
			}); err != nil {
				t.Fatal(err)
			}
			anchor, err := s.CatalogMusicIdentity(10)
			if err != nil {
				t.Fatal(err)
			}
			sibling, err := s.CatalogMusicIdentity(11)
			if err != nil {
				t.Fatal(err)
			}
			candidate := pipelineProviderCandidate()
			job := enqueuePipelineFetchJob(t, s, 10, anchor.CatalogFingerprint, candidate)
			leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
				Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
			if err != nil || leased.ID != job.ID {
				t.Fatalf("claim=%+v err=%v", leased, err)
			}
			associations := []model.LyricsSourceAssociation{
				{MusicID: 10, CatalogFingerprint: anchor.CatalogFingerprint, Kind: kinds[0]},
				{MusicID: 11, CatalogFingerprint: sibling.CatalogFingerprint, Kind: kinds[1]},
			}
			fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n完全な外部歌詞"),
				[]lyricssource.ExtractedLine{{Japanese: "完全な外部歌詞"}})
			_, err = s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
				ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Associations: associations, Fixed: fixed,
				Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
			if err == nil || !strings.Contains(err.Error(), "exactly one full target") {
				t.Fatalf("association error=%v", err)
			}
			for _, table := range []string{"lyrics_source_artifacts", "lyrics_source_analyses", "lyrics_source_associations", "lyrics_source_review_items", "lyrics_discovery_job_outputs", "song_lyrics"} {
				var count int
				if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestCompleteLyricsFetchIsPrivateAtomicAndReviewable(t *testing.T) {
	s, database := openLyricsSourcePipelineStore(t)
	identity := seedFullLyricsSourceCatalog(t, s, 10)
	candidate := pipelineProviderCandidate()
	job := enqueuePipelineFetchJob(t, s, 10, identity.CatalogFingerprint, candidate)
	leased, err := s.ClaimLyricsDiscoveryJob(context.Background(), LyricsDiscoveryJobLease{Owner: "fetch-worker", Duration: time.Minute,
		Kind: model.LyricsDiscoveryJobFetchRevision, Now: time.Now().UTC()})
	if err != nil || leased.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", leased, err)
	}
	fixed := pipelineFixedRevision(candidate, time.Now().UTC(), []byte("== Lyrics ==\n合成歌詞"),
		[]lyricssource.ExtractedLine{{Japanese: "合成歌詞"}})
	review, err := s.CompleteLyricsFetch(context.Background(), CompleteLyricsFetchParams{JobID: leased.ID, LeaseOwner: leased.LeaseOwner,
		ExpectedVersion: leased.Version, CompletedAt: time.Now().UTC(), Fixed: fixed,
		Evidence: []model.LyricsSourceEvidence{{RuleID: "fixed", Gate: "identity", Outcome: "passed", Summary: "exact revision"}}})
	if err != nil || review.Kind != LyricsSourceReviewKindArtifact || review.State != LyricsSourceReviewStatePending {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	for _, table := range []string{"lyrics_source_artifacts", "lyrics_source_analyses", "lyrics_source_associations", "lyrics_source_review_items", "lyrics_discovery_job_outputs"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var authoritative int
	if err := database.QueryRow(`SELECT COUNT(*) FROM song_lyrics`).Scan(&authoritative); err != nil || authoritative != 0 {
		t.Fatalf("authoritative lyrics count=%d err=%v", authoritative, err)
	}
	detail, err := s.GetLyricsSourceReviewDetail(context.Background(), review.ReviewID)
	if err != nil || detail.Analysis == nil || len(detail.Analysis.ExtractedLines) != 1 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if detail.Analysis.ExtractedLines[0].Segments[0].PerformerIDs == nil || detail.Analysis.ExtractedLines[0].TrailingPerformerIDs == nil {
		t.Fatalf("empty structured performer arrays must serialize as []: %+v", detail.Analysis.ExtractedLines[0])
	}
}
