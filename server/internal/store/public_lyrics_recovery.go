package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"moesekai/server/internal/model"
)

// RecoveryPublicLyricsCandidate is a read-only, exact-batch Public v2
// projection. Building it never creates song_lyrics_publications rows and never
// changes the externally served files. Details exist only for states that own
// authoritative text: complete Full and explicit Game-only.
type RecoveryPublicLyricsCandidate struct {
	BatchSHA256 string
	RootSHA256  string
	Index       PublicLyricsIndexDocument
	Details     map[int]PublicLyricsDetailDocument
}

func (s *Store) RecoveryPublicLyrics(batchSHA256 string) (RecoveryPublicLyricsCandidate, error) {
	return s.RecoveryPublicLyricsContext(context.Background(), batchSHA256)
}

func (s *Store) RecoveryPublicLyricsContext(ctx context.Context, batchSHA256 string) (RecoveryPublicLyricsCandidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !isCanonicalContentBackupSHA256(batchSHA256) {
		return RecoveryPublicLyricsCandidate{}, errors.New("recovery Public candidate requires an exact lowercase batch SHA-256")
	}

	// ExportLyricsContentContext reads one SQLite snapshot and performs the full
	// closed recovery graph validation, including exact evidence links. Public
	// projection therefore consumes validated immutable records rather than
	// re-reading mutable rows piecemeal.
	content, err := s.ExportLyricsContentContext(ctx)
	if err != nil {
		return RecoveryPublicLyricsCandidate{}, err
	}
	return buildRecoveryPublicLyricsCandidate(content, batchSHA256)
}

