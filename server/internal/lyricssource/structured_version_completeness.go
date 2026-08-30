package lyricssource

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type structuredVersionCompleteness uint8

const (
	structuredVersionCompletenessUnknown structuredVersionCompleteness = iota
	structuredVersionCompletenessComplete
	structuredVersionCompletenessTruncated
	structuredVersionCompletenessConflicting
)

// classifyStructuredVersionCompleteness recognizes only explicit, whole-token
// completeness labels. It deliberately leaves edits and other unlabeled
// renditions unknown rather than deriving completeness from position or size.
func classifyStructuredVersionCompleteness(label string) structuredVersionCompleteness {
	tokens := structuredVersionLabelTokens(label)
	complete := false
	truncated := false
	for index, token := range tokens {
		switch token {
		case "full", "long", "complete":
			complete = true
		case "short", "preview", "partial":
			truncated = true
		case "game":
			if index+1 < len(tokens) && tokens[index+1] == "size" {
				truncated = true
			}
		}
	}
	switch {
	case complete && truncated:
		return structuredVersionCompletenessConflicting
	case complete:
		return structuredVersionCompletenessComplete
	case truncated:
		return structuredVersionCompletenessTruncated
	default:
		return structuredVersionCompletenessUnknown
	}
}

func structuredVersionLabelTokens(label string) []string {
	value := strings.ToLower(norm.NFKC.String(label))
	return strings.FieldsFunc(value, func(current rune) bool {
		return !unicode.IsLetter(current) && !unicode.IsNumber(current)
	})
}

// preferExplicitCompleteStructuredVersions applies one conservative partial
// order: if any source candidate explicitly says full/long/complete without a
// conflicting truncation marker, only those candidates remain. It never ranks
// unknown versus truncated candidates and never breaks ties by input order.
func preferExplicitCompleteStructuredVersions(blocks []structuredVersionBlock) []structuredVersionBlock {
	complete := make([]structuredVersionBlock, 0, len(blocks))
	for _, block := range blocks {
		if classifyStructuredVersionCompleteness(block.label) == structuredVersionCompletenessComplete {
			complete = append(complete, block)
		}
	}
	if len(complete) == 0 {
		return blocks
	}
	return complete
}
