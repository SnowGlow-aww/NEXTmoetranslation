package model

import (
	"encoding/json"

	"strings"
	"testing"
)

func TestDecodeLyricsSourceDocumentKeepsSekaiSegmentationCompatibleWithPrivateMarker(t *testing.T) {
	document := validLyricsSourceDocument()
	document.PrivateReview = &LyricsSourcePrivateReview{
		PerformerSegmentationEvidence: LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured,
	}
	if err := ValidateLyricsSourceDocument(document); err != nil {
		t.Fatalf("SEKAI document with optional private marker: %v", err)
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLyricsSourceDocument(body); err != nil {
		t.Fatalf("closed SEKAI document with optional private marker: %v", err)
	}
}

func TestDecodeLyricsSourceDocumentAcceptsAuthoritativeVirtualSingerSegmentation(t *testing.T) {
	document := validAuthoritativeVirtualSingerLyricsSourceDocument()
	if err := ValidateLyricsSourceDocument(document); err != nil {
		t.Fatalf("valid authoritative VIRTUAL SINGER document: %v", err)
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLyricsSourceDocument(body)
	if err != nil {
		t.Fatalf("closed authoritative VIRTUAL SINGER document: %v", err)
	}
	if decoded.Full.Version.Label != "VIRTUAL SINGER Version" || len(decoded.Full.Performers) != 2 ||
		decoded.Provenance.PerformerSegmentation == nil || decoded.PrivateReview == nil {
		t.Fatalf("authoritative VIRTUAL SINGER structure was not preserved: %+v", decoded)
	}
	if err := ValidateLyricsSourceFull(document.Full); err == nil {
		t.Fatal("standalone Full validation bypassed the required private-review marker")
	}
}

func TestValidateLyricsSourceFullWithAuthoritativeVocaloidSegmentationRequiresExplicitAuthority(t *testing.T) {
	document := validAuthoritativeVirtualSingerLyricsSourceDocument()
	if err := ValidateLyricsSourceFull(document.Full); err == nil {
		t.Fatal("ordinary standalone Full validation accepted authoritative Vocaloid segmentation without evidence")
	}
	if err := ValidateLyricsSourceFullWithAuthoritativeVocaloidSegmentation(document.Full); err != nil {
		t.Fatalf("explicitly authorized Vocaloid segmentation was rejected: %v", err)
	}

	nonVocaloid := document.Full
	nonVocaloid.Version = LyricsSourceVersion{Kind: "sekai", Label: "SEKAI Version"}
	if err := ValidateLyricsSourceFullWithAuthoritativeVocaloidSegmentation(nonVocaloid); err == nil {
		t.Fatal("authoritative Vocaloid validator accepted a non-Vocaloid Full")
	}
}

func TestAuthoritativeVirtualSingerSegmentationFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*LyricsSourceDocument)
		wantErr string
	}{
		{
			name: "missing marker",
			mutate: func(document *LyricsSourceDocument) {
				document.PrivateReview = nil
			},
			wantErr: "vocaloid-only Full must not contain performer metadata",
		},
		{
			name: "wrong marker",
			mutate: func(document *LyricsSourceDocument) {
				document.PrivateReview.PerformerSegmentationEvidence = "structured"
			},
			wantErr: "invalid performerSegmentationEvidence marker",
		},
		{
			name: "missing performer provenance",
			mutate: func(document *LyricsSourceDocument) {
				document.Provenance.PerformerSegmentation = nil
			},
			wantErr: "provenance performerSegmentation is required",
		},
		{
			name: "performer provenance logical rendition mismatch",
			mutate: func(document *LyricsSourceDocument) {
				document.FixedIdentities[1].CompositionRenditionKey = "other-vocaloid"
			},
			wantErr: "must resolve to the Full logical rendition",
		},
		{
			name: "inline performer annotation",
			mutate: func(document *LyricsSourceDocument) {
				line := &document.Full.Lines[0]
				line.Text = "@1" + line.Text
				line.Segments[0].Text = "@1" + line.Segments[0].Text
				line.Segments[0].Ruby = []LyricsSourceRubySpan{{Text: line.Segments[0].Text}}
			},
			wantErr: "must not contain performer or color annotations",
		},
		{
			name: "inline color annotation",
			mutate: func(document *LyricsSourceDocument) {
				line := &document.Full.Lines[0]
				line.Text = "#33CCBB" + line.Text
				line.Segments[0].Text = "#33CCBB" + line.Segments[0].Text
				line.Segments[0].Ruby = []LyricsSourceRubySpan{{Text: line.Segments[0].Text}}
			},
			wantErr: "must not contain performer or color annotations",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := validAuthoritativeVirtualSingerLyricsSourceDocument()
			test.mutate(&document)
			validationErr := ValidateLyricsSourceDocument(document)
			if validationErr == nil {
				t.Fatal("invalid authoritative VIRTUAL SINGER document was accepted")
			}
			if test.wantErr != "" && !strings.Contains(validationErr.Error(), test.wantErr) {
				t.Fatalf("validation error = %v, want containing %q", validationErr, test.wantErr)
			}
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeLyricsSourceDocument(body); err == nil {
				t.Fatal("closed decoder accepted invalid authoritative VIRTUAL SINGER document")
			}
		})
	}
}