func buildRecoveryPublicLyricsCandidate(content LyricsContentExport, batchSHA256 string) (RecoveryPublicLyricsCandidate, error) {
	var batch *LyricsRecoveryBatchBackupRecord
	for index := range content.RecoveryBatches {
		if content.RecoveryBatches[index].BatchSHA256 != batchSHA256 {
			continue
		}
		if batch != nil {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery batch %s is duplicated", batchSHA256)
		}
		batch = &content.RecoveryBatches[index]
	}
	if batch == nil {
		return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery batch %s was not found", batchSHA256)
	}

	catalog := make(map[int]CatalogMusicBackupRecord, len(content.Music))
	for _, music := range content.Music {
		if _, duplicate := catalog[music.MusicID]; duplicate {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("catalog music %d is duplicated", music.MusicID)
		}
		catalog[music.MusicID] = music
	}
	if len(catalog) != batch.CatalogCount {
		return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery batch %s catalog count changed", batchSHA256)
	}

	catalogPerformers := newCatalogPerformerAliases()
	for _, performer := range content.Performers {
		addCatalogPerformerAliases(&catalogPerformers, performer.PerformerID,
			performer.NameJA, performer.NameZH, performer.NameEN)
	}
	addCanonicalLyricsSourcePerformerAliases(&catalogPerformers)
	if err := addAuditedExternalLyricsPerformerAliases(&catalogPerformers); err != nil {
		return RecoveryPublicLyricsCandidate{}, err
	}

	documents := make(map[int]LyricsDocumentBackupRecord, len(content.Documents))
	for _, document := range content.Documents {
		documents[document.MusicID] = document
	}
	lines := make(map[int][]LyricsLineBackupRecord)
	for _, line := range content.Lines {
		lines[line.MusicID] = append(lines[line.MusicID], line)
	}
	segments := make(map[int]map[string][]LyricsSegmentBackupRecord)
	for _, segment := range content.Segments {
		if segments[segment.MusicID] == nil {
			segments[segment.MusicID] = map[string][]LyricsSegmentBackupRecord{}
		}
		segments[segment.MusicID][segment.LineID] = append(segments[segment.MusicID][segment.LineID], segment)
	}

	sourceDocuments := make(map[int]LyricsSourceDocumentBackupRecord)
	for _, document := range content.SourceDocuments {
		if document.ManifestBatchSHA256 != batchSHA256 {
			continue
		}
		if _, duplicate := sourceDocuments[document.MusicID]; duplicate {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery source document %s/%d is duplicated", batchSHA256, document.MusicID)
		}
		sourceDocuments[document.MusicID] = document
	}
	availability := make(map[int]LyricsAvailabilityDocumentBackupRecord)
	for _, document := range content.AvailabilityDocuments {
		if document.BatchSHA256 != batchSHA256 {
			continue
		}
		if _, duplicate := availability[document.MusicID]; duplicate {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics availability document %s/%d is duplicated", batchSHA256, document.MusicID)
		}
		availability[document.MusicID] = document
	}
	contributions := make(map[int]map[string]string)
	for _, contribution := range content.RecoveryContributions {
		if contribution.BatchSHA256 != batchSHA256 {
			continue
		}
		if contributions[contribution.MusicID] == nil {
			contributions[contribution.MusicID] = map[string]string{}
		}
		if contributions[contribution.MusicID][contribution.Component] != "" {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery contribution %s/%d/%s is duplicated",
				batchSHA256, contribution.MusicID, contribution.Component)
		}
		contributions[contribution.MusicID][contribution.Component] = contribution.RenditionKey
	}

	items := make([]LyricsRecoveryItemBackupRecord, 0, batch.CatalogCount)
	for _, item := range content.RecoveryItems {
		if item.BatchSHA256 == batchSHA256 {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(left, right int) bool { return items[left].MusicID < items[right].MusicID })
	if len(items) != batch.CatalogCount {
		return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery batch %s does not cover its catalog", batchSHA256)
	}

	candidate := RecoveryPublicLyricsCandidate{
		BatchSHA256: batchSHA256,
		RootSHA256:  batch.RootSHA256,
		Index: PublicLyricsIndexDocument{
			Version: 2,
			Songs:   make([]PublicLyricsIndexSong, 0, len(items)),
		},
		Details: make(map[int]PublicLyricsDetailDocument),
	}
	lastMusicID := 0
	for _, item := range items {
		if item.MusicID <= lastMusicID {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery batch %s items are not strictly ordered", batchSHA256)
		}
		lastMusicID = item.MusicID
		music, exists := catalog[item.MusicID]
		if !exists {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery item %s/%d is outside the catalog", batchSHA256, item.MusicID)
		}
		state, err := publicLyricsRecoveryState(item.State)
		if err != nil {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery item %s/%d: %w", batchSHA256, item.MusicID, err)
		}
		indexItem := PublicLyricsIndexSong{
			MusicID: item.MusicID,
			State:   state,
			Title: model.LocalizedTitle{
				Japanese: music.TitleJA,
				Chinese:  music.TitleZH,
				English:  music.TitleEN,
			},
		}

		switch state {
		case PublicLyricsStateComplete:
			detail, err := buildRecoveryCompletePublicDetail(item, documents[item.MusicID], lines[item.MusicID],
				segments[item.MusicID], sourceDocuments[item.MusicID], contributions[item.MusicID], catalogPerformers)
			if err != nil {
				return RecoveryPublicLyricsCandidate{}, err
			}
			indexItem.Revision = detail.Revision
			indexItem.UpdatedAt = detail.UpdatedAt
			indexItem.AvailableVersions = append([]string{}, detail.AvailableVersions...)
			candidate.Details[item.MusicID] = detail
		case PublicLyricsStateGameOnly:
			detail, err := buildRecoveryGameOnlyPublicDetail(item, availability[item.MusicID],
				contributions[item.MusicID], catalogPerformers)
			if err != nil {
				return RecoveryPublicLyricsCandidate{}, err
			}
			indexItem.Revision = detail.Revision
			indexItem.UpdatedAt = detail.UpdatedAt
			indexItem.AvailableVersions = append([]string{}, detail.AvailableVersions...)
			candidate.Details[item.MusicID] = detail
		case PublicLyricsStateSatisfiedNoLyrics, PublicLyricsStateAmbiguous, PublicLyricsStateMissing,
			PublicLyricsStateIncomplete, PublicLyricsStateFailed:
			record, ok := availability[item.MusicID]
			if !ok {
				return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery item %s/%d has no availability document", batchSHA256, item.MusicID)
			}
			document, err := model.DecodeLyricsAvailabilityDocument([]byte(record.DocumentJSON))
			if err != nil || string(document.State) != item.State || string(document.State) != record.State {
				return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery item %s/%d availability state changed", batchSHA256, item.MusicID)
			}
			indexItem.Revision = 1
			indexItem.UpdatedAt = formatTimestamp(record.CreatedAt)
			if state == PublicLyricsStateSatisfiedNoLyrics {
				indexItem.NoLyricsReason = document.NoLyricsReason
			}
		default:
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery item %s/%d has unsupported public state %q",
				batchSHA256, item.MusicID, state)
		}
		if err := validateRecoveryPublicIndexItem(indexItem, candidate.Details[item.MusicID]); err != nil {
			return RecoveryPublicLyricsCandidate{}, fmt.Errorf("lyrics recovery item %s/%d: %w", batchSHA256, item.MusicID, err)
		}
		candidate.Index.Songs = append(candidate.Index.Songs, indexItem)
	}
	if err := validatePublicLyricsArtifactSize(candidate.Index); err != nil {
		return RecoveryPublicLyricsCandidate{}, err
	}
	return candidate, nil
}

