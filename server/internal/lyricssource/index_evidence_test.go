package lyricssource

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moesekai/server/internal/model"
)

func TestFandomSearchSurfacesExactNonRevisionEvidence(t *testing.T) {
	const content = "作者 original song Lyrics\n== Lyrics ==\n歌う"
	raw := fandomSearchEvidenceResponse(t, 12, 34, "新曲", content, []string{"Songs"})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("generator") != "search" || r.URL.Query().Get("gsrsearch") != "新曲" {
			t.Fatalf("unexpected search request: %s", r.URL.RawQuery)
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	identity := MusicIdentity{MusicID: 12, JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	candidates, err := client.Search(context.Background(), identity)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Fandom candidates=%+v err=%v", candidates, err)
	}
	candidate := candidates[0]
	if len(candidate.IndexEvidenceRefs) != 1 || len(candidate.IndexEvidence) != 1 {
		t.Fatalf("Fandom evidence transport=%+v refs=%+v", candidate.IndexEvidence, candidate.IndexEvidenceRefs)
	}
	evidence := candidate.IndexEvidence[0]
	expectedRequestURL, err := canonicalMediaWikiRequestURL(vocaloidWikiAPI, searchQueryRequestParams("新曲"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	expectedEvidenceID := fandomSearchIndexEvidenceID(
		expectedRequestURL, evidence.FetchedAt, fmt.Sprintf("%x", digest),
	)
	if evidence.EvidenceID != expectedEvidenceID ||
		evidence.Kind != IndexEvidenceKindMediaWikiSearchResponse || evidence.Provider != ProviderVocaloidFandom ||
		evidence.Origin != OriginVocaloidFandom || evidence.PageID != 0 || evidence.RevisionID != 0 ||
		evidence.MediaWikiSHA1 != "" || evidence.Title != "" || evidence.CanonicalURL != "" ||
		len(evidence.Categories) != 0 || evidence.CanonicalRequestURL != expectedRequestURL ||
		!bytes.Equal(evidence.Raw, raw) || evidence.RawSHA256 != fmt.Sprintf("%x", digest) ||
		evidence.SHA256 != evidence.RawSHA256 || candidate.IndexEvidenceRefs[0].EvidenceID != evidence.EvidenceID ||
		candidate.IndexEvidenceRefs[0].SHA256 != evidence.SHA256 {
		t.Fatalf("Fandom search evidence=%+v", evidence)
	}
	fetchedAt, err := time.Parse(time.RFC3339Nano, evidence.FetchedAt)
	if err != nil || canonicalFetchedAt(fetchedAt) != evidence.FetchedAt || ValidateCandidateIndexEvidence(candidate) != nil {
		t.Fatalf("Fandom evidence timestamp or resolution invalid: %+v err=%v", evidence, err)
	}

	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped Candidate
	if err := json.Unmarshal(encoded, &roundTripped); err != nil ||
		!bytes.Equal(roundTripped.IndexEvidence[0].Raw, raw) || ValidateCandidateIndexEvidence(roundTripped) != nil {
		t.Fatalf("private candidate evidence did not survive JSON transport: %+v err=%v", roundTripped, err)
	}

	cached, err := client.Search(context.Background(), identity)
	if err != nil || len(cached) != 1 || !indexEvidenceEqual(cached[0].IndexEvidence[0], evidence) {
		t.Fatalf("cached Fandom evidence changed: %+v err=%v", cached, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("cached Fandom evidence made %d requests, want 1", requests.Load())
	}
}

func TestFandomSearchSharesRequestScopedEvidenceAcrossCandidates(t *testing.T) {
	contents := []string{
		"作者 original song Lyrics\n== Lyrics ==\n歌う",
		"作者 original song Lyrics\n== Lyrics ==\n踊る",
	}
	pages := map[string]any{}
	for index, pageID := range []int{12, 13} {
		pages[fmt.Sprint(pageID)] = map[string]any{
			"pageid": pageID, "title": "新曲/作者",
			"categories": []map[string]string{{"title": "Category:Songs"}},
			"revisions": []map[string]any{{
				"revid": 34 + index, "sha1": sha1Hex(contents[index]),
				"slots": map[string]any{"main": map[string]string{"content": contents[index]}},
			}},
		}
	}
	raw, err := json.Marshal(map[string]any{"query": map[string]any{"pages": pages}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	candidates, err := newTestClient(server.URL).Search(context.Background(), MusicIdentity{
		MusicID: 12, JapaneseTitle: "新曲", ProducerMetadata: "作者",
	})
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	requestURL, err := canonicalMediaWikiRequestURL(vocaloidWikiAPI, searchQueryRequestParams("新曲"))
	if err != nil {
		t.Fatal(err)
	}
	wantEvidenceID := fandomSearchIndexEvidenceID(
		requestURL, candidates[0].IndexEvidence[0].FetchedAt, candidates[0].IndexEvidence[0].RawSHA256,
	)
	if candidates[0].IndexEvidenceRefs[0] != candidates[1].IndexEvidenceRefs[0] ||
		candidates[0].IndexEvidenceRefs[0].EvidenceID != wantEvidenceID ||
		!indexEvidenceEqual(candidates[0].IndexEvidence[0], candidates[1].IndexEvidence[0]) ||
		!bytes.Equal(candidates[0].IndexEvidence[0].Raw, raw) ||
		ValidateCandidatesIndexEvidence(candidates) != nil {
		t.Fatalf("request-scoped shared evidence was not preserved: %+v", candidates)
	}
}

func TestFandomSearchEvidenceIDSeparatesImmutableAcquisitions(t *testing.T) {
	requestURL, err := canonicalMediaWikiRequestURL(vocaloidWikiAPI, searchQueryRequestParams("新曲"))
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Date(2026, time.July, 31, 12, 0, 0, 123, time.UTC)
	firstRaw := fandomSearchEvidenceResponse(t, 12, 34, "新曲", "作者 original song Lyrics\n== Lyrics ==\n歌う", []string{"Songs"})
	changedRaw := fandomSearchEvidenceResponse(t, 12, 35, "新曲", "作者 original song Lyrics\n== Lyrics ==\n踊る", []string{"Songs"})

	first, err := newFandomSearchIndexEvidence(requestURL, fetchedAt, firstRaw)
	if err != nil {
		t.Fatal(err)
	}
	later, err := newFandomSearchIndexEvidence(requestURL, fetchedAt.Add(time.Nanosecond), firstRaw)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := newFandomSearchIndexEvidence(requestURL, fetchedAt, changedRaw)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := newFandomSearchIndexEvidence(requestURL, fetchedAt, firstRaw)
	if err != nil {
		t.Fatal(err)
	}

	if first.EvidenceID == later.EvidenceID {
		t.Fatal("identical request URL and bytes at different fetchedAt values reused one evidence ID")
	}
	if first.EvidenceID == changed.EvidenceID {
		t.Fatal("identical request URL and fetchedAt with different raw bytes reused one evidence ID")
	}
	if !indexEvidenceEqual(first, duplicate) {
		t.Fatalf("exact duplicate acquisition did not reuse one exact envelope: first=%+v duplicate=%+v", first, duplicate)
	}

	candidate := validFandomEvidenceCandidate(t, "作者 original song Lyrics\n== Lyrics ==\n歌う")
	if err := ValidateCandidatesIndexEvidence([]Candidate{candidate, cloneCandidateEvidence(candidate)}); err != nil {
		t.Fatalf("exact duplicate evidence could not be reused by exact references: %v", err)
	}
}

func TestFandomSearchEvidenceIDFailsClosedOnEnvelopeDrift(t *testing.T) {
	candidate := validFandomEvidenceCandidate(t, "作者 original song Lyrics\n== Lyrics ==\n歌う")
	base := candidate.IndexEvidence[0]
	otherRequestURL, err := canonicalMediaWikiRequestURL(vocaloidWikiAPI, searchQueryRequestParams("別曲"))
	if err != nil {
		t.Fatal(err)
	}
	otherRaw := fandomSearchEvidenceResponse(t, 12, 35, "別曲", "作者 original song Lyrics\n== Lyrics ==\n踊る", []string{"Songs"})
	otherDigest := sha256.Sum256(otherRaw)

	for name, mutate := range map[string]func(*IndexEvidence){
		"fetchedAt": func(evidence *IndexEvidence) {
			evidence.FetchedAt = time.Date(2026, time.July, 31, 12, 0, 1, 0, time.UTC).Format(time.RFC3339Nano)
		},
		"request URL": func(evidence *IndexEvidence) {
			evidence.CanonicalRequestURL = otherRequestURL
		},
		"raw bytes and SHA-256": func(evidence *IndexEvidence) {
			evidence.Raw = append([]byte(nil), otherRaw...)
			evidence.SHA256 = fmt.Sprintf("%x", otherDigest)
			evidence.RawSHA256 = evidence.SHA256
		},
		"provider": func(evidence *IndexEvidence) {
			evidence.Provider = ProviderMoegirl
			evidence.Origin = OriginMoegirl
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := base
			mutated.Categories = append([]string(nil), base.Categories...)
			mutated.Raw = append([]byte(nil), base.Raw...)
			mutate(&mutated)
			if err := validateIndexEvidence(mutated); err == nil {
				t.Fatal("search evidence envelope drift was accepted without a matching acquisition ID")
			}
		})
	}
}

func TestCandidateIndexEvidenceRequiresExactOnceResolution(t *testing.T) {
	candidate := validFandomEvidenceCandidate(t, "作者 original song Lyrics\n== Lyrics ==\n歌う")
	if err := ValidateCandidateIndexEvidence(candidate); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}

	for name, mutate := range map[string]func(*Candidate){
		"missing concrete evidence": func(value *Candidate) { value.IndexEvidence = nil },
		"reference digest mismatch": func(value *Candidate) { value.IndexEvidenceRefs[0].SHA256 = strings.Repeat("c", 64) },
		"raw bytes changed":         func(value *Candidate) { value.IndexEvidence[0].Raw[0] ^= 1 },
		"unreferenced extra evidence": func(value *Candidate) {
			value.IndexEvidence = append(value.IndexEvidence, value.IndexEvidence[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := cloneCandidateEvidence(candidate)
			mutate(&mutated)
			if err := ValidateCandidateIndexEvidence(mutated); err == nil {
				t.Fatal("invalid evidence transport was accepted")
			}
		})
	}

	conflicting := cloneCandidateEvidence(candidate)
	other := validFandomEvidenceCandidate(t, "作者 original song Lyrics\n== Lyrics ==\n踊る")
	other.IndexEvidence[0].EvidenceID = conflicting.IndexEvidence[0].EvidenceID
	other.IndexEvidenceRefs[0].EvidenceID = conflicting.IndexEvidenceRefs[0].EvidenceID
	conflicting.IndexEvidence = append(conflicting.IndexEvidence, other.IndexEvidence[0])
	conflicting.IndexEvidenceRefs = append(conflicting.IndexEvidenceRefs, other.IndexEvidenceRefs[0])
	if err := ValidateCandidateIndexEvidence(conflicting); err == nil {
		t.Fatal("one evidence ID with conflicting exact-byte digests was accepted")
	}
}

func TestCandidateIndexEvidenceResolverValidatesWithoutHydratingRawCopies(t *testing.T) {
	content := "作者 original song Lyrics\n== Lyrics ==\n" + strings.Repeat("a", 1<<20)
	candidate := validFandomEvidenceCandidate(t, content)
	resolver, err := NewCandidateIndexEvidenceResolver(candidate.IndexEvidence)
	if err != nil {
		t.Fatal(err)
	}
	refsOnly := cloneCandidateEvidence(candidate)
	refsOnly.IndexEvidence = nil

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for index := 0; index < 4; index++ {
		if err := resolver.ValidateCandidate(refsOnly); err != nil {
			t.Fatalf("validate refs-only candidate: %v", err)
		}
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("refs-only validation allocated %d bytes; raw evidence was likely hydrated", allocated)
	}

	hydrated, err := resolver.ResolveCandidate(refsOnly)
	if err != nil || len(hydrated.IndexEvidence) != 1 || !bytes.Equal(hydrated.IndexEvidence[0].Raw, candidate.IndexEvidence[0].Raw) {
		t.Fatalf("hydrated candidate=%+v err=%v", hydrated, err)
	}
	hydrated.IndexEvidence[0].Raw[0] ^= 1
	resolvedAgain, err := resolver.ResolveCandidate(refsOnly)
	if err != nil || !bytes.Equal(resolvedAgain.IndexEvidence[0].Raw, candidate.IndexEvidence[0].Raw) {
		t.Fatal("per-item hydration did not return a defensive raw copy")
	}
	runtime.KeepAlive(refsOnly)
}

func TestCandidateIndexEvidenceResolverValidatesResolvedEvidenceWithoutReceiptClone(t *testing.T) {
	content := "作者 original song Lyrics\n== Lyrics ==\n" + strings.Repeat("a", 1<<20)
	candidate := validFandomEvidenceCandidate(t, content)
	resolver, err := NewCandidateIndexEvidenceResolver(candidate.IndexEvidence)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for index := 0; index < 4; index++ {
		if err := resolver.ValidateResolvedCandidate(candidate); err != nil {
			t.Fatalf("validate resolved candidate: %v", err)
		}
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("resolved validation allocated %d bytes; raw evidence was likely cloned", allocated)
	}

	for name, mutate := range map[string]func(*Candidate){
		"raw drift":      func(value *Candidate) { value.IndexEvidence[0].Raw[0] ^= 1 },
		"metadata drift": func(value *Candidate) { value.IndexEvidence[0].FetchedAt = "1970-01-01T00:00:00Z" },
		"missing evidence": func(value *Candidate) {
			value.IndexEvidence = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := cloneCandidateEvidence(candidate)
			mutate(&mutated)
			if err := resolver.ValidateResolvedCandidate(mutated); err == nil {
				t.Fatal("resolved evidence drift was accepted")
			}
		})
	}
	runtime.KeepAlive(candidate)
}

func TestCandidateIndexEvidenceResolverRejectsLateInvalidEnvelopeBeforeRawClone(t *testing.T) {
	raw := bytes.Repeat([]byte("a"), MaxIndexEvidenceRawBytes)
	content := string(raw)
	page := wikiPage{
		pageID: 12, revisionID: 34, title: "Fixed index", content: content,
		sha1: sha1Hex(content), categories: []string{},
		fetchedAt: time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}
	valid, err := newMediaWikiRevisionIndexEvidence(
		ProviderVocaloidFandom, "fetch:vocaloid-fandom:12", page, raw,
	)
	if err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.EvidenceID = "fetch:vocaloid-fandom:999:" + strings.Repeat("0", 64)
	input := []IndexEvidence{valid, invalid}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = NewCandidateIndexEvidenceResolver(input)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "acquisition identity is invalid") {
		t.Fatalf("late invalid envelope error=%v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("late invalid envelope allocated %d bytes; earlier raw evidence was cloned before rejection", allocated)
	}
	runtime.KeepAlive(input)
	runtime.KeepAlive(raw)
}

func TestSekaipediaCandidateEvidenceBindsSongRevisionAndSeparateFixedList(t *testing.T) {
	fixture := newSekaipediaFixtureServer(t)
	defer fixture.Close()
	provider := fixture.Provider(t)
	identity := rokiSekaipediaIdentity()
	candidates, err := provider.Search(context.Background(), identity)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Sekaipedia candidates=%+v err=%v", candidates, err)
	}
	candidate := candidates[0]
	if len(candidate.IndexEvidence) != 2 ||
		!isFixedSekaipediaAuthorityEvidence(candidate.IndexEvidence[0], historicalSekaipediaAuthority()) ||
		candidate.IndexEvidence[1].EvidenceID != sekaipediaRevisionAcquisitionEvidenceID(
			sekaipediaSongEvidenceID(candidate.PageID, candidate.RevisionID),
			candidate.IndexEvidence[1].FetchedAt,
			candidate.IndexEvidence[1].RawSHA256,
		) || ValidateCandidateIndexEvidence(candidate) != nil {
		t.Fatalf("Sekaipedia candidate evidence=%+v refs=%+v", candidate.IndexEvidence, candidate.IndexEvidenceRefs)
	}

	authority := historicalSekaipediaAuthority()
	historicalEnvelope := authority
	historicalEnvelope.RawSHA256 = strings.Repeat("f", 64)
	if err := ValidateCandidateIndexEvidenceAgainstSekaipediaAuthority(candidate, historicalEnvelope); err != nil {
		t.Fatalf("semantic authority rejected page-info-only response-envelope drift: %v", err)
	}
	for name, mutate := range map[string]func(*FixedIndex){
		"page ID":            func(value *FixedIndex) { value.PageID++ },
		"revision ID":        func(value *FixedIndex) { value.RevisionID++ },
		"revision timestamp": func(value *FixedIndex) { value.RevisionTimestamp = "2026-07-27T16:29:14Z" },
		"MediaWiki SHA1":     func(value *FixedIndex) { value.SHA1 = strings.Repeat("0", 40) },
		"content SHA256":     func(value *FixedIndex) { value.ContentSHA256 = strings.Repeat("0", 64) },
		"title":              func(value *FixedIndex) { value.Title = "Different fixed index" },
	} {
		t.Run("caller authority "+name, func(t *testing.T) {
			mutated := authority
			mutate(&mutated)
			if err := ValidateCandidateIndexEvidenceAgainstSekaipediaAuthority(candidate, mutated); err == nil {
				t.Fatal("candidate drift from semantic caller authority was accepted")
			}
		})
	}

	refsOnlyFields := cloneCandidateEvidence(candidate)
	refsOnlyFields.RawSHA256 = ""
	refsOnlyFields.FetchedAt = ""
	if err := ValidateCandidateIndexEvidence(refsOnlyFields); err != nil || !validSekaipediaCandidate(refsOnlyFields) {
		t.Fatalf("refs-only public candidate did not resolve through exact private song evidence: %v", err)
	}
	fixed, err := provider.FetchFixedCandidateRevision(context.Background(), identity, refsOnlyFields)
	if err != nil || len(fixed.Wikitext) == 0 || fixed.RawSHA256 != candidate.RawSHA256 {
		t.Fatalf("refs-only candidate fixed fetch=%+v err=%v", fixed, err)
	}

	for name, mutate := range map[string]func(*Candidate){
		"song page ID":            func(value *Candidate) { value.PageID++ },
		"song revision":           func(value *Candidate) { value.RevisionID++ },
		"song revision timestamp": func(value *Candidate) { value.RevisionTimestamp = "2026-07-15T07:59:13Z" },
		"song MediaWiki SHA1":     func(value *Candidate) { value.SHA1 = strings.Repeat("0", 40) },
		"song raw SHA256":         func(value *Candidate) { value.RawSHA256 = strings.Repeat("0", 64) },
		"song fetchedAt":          func(value *Candidate) { value.FetchedAt = "2026-08-01T00:00:00Z" },
		"song categories":         func(value *Candidate) { value.Categories = []string{"Songs"} },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := cloneCandidateEvidence(candidate)
			mutate(&mutated)
			if err := ValidateCandidateIndexEvidence(mutated); err == nil {
				t.Fatal("candidate drift from exact song revision evidence was accepted")
			}
		})
	}

	for name, keep := range map[string]int{"missing fixed List": 1, "missing song revision": 0} {
		t.Run(name, func(t *testing.T) {
			mutated := cloneCandidateEvidence(candidate)
			mutated.IndexEvidenceRefs = []model.LyricsSourceIndexEvidenceRef{mutated.IndexEvidenceRefs[keep]}
			mutated.IndexEvidence = []IndexEvidence{mutated.IndexEvidence[keep]}
			if err := ValidateCandidateIndexEvidence(mutated); err == nil {
				t.Fatal("incomplete Sekaipedia evidence pair was accepted")
			}
		})
	}
}

func TestSekaipediaRevisionEvidenceIDSeparatesImmutableAcquisitions(t *testing.T) {
	raw := readSekaipediaFixture(t, "testdata/sekaipedia-list-335193.json")
	page, err := parsePageResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	page.fetchedAt = time.Date(2026, time.July, 31, 12, 0, 0, 123, time.UTC)
	first, err := newMediaWikiRevisionIndexEvidence(ProviderSekaipedia, historicalSekaipediaAuthorityEvidenceID(), page, raw)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := newMediaWikiRevisionIndexEvidence(ProviderSekaipedia, historicalSekaipediaAuthorityEvidenceID(), page, raw)
	if err != nil {
		t.Fatal(err)
	}
	page.fetchedAt = page.fetchedAt.Add(time.Nanosecond)
	later, err := newMediaWikiRevisionIndexEvidence(ProviderSekaipedia, historicalSekaipediaAuthorityEvidenceID(), page, raw)
	if err != nil {
		t.Fatal(err)
	}
	if first.EvidenceID == later.EvidenceID {
		t.Fatal("identical Sekaipedia revision bytes at different fetchedAt values reused one evidence ID")
	}
	if !indexEvidenceEqual(first, duplicate) {
		t.Fatalf("exact duplicate Sekaipedia acquisition changed: first=%+v duplicate=%+v", first, duplicate)
	}

	drifted := first
	drifted.FetchedAt = later.FetchedAt
	if err := validateIndexEvidence(drifted); err == nil {
		t.Fatal("Sekaipedia fetchedAt drift was accepted without a matching acquisition ID")
	}
}

func TestFandomAndMoegirlRevisionEvidenceIDsSeparateImmutableAcquisitions(t *testing.T) {
	const rawText = "fixed revision evidence"
	raw := []byte(rawText)
	fetchedAt := time.Date(2026, time.July, 31, 12, 0, 0, 123, time.UTC)
	for _, test := range []struct {
		provider model.LyricsSourceProvider
		baseID   string
	}{
		{provider: ProviderVocaloidFandom, baseID: "fetch:vocaloid-fandom:12"},
		{provider: ProviderMoegirl, baseID: "search:moegirl:12"},
	} {
		t.Run(string(test.provider), func(t *testing.T) {
			page := wikiPage{
				pageID: 12, revisionID: 34, title: "Fixed index", content: rawText,
				sha1: sha1Hex(rawText), categories: []string{}, fetchedAt: fetchedAt,
			}
			first, err := newMediaWikiRevisionIndexEvidence(test.provider, test.baseID, page, raw)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := newMediaWikiRevisionIndexEvidence(test.provider, test.baseID, page, raw)
			if err != nil {
				t.Fatal(err)
			}
			changedRaw := []byte("changed fixed revision evidence")
			changedPage := page
			changedPage.content = string(changedRaw)
			changedPage.sha1 = sha1Hex(changedPage.content)
			changed, err := newMediaWikiRevisionIndexEvidence(test.provider, test.baseID, changedPage, changedRaw)
			if err != nil {
				t.Fatal(err)
			}
			page.fetchedAt = page.fetchedAt.Add(time.Nanosecond)
			later, err := newMediaWikiRevisionIndexEvidence(test.provider, test.baseID, page, raw)
			if err != nil {
				t.Fatal(err)
			}
			if first.EvidenceID == later.EvidenceID {
				t.Fatal("identical revision bytes at different fetchedAt values reused one evidence ID")
			}
			if first.EvidenceID == changed.EvidenceID {
				t.Fatal("different revision bytes at one fetchedAt reused one evidence ID")
			}
			if !indexEvidenceEqual(first, duplicate) {
				t.Fatalf("exact duplicate revision acquisition changed: first=%+v duplicate=%+v", first, duplicate)
			}
			for name, evidenceID := range map[string]string{
				"fetchedAt": first.EvidenceID,
				"base ID": mediaWikiRevisionAcquisitionEvidenceID(
					test.provider, "wrong:revision:base", first.FetchedAt, first.RawSHA256,
				),
				"provider": mediaWikiRevisionAcquisitionEvidenceID(
					map[model.LyricsSourceProvider]model.LyricsSourceProvider{
						ProviderVocaloidFandom: ProviderMoegirl,
						ProviderMoegirl:        ProviderVocaloidFandom,
					}[test.provider],
					test.baseID,
					first.FetchedAt,
					first.RawSHA256,
				),
			} {
				t.Run(name, func(t *testing.T) {
					drifted := first
					drifted.EvidenceID = evidenceID
					if name == "fetchedAt" {
						drifted.FetchedAt = later.FetchedAt
					}
					if err := validateIndexEvidence(drifted); err == nil {
						t.Fatal("revision acquisition identity drift was accepted")
					}
				})
			}
		})
	}
}

func TestLegacyPersistedRevisionEvidenceCanonicalizationIsBackupOnlyAndFailClosed(t *testing.T) {
	const rawText = "historical fixed revision evidence"
	raw := []byte(rawText)
	fetchedAt := time.Date(2026, time.July, 31, 12, 0, 0, 123, time.UTC)
	for _, test := range []struct {
		provider model.LyricsSourceProvider
		baseID   string
	}{
		{provider: ProviderVocaloidFandom, baseID: "fetch:vocaloid-fandom:12"},
		{provider: ProviderMoegirl, baseID: "search:moegirl:12"},
	} {
		t.Run(string(test.provider), func(t *testing.T) {
			page := wikiPage{
				pageID: 12, revisionID: 34, title: "Historical revision", content: rawText,
				sha1: sha1Hex(rawText), categories: []string{}, fetchedAt: fetchedAt,
			}
			current, err := newMediaWikiRevisionIndexEvidence(test.provider, test.baseID, page, raw)
			if err != nil {
				t.Fatal(err)
			}
			legacy := current
			legacy.EvidenceID = test.baseID
			if err := ValidateIndexEvidenceEnvelope(legacy); err == nil {
				t.Fatal("live strict evidence validation accepted a legacy unsuffixed ID")
			}
			normalized, err := CanonicalizeLegacyPersistedMediaWikiRevisionEvidence(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if !indexEvidenceEqual(normalized, current) || legacy.EvidenceID != test.baseID {
				t.Fatalf("legacy validation normalization drifted: legacy=%+v normalized=%+v current=%+v", legacy, normalized, current)
			}

			for name, mutate := range map[string]func(*IndexEvidence){
				"wrong legacy base": func(value *IndexEvidence) { value.EvidenceID += ":wrong" },
				"raw drift":         func(value *IndexEvidence) { value.Raw[0] ^= 1 },
				"wrong kind":        func(value *IndexEvidence) { value.Kind = IndexEvidenceKindMediaWikiSearchResponse },
				"unsupported provider": func(value *IndexEvidence) {
					value.Provider = ProviderSekaipedia
					value.Origin = OriginSekaipedia
				},
			} {
				t.Run(name, func(t *testing.T) {
					mutated := legacy
					mutated.Categories = append([]string{}, legacy.Categories...)
					mutated.Raw = append([]byte(nil), legacy.Raw...)
					mutate(&mutated)
					if _, err := CanonicalizeLegacyPersistedMediaWikiRevisionEvidence(mutated); err == nil {
						t.Fatal("invalid historical evidence was accepted by backup compatibility validation")
					}
				})
			}
		})
	}
}

func TestIndexEvidenceRejectsRawBytesBeyondBound(t *testing.T) {
	raw := bytes.Repeat([]byte("a"), MaxIndexEvidenceRawBytes+1)
	page := wikiPage{
		pageID: 1, revisionID: 2, title: "Oversized index", content: string(raw),
		sha1: fmt.Sprintf("%x", sha1.Sum(raw)), categories: []string{},
		fetchedAt: time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}
	if _, err := newMediaWikiRevisionIndexEvidence(ProviderMoegirl, "search:moegirl:1", page, raw); err == nil {
		t.Fatal("oversized exact evidence bytes were accepted")
	}
}

func validFandomEvidenceCandidate(t *testing.T, content string) Candidate {
	t.Helper()
	raw := fandomSearchEvidenceResponse(t, 12, 34, "新曲", content, []string{"Songs"})
	requestURL, err := canonicalMediaWikiRequestURL(vocaloidWikiAPI, searchQueryRequestParams("新曲"))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := newFandomSearchIndexEvidence(
		requestURL, time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC), raw,
	)
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{
		Provider: ProviderVocaloidFandom, Origin: OriginVocaloidFandom,
		PageID: 12, RevisionID: 34, SHA1: sha1Hex(content), Title: "新曲",
		CanonicalURL: canonicalRevisionURL(ProviderVocaloidFandom, "新曲", 34), Categories: []string{"Songs"},
		Section: "Lyrics", RenditionKey: "full-original",
		VersionReason:     model.LyricsSourceVersionReasonUntaggedFullOnly,
		IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{EvidenceID: evidence.EvidenceID, SHA256: evidence.SHA256}},
		IndexEvidence:     []IndexEvidence{evidence},
	}
}

func fandomSearchEvidenceResponse(
	t *testing.T,
	pageID, revisionID int,
	title, content string,
	categories []string,
) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": map[string]any{"pages": map[string]any{
		fmt.Sprint(pageID): map[string]any{
			"pageid": pageID, "title": title, "categories": mediaWikiCategories(categories),
			"revisions": []any{map[string]any{
				"revid": revisionID, "sha1": sha1Hex(content),
				"slots": map[string]any{"main": map[string]any{"content": content}},
			}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func cloneCandidateEvidence(candidate Candidate) Candidate {
	result := candidate
	result.Categories = append([]string(nil), candidate.Categories...)
	result.IndexEvidenceRefs = cloneIndexEvidenceRefs(candidate.IndexEvidenceRefs)
	result.IndexEvidence = cloneIndexEvidence(candidate.IndexEvidence)
	return result
}
