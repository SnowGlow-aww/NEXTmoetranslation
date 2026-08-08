package lyricsprovideroutcome

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"moesekai/server/internal/model"
)

func TestClosedProviderOutcomeContract(t *testing.T) {
	ref := model.LyricsSourceIndexEvidenceRef{
		EvidenceID: "authority:sekaipedia:list-of-songs:123456:" + strings.Repeat("a", 64),
		SHA256:     strings.Repeat("b", 64),
	}
	outcome, err := New(
		model.LyricsSourceProviderSekaipedia,
		StatusCandidate,
		[]int{7},
		Diagnostic{
			Provider: model.LyricsSourceProviderSekaipedia,
			Phase:    PhaseFinalize, ReasonCode: ReasonCandidate,
			Counts:          Counts{Acquisitions: 2, Targets: 1, Evaluated: 1, Candidates: 1},
			AcquisitionRefs: []model.LyricsSourceIndexEvidenceRef{ref, ref},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Candidates) != 1 || outcome.Candidates[0] != 7 || len(outcome.Diagnostic.AcquisitionRefs) != 1 || outcome.Retryable() {
		t.Fatalf("candidate outcome = %+v", outcome)
	}

	transport, err := New[int](
		model.LyricsSourceProviderMoegirl,
		StatusTransportError,
		nil,
		Diagnostic{
			Provider: model.LyricsSourceProviderMoegirl,
			Phase:    PhaseAcquireTarget, ReasonCode: ReasonCanceled,
			Counts: Counts{Acquisitions: 1, TransportErrors: 1},
		},
	)
	if err != nil || !transport.Retryable() {
		t.Fatalf("transport outcome=%+v err=%v", transport, err)
	}
}

func TestProviderOutcomeAcceptsFutureSekaipediaAuthorityEvidenceGrammar(t *testing.T) {
	future := model.LyricsSourceIndexEvidenceRef{
		EvidenceID: "authority:sekaipedia:list-of-songs:987654321:" + strings.Repeat("a", 64),
		SHA256:     strings.Repeat("b", 64),
	}
	outcome, err := New[int](
		model.LyricsSourceProviderSekaipedia,
		StatusNoMatch,
		nil,
		Diagnostic{
			Provider: model.LyricsSourceProviderSekaipedia,
			Phase:    PhaseResolveTargets, ReasonCode: ReasonNoMatch,
			Counts:          Counts{Acquisitions: 1, NoMatch: 1},
			AcquisitionRefs: []model.LyricsSourceIndexEvidenceRef{future},
		},
	)
	if err != nil || len(outcome.Diagnostic.AcquisitionRefs) != 1 ||
		outcome.Diagnostic.AcquisitionRefs[0] != future {
		t.Fatalf("future Sekaipedia authority outcome=%+v err=%v", outcome, err)
	}
}

func TestProviderOutcomeStatusesAreClosed(t *testing.T) {
	statuses := []Status{
		StatusCandidate, StatusNoMatch, StatusUnsupported,
		StatusStale, StatusTransportError, StatusAmbiguous,
	}
	want := []string{"candidate", "no_match", "unsupported", "stale", "transport_error", "ambiguous"}
	for index, status := range statuses {
		if string(status) != want[index] || !validStatus(status) {
			t.Fatalf("status %d = %q", index, status)
		}
	}
	if validStatus("other") {
		t.Fatal("open provider status was accepted")
	}
}

func TestProviderOutcomeRejectsUnboundedOrOpenDiagnostics(t *testing.T) {
	valid := Diagnostic{
		Provider: model.LyricsSourceProviderSekaipedia,
		Phase:    PhaseFinalize, ReasonCode: ReasonCandidate,
		Counts: Counts{Candidates: 1},
	}
	for name, mutate := range map[string]func(*Diagnostic){
		"open phase":      func(value *Diagnostic) { value.Phase = "download_secret_page" },
		"open reason":     func(value *Diagnostic) { value.ReasonCode = "parser said source lyrics" },
		"negative count":  func(value *Diagnostic) { value.Counts.Targets = -1 },
		"oversized count": func(value *Diagnostic) { value.Counts.Targets = MaxDiagnosticCount + 1 },
		"oversized references": func(value *Diagnostic) {
			value.AcquisitionRefs = make([]model.LyricsSourceIndexEvidenceRef, MaxAcquisitionRefs+1)
		},
		"content reference": func(value *Diagnostic) {
			value.AcquisitionRefs = []model.LyricsSourceIndexEvidenceRef{{
				EvidenceID: "lyrics:secret-title:romanization", SHA256: strings.Repeat("a", 64),
			}}
		},
		"cross-provider reference": func(value *Diagnostic) {
			value.AcquisitionRefs = []model.LyricsSourceIndexEvidenceRef{{
				EvidenceID: "search:moegirl:1:" + strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64),
			}}
		},
		"unversioned authority reference": func(value *Diagnostic) {
			value.AcquisitionRefs = []model.LyricsSourceIndexEvidenceRef{{
				EvidenceID: "authority:sekaipedia:list-of-songs:" + strings.Repeat("a", 64),
				SHA256:     strings.Repeat("b", 64),
			}}
		},
		"zero authority revision": func(value *Diagnostic) {
			value.AcquisitionRefs = []model.LyricsSourceIndexEvidenceRef{{
				EvidenceID: "authority:sekaipedia:list-of-songs:0:" + strings.Repeat("a", 64),
				SHA256:     strings.Repeat("b", 64),
			}}
		},
		"content-shaped authority reference": func(value *Diagnostic) {
			value.AcquisitionRefs = []model.LyricsSourceIndexEvidenceRef{{
				EvidenceID: "authority:sekaipedia:lyrics:987654321:" + strings.Repeat("a", 64),
				SHA256:     strings.Repeat("b", 64),
			}}
		},
		"foreign authority reference": func(value *Diagnostic) {
			value.AcquisitionRefs = []model.LyricsSourceIndexEvidenceRef{{
				EvidenceID: "authority:moegirl:list-of-songs:987654321:" + strings.Repeat("a", 64),
				SHA256:     strings.Repeat("b", 64),
			}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			diagnostic := valid
			mutate(&diagnostic)
			if _, err := New(model.LyricsSourceProviderSekaipedia, StatusCandidate, []int{1}, diagnostic); err == nil {
				t.Fatal("invalid diagnostic was accepted")
			}
		})
	}
}