func TestAuthoritativeVirtualSingerSegmentationPreservesSourceShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*LyricsSourceDocument)
	}{
		{
			name: "partially attributed source segments",
			mutate: func(document *LyricsSourceDocument) {
				document.Full.Lines[0].Segments[1].PerformerIDs = []string{}
			},
		},
		{
			name: "one source-attributed segment",
			mutate: func(document *LyricsSourceDocument) {
				document.Full.Lines = []LyricsSourceFullLine{{
					ID: "full-000001", Text: "初音歌う",
					Segments: []LyricsSourceSegment{{
						Text: "初音歌う", PerformerIDs: []string{"miku"},
						Ruby: []LyricsSourceRubySpan{{Text: "初音", Reading: "はつね"}, {Text: "歌", Reading: "うた"}, {Text: "う"}},
					}},
					TrailingPerformerIDs: []string{},
				}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := validAuthoritativeVirtualSingerLyricsSourceDocument()
			test.mutate(&document)
			if err := ValidateLyricsSourceDocument(document); err != nil {
				t.Fatalf("source-proven segmentation shape was rejected: %v", err)
			}
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeLyricsSourceDocument(body); err != nil {
				t.Fatalf("closed decoder rejected source-proven segmentation shape: %v", err)
			}
		})
	}
}

func TestDecodeLyricsSourceDocumentClosesPrivateReviewAndStillForbidsRomanization(t *testing.T) {
	body, err := json.Marshal(validAuthoritativeVirtualSingerLyricsSourceDocument())
	if err != nil {
		t.Fatal(err)
	}
	marker := `"performerSegmentationEvidence":"authoritative_complete_structured"`
	for name, replacement := range map[string]string{
		"unknown private review field": marker + `,"futureField":true`,
		"private review romanization":  marker + `,"romanizedSegmentation":"Hatsune utau"`,
		"duplicate private review marker": marker +
			`,"performerSegmentationEvidence":"authoritative_complete_structured"`,
	} {
		t.Run(name, func(t *testing.T) {
			mutated := []byte(strings.Replace(string(body), marker, replacement, 1))
			if _, err := DecodeLyricsSourceDocument(mutated); err == nil {
				t.Fatal("privateReview extension was accepted")
			} else if name == "private review romanization" && !strings.Contains(err.Error(), "romanization field") {
				t.Fatalf("romanization error = %v", err)
			}
		})
	}
}

func TestValidateLyricsSourceFullAcceptsUnsegmentedLineWithoutPerformers(t *testing.T) {
	full := LyricsSourceFull{
		Version:    LyricsSourceVersion{Kind: "original", Label: "Original lyrics"},
		Performers: []LyricsSourcePerformer{},
		Lines: []LyricsSourceFullLine{{
			ID: "full-000001", Text: "歌う",
			Segments: []LyricsSourceSegment{{
				Text: "歌う", PerformerIDs: []string{}, Ruby: []LyricsSourceRubySpan{{Text: "歌う"}},
			}},
			TrailingPerformerIDs: []string{},
		}},
	}
	if err := ValidateLyricsSourceFull(full); err != nil {
		t.Fatalf("unsegmented Full without performers: %v", err)
	}
}

func TestLyricsSourcePerformerSegmentationShapeRequiresStrictProvenance(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*LyricsSourceDocument)
		wantErr string
	}{
		{
			name:   "trivial exact segment without provenance",
			mutate: func(*LyricsSourceDocument) {},
		},
		{
			name: "multi-segment split without provenance",
			mutate: func(document *LyricsSourceDocument) {
				splitFirstLyricsSourceLineWithoutPerformerIDs(document)
			},
			wantErr: "provenance performerSegmentation is required",
		},
		{
			name: "multi-segment split with provenance",
			mutate: func(document *LyricsSourceDocument) {
				splitFirstLyricsSourceLineWithoutPerformerIDs(document)
				reference := document.Provenance.FullText
				document.Provenance.PerformerSegmentation = &reference
			},
		},
		{
			name: "trivial exact segment with extraneous provenance",
			mutate: func(document *LyricsSourceDocument) {
				reference := document.Provenance.FullText
				document.Provenance.PerformerSegmentation = &reference
			},
			wantErr: "provenance performerSegmentation is present without component data",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validNonVocaloidUnsegmentedLyricsSourceDocument()
			test.mutate(&document)

			validationErr := ValidateLyricsSourceDocument(document)
			if test.wantErr == "" {
				if validationErr != nil {
					t.Fatalf("validator rejected document: %v", validationErr)
				}
			} else if validationErr == nil || !strings.Contains(validationErr.Error(), test.wantErr) {
				t.Fatalf("validator error = %v, want containing %q", validationErr, test.wantErr)
			}

			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			_, decodeErr := DecodeLyricsSourceDocument(body)
			if test.wantErr == "" {
				if decodeErr != nil {
					t.Fatalf("closed decoder rejected document: %v", decodeErr)
				}
			} else if decodeErr == nil || !strings.Contains(decodeErr.Error(), test.wantErr) {
				t.Fatalf("closed decoder error = %v, want containing %q", decodeErr, test.wantErr)
			}
		})
	}
}