func publicLyricsRecoveryState(state string) (PublicLyricsAvailabilityState, error) {
	switch PublicLyricsAvailabilityState(state) {
	case PublicLyricsStateComplete, PublicLyricsStateGameOnly, PublicLyricsStateSatisfiedNoLyrics,
		PublicLyricsStateAmbiguous, PublicLyricsStateMissing, PublicLyricsStateIncomplete, PublicLyricsStateFailed:
		return PublicLyricsAvailabilityState(state), nil
	default:
		return "", fmt.Errorf("unsupported recovery state %q", state)
	}
}

func validateRecoveryPublicContributions(expected, actual map[string]string) error {
	if len(actual) != len(expected) {
		return errors.New("stored recovery contributions are incomplete or contain unused components")
	}
	for component, renditionKey := range expected {
		if component == "" || renditionKey == "" || actual[component] != renditionKey {
			return errors.New("stored recovery contributions do not exactly match source provenance")
		}
	}
	return nil
}

func buildRecoveryCompletePublicDetail(
	item LyricsRecoveryItemBackupRecord,
	documentRecord LyricsDocumentBackupRecord,
	lineRecords []LyricsLineBackupRecord,
	segmentRecords map[string][]LyricsSegmentBackupRecord,
	sourceRecord LyricsSourceDocumentBackupRecord,
	contributions map[string]string,
	catalogPerformers catalogPerformerAliases,
) (PublicLyricsDetailDocument, error) {
	if documentRecord.MusicID != item.MusicID || sourceRecord.MusicID != item.MusicID ||
		sourceRecord.ManifestBatchSHA256 != item.BatchSHA256 || sourceRecord.DocumentSHA256 != item.DocumentSHA256 {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery complete item %s/%d has incomplete stored ownership",
			item.BatchSHA256, item.MusicID)
	}
	document, err := model.DecodeLyricsSourceDocument([]byte(sourceRecord.DocumentJSON))
	if err != nil {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery complete item %s/%d source document: %w",
			item.BatchSHA256, item.MusicID, err)
	}
	if err := validateRecoveryPublicContributions(publicLyricsSourceComponentRefs(document), contributions); err != nil {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery complete item %s/%d contributions: %w",
			item.BatchSHA256, item.MusicID, err)
	}
	lyrics, err := recoveryPublicEditableLyrics(documentRecord, lineRecords, segmentRecords)
	if err != nil {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery complete item %s/%d editable lyrics: %w",
			item.BatchSHA256, item.MusicID, err)
	}
	bundle := &publicLyricsSourceBundle{
		documentJSON: sourceRecord.DocumentJSON,
		documentSHA:  sourceRecord.DocumentSHA256,
		document:     document,
		contributions: func() map[string]string {
			result := make(map[string]string, len(contributions))
			for component, renditionKey := range contributions {
				result[component] = renditionKey
			}
			return result
		}(),
	}
	detail, err := buildPublicLyricsV2(lyrics, bundle, catalogPerformers)
	if err != nil {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery complete item %s/%d Public detail: %w",
			item.BatchSHA256, item.MusicID, err)
	}
	if err := validatePublicLyricsArtifactSize(detail); err != nil {
		return PublicLyricsDetailDocument{}, err
	}
	return detail, nil
}

