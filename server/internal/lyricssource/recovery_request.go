package lyricssource

import (
	"errors"
	"strconv"
	"strings"

	"moesekai/server/internal/model"
)

// ValidateRecoveryHTTPRequestBoundary proves that a bounded response is bound
// to one canonical provider-owned request before private forensic retention.
// It validates no response bytes and creates no provider authority.
func ValidateRecoveryHTTPRequestBoundary(
	provider model.LyricsSourceProvider,
	action string,
	value string,
) error {
	if strings.TrimSpace(action) != action {
		return errors.New("recovery HTTP action is invalid")
	}
	_, query, err := validateRecoveryRequestURL(provider, value)
	if err != nil {
		return err
	}
	switch action {
	case "page":
		if query.Get("generator") != "" || query.Get("prop") != "revisions|categories" {
			return errors.New("recovery page request operation is invalid")
		}
	case "search", "creator-alias":
		if query.Get("generator") != "search" || query.Get("prop") != "revisions|categories" {
			return errors.New("recovery search request operation is invalid")
		}
	default:
		return errors.New("recovery HTTP action is unsupported")
	}
	return nil
}

// ValidateRecoveryRevisionRequestIdentity proves that an exact canonical
// provider API request could have produced the bound revision envelope. Legacy
// callers may continue binding the public canonical revision URL instead.
func ValidateRecoveryRevisionRequestIdentity(
	provider model.LyricsSourceProvider,
	value string,
	evidence IndexEvidence,
) error {
	if evidence.Kind != IndexEvidenceKindMediaWikiRevision || evidence.Provider != provider ||
		evidence.RevisionID <= 0 || evidence.PageID <= 0 {
		return errors.New("recovery revision request evidence is invalid")
	}
	_, query, err := validateRecoveryRequestURL(provider, value)
	if err != nil {
		return err
	}
	if query.Get("generator") != "" || query.Get("prop") != "revisions|categories" {
		return errors.New("recovery revision request operation is invalid")
	}
	revisionID := strconv.Itoa(evidence.RevisionID)
	switch {
	case query.Get("revids") != "":
		if query.Get("revids") != revisionID || query.Get("titles") != "" || query.Get("pageids") != "" ||
			query.Get("redirects") != "" || query.Get("rvlimit") != "" || query.Get("generator") != "" {
			return errors.New("recovery revision request selector does not match evidence")
		}
	case query.Get("titles") != "":
		redirected := query.Get("redirects") == "1"
		if query.Get("pageids") != "" || query.Get("rvlimit") != "1" ||
			(query.Get("titles") != evidence.Title && !redirected) ||
			(query.Get("redirects") != "" && !redirected) {
			return errors.New("recovery title request selector does not match evidence")
		}
	case query.Get("pageids") != "":
		if query.Get("pageids") != strconv.Itoa(evidence.PageID) || query.Get("rvlimit") != "1" {
			return errors.New("recovery page request selector does not match evidence")
		}
	default:
		return errors.New("recovery revision request has no bounded selector")
	}
	return nil
}
