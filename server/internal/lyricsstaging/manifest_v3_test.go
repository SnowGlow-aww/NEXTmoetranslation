package lyricsstaging

import (
	"errors"
	"strings"
	"testing"

	"moesekai/server/internal/model"
)

func TestDecodeManifestV2RequiresRebuildWithoutOverwritingBytes(t *testing.T) {
	legacy := []byte(`{"schemaVersion":2,"preflight":{},"catalogReference":[],"items":[],"batchSha256":""}`)
	before := append([]byte(nil), legacy...)
	_, err := DecodeManifest(legacy)
	if !errors.Is(err, ErrManifestRebuildRequired) || !strings.Contains(err.Error(), "schema v2") {
		t.Fatalf("legacy manifest error=%v", err)
	}
	if string(legacy) != string(before) {
		t.Fatal("legacy manifest bytes changed while reporting rebuild")
	}
}

func TestBuildDraftConsumesExactProviderDocumentReasonAndProjection(t *testing.T) {
	report, catalogIdentity, fixed := validPreflightAndFixed(t)
	legacy, err := BuildDraft(report.UniqueComplete[0], catalogIdentity, fixed)
	if err != nil {
		t.Fatal(err)
	}
	projection := &model.LyricsSourceGameProjection{LineIDs: []string{legacy.Document.Full.Lines[0].ID}}
	legacy.Document.ReasonCode = model.LyricsSourceVersionReasonUntaggedUncutIdentity
	report.UniqueComplete[0].CompositionReason = legacy.Document.ReasonCode
	legacy.Document.GameProjection = projection
	component := model.LyricsSourceComponentRef{RenditionKey: legacy.Document.Provenance.FullText.RenditionKey}
	legacy.Document.Provenance.GameProjection = &component
	fixed.Provider = legacy.Document.FixedIdentities[0].Provider
	fixed.Origin = legacy.Document.FixedIdentities[0].Origin
	fixed.Section = legacy.Document.FixedIdentities[0].Section
	fixed.RenditionKey = legacy.Document.FixedIdentities[0].RenditionKey
	fixed.VersionReason = report.UniqueComplete[0].Candidate.VersionReason
	fixed.IndexEvidenceRefs = legacy.Document.FixedIdentities[0].IndexEvidenceRefs
	fixed.FixedIdentities = append([]model.LyricsSourceFixedIdentity{}, legacy.Document.FixedIdentities...)
	fixed.Document = &legacy.Document
	draft, err := BuildDraft(report.UniqueComplete[0], catalogIdentity, fixed)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Document.ReasonCode != model.LyricsSourceVersionReasonUntaggedUncutIdentity ||
		draft.Document.GameProjection == nil || draft.Document.Provenance.GameProjection == nil {
		t.Fatalf("exact fixed document was not preserved: %+v", draft.Document)
	}
}

func TestManifestV3SupportsMultipleProviderArtifactsAndComponentContributions(t *testing.T) {
	draft := validSemanticDraft(t)
	moegirl := model.LyricsSourceFixedIdentity{
		Provider: model.LyricsSourceProviderMoegirl, Origin: model.LyricsSourceOriginMoegirl,
		PageID: 99, RevisionID: 100, SHA1: strings.Repeat("b", 40), Title: "合成試験曲/版本资料",
		CanonicalURL: "https://moegirl.icu/wiki/%E5%90%88%E6%88%90%E8%A9%A6%E9%A8%93%E6%9B%B2?oldid=100",
		FetchedAt:    "2026-07-30T12:35:00Z", Categories: []string{}, Section: "版本资料",
		RenditionKey: "version-evidence", IndexEvidenceRefs: []model.LyricsSourceIndexEvidenceRef{{
			EvidenceID: "moegirl/index/99/100", SHA256: strings.Repeat("c", 64),
		}},
	}
	artifact := Artifact{Identity: moegirl, RawWikitextByteCount: 17, RawWikitextSHA256: strings.Repeat("d", 64)}
	var err error
	artifact.ArtifactSHA256, err = stagedArtifactDigest(artifact)
	if err != nil {
		t.Fatal(err)
	}
	draft.Artifacts = append(draft.Artifacts, artifact)
	draft.Document.FixedIdentities = append(draft.Document.FixedIdentities, moegirl)
	draft.Document.Provenance.VersionEvidence = model.LyricsSourceComponentRef{RenditionKey: moegirl.RenditionKey}
	draft.DocumentSHA256, err = lyricsSourceDocumentDigest(draft.Document)
	if err != nil {
		t.Fatal(err)
	}
	draft.DraftSHA256, err = draftDigest(draft)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDraft(draft); err != nil {
		t.Fatalf("multi-provider draft: %v", err)
	}
}

func TestManifestV3PreservesSubsecondFullTextFetchedAt(t *testing.T) {
	draft := validSemanticDraft(t)
	draft.Source.FetchedAt = "2026-07-30T12:34:57.123Z"
	draft.Artifacts[0].Identity.FetchedAt = draft.Source.FetchedAt
	draft.Document.FixedIdentities[0].FetchedAt = draft.Source.FetchedAt
	draft.Artifacts[0].ArtifactSHA256, _ = stagedArtifactDigest(draft.Artifacts[0])
	draft.DocumentSHA256, _ = lyricsSourceDocumentDigest(draft.Document)
	draft.DraftSHA256, _ = draftDigest(draft)
	if err := ValidateDraft(draft); err != nil {
		t.Fatalf("subsecond full-text fetchedAt: %v", err)
	}
	if draft.Document.FixedIdentities[0].FetchedAt != "2026-07-30T12:34:57.123Z" {
		t.Fatalf("subsecond full-text fetchedAt changed to %q", draft.Document.FixedIdentities[0].FetchedAt)
	}
}