func recoveryPublicEditableLyrics(
	document LyricsDocumentBackupRecord,
	lineRecords []LyricsLineBackupRecord,
	segmentRecords map[string][]LyricsSegmentBackupRecord,
) (model.SongLyrics, error) {
	if document.MusicID <= 0 || document.Revision <= 0 || document.UpdatedAt <= 0 {
		return model.SongLyrics{}, errors.New("stored editable lyrics header is invalid")
	}
	sort.Slice(lineRecords, func(left, right int) bool { return lineRecords[left].Position < lineRecords[right].Position })
	lines := make([]model.LyricLine, len(lineRecords))
	seenLineIDs := make(map[string]bool, len(lineRecords))
	for lineIndex, record := range lineRecords {
		if record.MusicID != document.MusicID || record.Position != lineIndex || seenLineIDs[record.LineID] {
			return model.SongLyrics{}, errors.New("stored editable lyrics lines are invalid")
		}
		seenLineIDs[record.LineID] = true
		storedSegments := append([]LyricsSegmentBackupRecord(nil), segmentRecords[record.LineID]...)
		sort.Slice(storedSegments, func(left, right int) bool { return storedSegments[left].Position < storedSegments[right].Position })
		segments := make([]model.LyricSegment, len(storedSegments))
		for segmentIndex, stored := range storedSegments {
			if stored.MusicID != document.MusicID || stored.LineID != record.LineID || stored.Position != segmentIndex {
				return model.SongLyrics{}, errors.New("stored editable lyrics segments are invalid")
			}
			var performerIDs []int
			var ruby []model.LyricRubySpan
			if err := json.Unmarshal([]byte(stored.PerformerIDsJSON), &performerIDs); err != nil {
				return model.SongLyrics{}, err
			}
			if err := json.Unmarshal([]byte(stored.RubyJSON), &ruby); err != nil {
				return model.SongLyrics{}, err
			}
			if performerIDs == nil {
				performerIDs = []int{}
			}
			if ruby == nil {
				ruby = []model.LyricRubySpan{}
			}
			segments[segmentIndex] = model.LyricSegment{
				Text: stored.Text, PerformerIDs: performerIDs, Ruby: ruby,
			}
		}
		lines[lineIndex] = model.LyricLine{
			ID: record.LineID, Order: record.Position, Japanese: record.Japanese,
			Chinese: record.Chinese, English: record.English,
			StanzaBreakBefore: record.StanzaBreakBefore == 1,
			Segments:          segments,
		}
	}
	fetchedAt := document.SourceFetchedAtRFC3339
	if fetchedAt == "" && document.SourceFetchedAt > 0 {
		fetchedAt = formatTimestamp(document.SourceFetchedAt)
	}
	return model.SongLyrics{
		MusicID: document.MusicID, Revision: document.Revision, UpdatedAt: formatTimestamp(document.UpdatedAt),
		Attribution: document.Attribution, SourceNote: document.SourceNote, SourceURL: document.SourceURL,
		LicenseNote: document.LicenseNote, SourcePageID: document.SourcePageID,
		SourceRevisionID: document.SourceRevisionID, SourceSHA1: document.SourceSHA1,
		SourceFetchedAt: fetchedAt, Lines: lines,
	}, nil
}

