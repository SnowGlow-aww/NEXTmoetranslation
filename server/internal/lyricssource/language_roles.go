package lyricssource

import (
	"strings"
	"unicode"
)

type structuredTabLanguage uint8

const (
	structuredTabLanguageUnknown structuredTabLanguage = iota
	structuredTabLanguageJapanese
	structuredTabLanguageEnglish
	structuredTabLanguageChinese
	structuredTabLanguageKorean
	structuredTabLanguageSpanish
	structuredTabLanguageRomanized
)

type structuredLanguageLabel struct {
	language             structuredTabLanguage
	explicitTranslation  bool
	explicitApproval     bool
	conflictingLanguages bool
}

func assignStructuredVersionLanguageRoles(blocks []structuredVersionBlock) {
	labels := make([]structuredLanguageLabel, len(blocks))
	hasJapaneseSource := false
	for index, block := range blocks {
		labels[index] = parseStructuredLanguageLabel(block.label)
		if labels[index].language == structuredTabLanguageJapanese && !labels[index].explicitTranslation &&
			!labels[index].conflictingLanguages {
			hasJapaneseSource = true
		}
	}

	multilingualTabContext := len(blocks) > 1
	for index, label := range labels {
		blocks[index].languageRole = structuredLanguageRole(label, multilingualTabContext, hasJapaneseSource)
	}
}

func structuredLanguageRole(label structuredLanguageLabel, multilingualTabContext, hasJapaneseSource bool) string {
	if label.explicitTranslation || label.conflictingLanguages {
		return "translation"
	}
	switch label.language {
	case structuredTabLanguageJapanese:
		return "source"
	case structuredTabLanguageChinese, structuredTabLanguageKorean, structuredTabLanguageSpanish, structuredTabLanguageRomanized:
		return "translation"
	case structuredTabLanguageEnglish:
		if label.explicitApproval || multilingualTabContext && hasJapaneseSource {
			return "translation"
		}
		return "source"
	default:
		if label.explicitApproval {
			return "translation"
		}
		return "source"
	}
}

func parseStructuredLanguageLabel(label string) structuredLanguageLabel {
	value := normalizeStructuredLanguageLabel(label)
	parsed := structuredLanguageLabel{
		explicitTranslation: structuredLanguageLabelHasWord(value, "translation") ||
			structuredLanguageLabelHasWord(value, "translations") ||
			structuredLanguageLabelHasWord(value, "translated"),
		explicitApproval: structuredLanguageLabelHasWord(value, "official") ||
			structuredLanguageLabelHasWord(value, "approved"),
	}

	base := stripStructuredLanguageLabelQualifiers(value)
	parsed.language = exactStructuredTabLanguage(base)
	if parsed.language == structuredTabLanguageUnknown {
		detected := structuredTabLanguageUnknown
		for _, field := range strings.Fields(base) {
			language := exactStructuredTabLanguage(field)
			if language == structuredTabLanguageUnknown {
				continue
			}
			if detected != structuredTabLanguageUnknown && detected != language {
				parsed.conflictingLanguages = true
				return parsed
			}
			detected = language
		}
		parsed.language = detected
	}
	return parsed
}

func exactStructuredTabLanguage(value string) structuredTabLanguage {
	switch value {
	case "japanese", "日本語", "日本語歌詞", "日本語 歌詞":
		return structuredTabLanguageJapanese
	case "english":
		return structuredTabLanguageEnglish
	case "chinese", "mandarin", "中文", "中文歌词", "中文歌詞", "pinyin":
		return structuredTabLanguageChinese
	case "korean", "한국어", "한국어 가사", "한국어가사", "한국어 번역", "한국어번역":
		return structuredTabLanguageKorean
	case "spanish", "español", "espanol", "letras en español", "letras en espanol",
		"traducción al español", "traduccion al espanol", "traducción en español", "traduccion en espanol":
		return structuredTabLanguageSpanish
	case "romaji", "romanization", "romanisation", "romanized", "romanised":
		return structuredTabLanguageRomanized
	default:
		return structuredTabLanguageUnknown
	}
}

func normalizeStructuredLanguageLabel(label string) string {
	value := strings.ToLower(label)
	value = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ").Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	return strings.Trim(value, " \t:=|-–—")
}

func stripStructuredLanguageLabelQualifiers(value string) string {
	prefixes := []string{"approved ", "official ", "translated ", "translation "}
	suffixes := []string{" translated lyrics", " translation lyrics", " translations", " translation", " translated", " lyrics"}
	for {
		original := value
		for _, prefix := range prefixes {
			if strings.HasPrefix(value, prefix) {
				value = strings.TrimPrefix(value, prefix)
				break
			}
		}
		for _, suffix := range suffixes {
			if strings.HasSuffix(value, suffix) {
				value = strings.TrimSuffix(value, suffix)
				break
			}
		}
		value = strings.TrimSpace(value)
		if value == original {
			return value
		}
	}
}

func structuredLanguageLabelHasWord(value, wanted string) bool {
	for _, word := range strings.FieldsFunc(value, func(current rune) bool {
		return !unicode.IsLetter(current) && !unicode.IsNumber(current)
	}) {
		if word == wanted {
			return true
		}
	}
	return false
}
