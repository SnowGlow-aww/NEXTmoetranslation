package lyricssource

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func TestRecoverSekaipediaProjectionReparsesExactRevisionEvidence(t *testing.T) {
	raw := sekaipediaFixturePageResponse(t, "Roki")
	page, err := parsePageResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256([]byte(page.content))
	expected := FixedIndex{
		PageID: page.pageID, RevisionID: page.revisionID,
		RevisionTimestamp: canonicalFetchedAt(page.revisionTimestamp), SHA1: page.sha1,
		ContentSHA256: hex.EncodeToString(contentDigest[:]), Title: page.title,
	}
	projection, err := RecoverSekaipediaProjection(raw, expected, PerformerSegmentationSekaiEligible)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Section == "" || projection.RenditionKey == "" || projection.ReasonCode == "" ||
		len(projection.Full.Lines) == 0 || len(projection.FixedJapaneseWikitext) == 0 {
		t.Fatalf("incomplete projection: %+v", projection)
	}
	if got := SekaipediaFixedJapaneseWikitext(projection.FullToStructuredLinesForTest()); string(got) != string(projection.FixedJapaneseWikitext) {
		t.Fatal("fixed Japanese bytes are not reproducible from the projection")
	}

	drifted := expected
	drifted.Title += " changed"
	if _, err := RecoverSekaipediaProjection(raw, drifted, PerformerSegmentationSekaiEligible); !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("title drift error=%v", err)
	}
}

func (projection SekaipediaRecoveryProjection) FullToStructuredLinesForTest() []StructuredLine {
	lines := make([]StructuredLine, len(projection.Full.Lines))
	for index, line := range projection.Full.Lines {
		lines[index] = StructuredLine{Japanese: line.Text, StanzaBreakBefore: line.StanzaBreakBefore}
	}
	return lines
}