func buildRecoveryGameOnlyPublicDetail(
	item LyricsRecoveryItemBackupRecord,
	record LyricsAvailabilityDocumentBackupRecord,
	contributions map[string]string,
	catalogPerformers catalogPerformerAliases,
) (PublicLyricsDetailDocument, error) {
	if record.BatchSHA256 != item.BatchSHA256 || record.MusicID != item.MusicID || record.State != item.State ||
		record.DocumentSHA256 != item.AvailabilityDocumentSHA256 || record.CreatedAt <= 0 {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery Game-only item %s/%d has incomplete stored ownership",
			item.BatchSHA256, item.MusicID)
	}
	document, err := model.DecodeLyricsAvailabilityDocument([]byte(record.DocumentJSON))
	if err != nil || document.State != model.LyricsAvailabilityStateGameOnly || document.Game == nil {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery Game-only item %s/%d availability document is invalid",
			item.BatchSHA256, item.MusicID)
	}
	if err := validateRecoveryPublicContributions(recoveryAvailabilityComponentRefs(document), contributions); err != nil {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery Game-only item %s/%d contributions: %w",
			item.BatchSHA256, item.MusicID, err)
	}
	sourcePerformers, err := newPublicLyricsSourcePerformerCatalogForPerformers(document.Game.Performers, catalogPerformers)
	if err != nil {
		return PublicLyricsDetailDocument{}, err
	}
	detail := PublicLyricsDetailDocument{
		Version: 2, MusicID: item.MusicID, Revision: 1, UpdatedAt: formatTimestamp(record.CreatedAt),
		State:             PublicLyricsStateGameOnly,
		Attributions:      publicLyricsAttributionsFrom(document.FixedIdentities, contributions),
		AvailableVersions: []string{publicLyricsGameVersion},
		Lines:             make([]PublicLyricsLine, len(document.Game.Lines)),
	}
	for lineIndex, sourceLine := range document.Game.Lines {
		trailingPerformerIDs, err := publicLyricsSourceSegmentPerformerIDs(sourceLine.TrailingPerformerIDs, sourcePerformers)
		if err != nil {
			return PublicLyricsDetailDocument{}, fmt.Errorf("authoritative Game line %d trailing performers: %w", lineIndex+1, err)
		}
		line := PublicLyricsLine{
			ID: sourceLine.ID, Order: lineIndex, Japanese: sourceLine.Text,
			Chinese: "", English: "", StanzaBreakBefore: sourceLine.StanzaBreakBefore,
			Segments:             make([]model.LyricSegment, len(sourceLine.Segments)),
			TrailingPerformerIDs: append([]int{}, trailingPerformerIDs...),
		}
		for segmentIndex, sourceSegment := range sourceLine.Segments {
			performerIDs, err := publicLyricsSourceSegmentPerformerIDs(sourceSegment.PerformerIDs, sourcePerformers)
			if err != nil {
				return PublicLyricsDetailDocument{}, fmt.Errorf("authoritative Game line %d segment %d: %w", lineIndex+1, segmentIndex+1, err)
			}
			ruby := make([]model.LyricRubySpan, len(sourceSegment.Ruby))
			for rubyIndex, span := range sourceSegment.Ruby {
				ruby[rubyIndex] = model.LyricRubySpan{Text: span.Text, Reading: span.Reading}
			}
			line.Segments[segmentIndex] = model.LyricSegment{
				Text: sourceSegment.Text, PerformerIDs: performerIDs, Ruby: ruby,
			}
		}
		detail.Lines[lineIndex] = line
	}
	if err := validateRecoveryGameOnlyPublicDetail(detail, document, contributions, catalogPerformers); err != nil {
		return PublicLyricsDetailDocument{}, fmt.Errorf("lyrics recovery Game-only item %s/%d Public detail: %w",
			item.BatchSHA256, item.MusicID, err)
	}
	if err := validatePublicLyricsArtifactSize(detail); err != nil {
		return PublicLyricsDetailDocument{}, err
	}
	return detail, nil
}