func TestProviderOutcomeRequiresStatusSupportingCounts(t *testing.T) {
	for _, test := range []struct {
		name   string
		status Status
		reason ReasonCode
		counts Counts
	}{
		{name: "no match", status: StatusNoMatch, reason: ReasonNoMatch},
		{name: "unsupported", status: StatusUnsupported, reason: ReasonUnsupportedFormat},
		{name: "stale", status: StatusStale, reason: ReasonRevisionChanged},
		{name: "transport", status: StatusTransportError, reason: ReasonTransport},
		{name: "ambiguous", status: StatusAmbiguous, reason: ReasonAmbiguousMatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New[int](
				model.LyricsSourceProviderMoegirl,
				test.status,
				nil,
				Diagnostic{
					Provider:   model.LyricsSourceProviderMoegirl,
					Phase:      PhaseFinalize,
					ReasonCode: test.reason,
					Counts:     test.counts,
				},
			); err == nil {
				t.Fatal("outcome without a supporting status count was accepted")
			}
		})
	}
}

func TestProviderOutcomeMaximumDiagnosticRemainsBoundedAndOpaque(t *testing.T) {
	refs := make([]model.LyricsSourceIndexEvidenceRef, MaxAcquisitionRefs)
	for index := range refs {
		refs[index] = model.LyricsSourceIndexEvidenceRef{
			EvidenceID: fmt.Sprintf("search:moegirl:%d:%s", index+1, strings.Repeat("a", 64)),
			SHA256:     strings.Repeat("b", 64),
		}
	}
	outcome, err := New[int](
		model.LyricsSourceProviderMoegirl,
		StatusUnsupported,
		nil,
		Diagnostic{
			Provider: model.LyricsSourceProviderMoegirl,
			Phase:    PhaseParseLyrics, ReasonCode: ReasonUnsupportedFormat,
			Counts:          Counts{Acquisitions: MaxDiagnosticCount, Unsupported: 1},
			AcquisitionRefs: refs,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(outcome.Diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 16*1024 {
		t.Fatalf("maximum diagnostic grew to %d bytes", len(body))
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{
		"secret-title", "secret-url", "secret-lyrics", "secret-romanization", "secret-wikitext", "secret-raw",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("maximum diagnostic contains forbidden content marker %q: %s", forbidden, body)
		}
	}
}

func TestProviderOutcomeDiagnosticJSONIsContentFreeAndBounded(t *testing.T) {
	outcome, err := New[int](
		model.LyricsSourceProviderVocaloidFandom,
		StatusNoMatch,
		nil,
		Diagnostic{
			Provider: model.LyricsSourceProviderVocaloidFandom,
			Phase:    PhaseMatchIdentity, ReasonCode: ReasonIdentityMismatch,
			Counts: Counts{Targets: 4, Evaluated: 4, NoMatch: 4},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(outcome.Diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 1024 {
		t.Fatalf("content-free diagnostic grew to %d bytes: %s", len(body), body)
	}
	for _, forbiddenField := range []string{
		`"title":`, `"url":`, `"lyrics":`, `"raw":`, `"wikitext":`, `"pageid":`, `"revisionid":`,
		`"romaji":`, `"romanized":`, `"romanization":`, `"parser":`,
	} {
		if strings.Contains(strings.ToLower(string(body)), forbiddenField) {
			t.Fatalf("diagnostic contains forbidden field %q: %s", forbiddenField, body)
		}
	}
}
