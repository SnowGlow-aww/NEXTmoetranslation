package lyricssource

import (
	"context"
	"errors"
	"time"
)

// Preview resolves the editor-selected candidate by its exact page and revision
// identity and returns the public preview envelope. It follows the same
// authoritative fallback chain as Search: the first provider with a verified
// candidate wins, so the returned preview belongs to the same provider the
// editor selected. A page/revision pair that does not match any reviewed
// candidate fails closed without fetching arbitrary pages.
func (registry *Registry) Preview(ctx context.Context, identity MusicIdentity, pageID, revisionID int) (Preview, error) {
	if registry == nil || len(registry.order) == 0 {
		return Preview{}, errors.New("lyrics source registry is not configured")
	}
	if ctx == nil {
		return Preview{}, errors.New("lyrics source preview requires context")
	}
	if pageID <= 0 || revisionID <= 0 {
		return Preview{}, ErrMalformedResponse
	}
	candidates, err := registry.Search(ctx, identity)
	if err != nil {
		return Preview{}, err
	}
	var matched *Candidate
	for index := range candidates {
		if candidates[index].PageID == pageID && candidates[index].RevisionID == revisionID {
			matched = &candidates[index]
			break
		}
	}
	if matched == nil {
		return Preview{}, ErrMalformedResponse
	}
	fixed, err := registry.FetchFixedCandidateRevision(ctx, identity, *matched)
	if err != nil {
		return Preview{}, err
	}
	return Preview{
		CanonicalURL:         fixed.CanonicalURL,
		PageID:               fixed.PageID,
		RevisionID:           fixed.RevisionID,
		SHA1:                 fixed.SHA1,
		Categories:           append([]string(nil), fixed.Categories...),
		FetchedAt:            fixed.FetchedAt.UTC().Format(time.RFC3339),
		Lines:                fixed.Lines,
		StructuredLines:      fixed.Extraction.Lines,
		RubyGeneratorVersion: fixed.Extraction.RubyGeneratorVersion,
	}, nil
}