func validateRecoveryGameOnlyPublicDetail(
	public PublicLyricsDetailDocument,
	document model.LyricsAvailabilityDocument,
	contributions map[string]string,
	catalogPerformers catalogPerformerAliases,
) error {
	if document.Game == nil || public.Version != 2 || public.MusicID <= 0 || public.Revision != 1 || public.UpdatedAt == "" ||
		public.State != PublicLyricsStateGameOnly || public.NoLyricsReason != "" || public.Attribution != "" ||
		!samePublicLyricsVersions(public.AvailableVersions, []string{publicLyricsGameVersion}) || public.GameProjection != nil ||
		len(public.Attributions) == 0 || len(public.Attributions) > 16 ||
		!reflect.DeepEqual(public.Attributions, publicLyricsAttributionsFrom(document.FixedIdentities, contributions)) {
		return errors.New("public Game-only lyrics header or attributions do not match the availability document")
	}
	for _, attribution := range public.Attributions {
		licenseName, licenseURL := publicLyricsProviderLicense(attribution.Provider)
		if attribution.Title == "" || attribution.RevisionID <= 0 || attribution.RevisionURL == "" ||
			licenseName == "" || attribution.LicenseName != licenseName || attribution.LicenseURL != licenseURL {
			return errors.New("public Game-only attribution is not backed by a fixed provider license policy")
		}
	}
	if document.Provenance.PerformerSegmentation != nil && document.Game.Version.Kind != "sekai" &&
		(document.PrivateReview == nil || document.PrivateReview.PerformerSegmentationEvidence !=
			model.LyricsSourcePerformerSegmentationEvidenceAuthoritativeCompleteStructured) {
		return errors.New("public Game-only non-SEKAI rendition lacks authoritative structured performer evidence")
	}
	sourcePerformers, err := newPublicLyricsSourcePerformerCatalogForPerformers(document.Game.Performers, catalogPerformers)
	if err != nil {
		return err
	}
	if len(public.Lines) == 0 || len(public.Lines) > maxLyricsLines || len(public.Lines) != len(document.Game.Lines) {
		return errors.New("public Game-only lines do not match authoritative Game")
	}
	for lineIndex, line := range public.Lines {
		sourceLine := document.Game.Lines[lineIndex]
		if line.ID != sourceLine.ID || line.Order != lineIndex || line.Japanese != sourceLine.Text || line.Chinese != "" ||
			line.English != "" || line.StanzaBreakBefore != sourceLine.StanzaBreakBefore || len(line.ID) > 128 ||
			len(line.Japanese) > maxLyricsLineTextBytes || len(line.Segments) == 0 ||
			len(line.Segments) > maxLyricsSegmentsPerLine || len(line.Segments) != len(sourceLine.Segments) ||
			len(line.TrailingPerformerIDs) > maxLyricsPerformers {
			return fmt.Errorf("public Game-only line %d is invalid or stale", lineIndex+1)
		}
		expectedTrailing, err := publicLyricsSourceSegmentPerformerIDs(sourceLine.TrailingPerformerIDs, sourcePerformers)
		if err != nil || !sameLyricsPerformerIDs(line.TrailingPerformerIDs, expectedTrailing) {
			return fmt.Errorf("public Game-only line %d trailing performer assignment is stale", lineIndex+1)
		}
		if document.Provenance.PerformerSegmentation == nil && len(expectedTrailing) != 0 {
			return fmt.Errorf("public Game-only line %d has unproven trailing performer attribution", lineIndex+1)
		}
		var lineText strings.Builder
		for segmentIndex, segment := range line.Segments {
			sourceSegment := sourceLine.Segments[segmentIndex]
			if segment.Text != sourceSegment.Text || segment.Text == "" || len(segment.Text) > maxLyricsLineTextBytes ||
				segment.PerformerIDs == nil || len(segment.PerformerIDs) > maxLyricsPerformers || segment.Ruby == nil ||
				len(segment.Ruby) == 0 || len(segment.Ruby) > maxLyricsRubyPerSegment ||
				!publicLyricsRubyMatches(segment.Ruby, sourceSegment.Ruby) {
				return fmt.Errorf("public Game-only line %d segment %d is invalid or stale", lineIndex+1, segmentIndex+1)
			}
			expectedPerformers, err := publicLyricsSourceSegmentPerformerIDs(sourceSegment.PerformerIDs, sourcePerformers)
			if err != nil || !reflect.DeepEqual(segment.PerformerIDs, expectedPerformers) {
				return fmt.Errorf("public Game-only line %d segment %d performer assignment is stale", lineIndex+1, segmentIndex+1)
			}
			if document.Provenance.PerformerSegmentation == nil && len(expectedPerformers) != 0 {
				return fmt.Errorf("public Game-only line %d segment %d has unproven performer segmentation", lineIndex+1, segmentIndex+1)
			}
			var rubyText strings.Builder
			for rubyIndex, span := range segment.Ruby {
				if span.Text == "" || len(span.Text) > maxLyricsLineTextBytes || !utf8.ValidString(span.Text) ||
					len(span.Reading) > 1024 || !publicLyricsKanaReading(span.Reading) {
					return fmt.Errorf("public Game-only line %d segment %d ruby span %d is invalid", lineIndex+1, segmentIndex+1, rubyIndex+1)
				}
				rubyText.WriteString(span.Text)
			}
			if rubyText.String() != segment.Text {
				return fmt.Errorf("public Game-only line %d segment %d ruby spans do not concatenate exactly", lineIndex+1, segmentIndex+1)
			}
			lineText.WriteString(segment.Text)
		}
		if lineText.String() != line.Japanese {
			return fmt.Errorf("public Game-only line %d segments do not concatenate exactly", lineIndex+1)
		}
	}
	return nil
}

func validateRecoveryPublicIndexItem(item PublicLyricsIndexSong, detail PublicLyricsDetailDocument) error {
	if item.MusicID <= 0 || item.Revision <= 0 || item.UpdatedAt == "" || item.State == "" ||
		item.Title.Japanese == "" || len(item.Title.Japanese) > model.PublicLyricsMaxTitleBytes ||
		len(item.Title.Chinese) > model.PublicLyricsMaxTitleBytes || len(item.Title.English) > model.PublicLyricsMaxTitleBytes {
		return errors.New("public lyrics index identity or title is invalid")
	}
	switch item.State {
	case PublicLyricsStateComplete, PublicLyricsStateGameOnly:
		if detail.MusicID != item.MusicID || detail.Revision != item.Revision || detail.UpdatedAt != item.UpdatedAt ||
			detail.State != item.State || !samePublicLyricsVersions(detail.AvailableVersions, item.AvailableVersions) ||
			len(item.AvailableVersions) == 0 || item.NoLyricsReason != "" {
			return errors.New("public lyrics text-bearing index/detail state is inconsistent")
		}
	case PublicLyricsStateSatisfiedNoLyrics:
		if detail.MusicID != 0 || len(item.AvailableVersions) != 0 ||
			item.NoLyricsReason != model.LyricsAvailabilityNoLyricsCatalogInstrumental {
			return errors.New("public satisfied no-lyrics index state is invalid")
		}
	case PublicLyricsStateAmbiguous, PublicLyricsStateMissing, PublicLyricsStateIncomplete, PublicLyricsStateFailed:
		if detail.MusicID != 0 || len(item.AvailableVersions) != 0 || item.NoLyricsReason != "" {
			return errors.New("public unresolved lyrics index state is invalid")
		}
	default:
		return fmt.Errorf("unsupported public lyrics state %q", item.State)
	}
	return nil
}
