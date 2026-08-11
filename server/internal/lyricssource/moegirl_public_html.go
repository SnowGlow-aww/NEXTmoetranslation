package lyricssource

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const moegirlPublicLyricsRightsNotice = "本段落中所使用的歌词，其著作权属于原著作权人，仅以介绍为目的引用。"

var (
	moegirlPublicHTMLLinePattern = regexp.MustCompile(`(?s)<div class="Lyrics-line"><div class="Lyrics-original"[^>]*><span lang="ja">(.*?)</span></div><div class="Lyrics-translated"[^>]*><span lang="zh">(.*?)</span></div></div>`)
	moegirlPublicHTMLJSONText    = regexp.MustCompile(`^"(?:\\.|[^"\\])*"$`)
)

// MoegirlPublicHTMLLine is one exact Japanese/Chinese pair extracted from the
// rendered Lyrics component. Blank rendered rows become stanza boundaries and
// are not retained as text lines.
type MoegirlPublicHTMLLine struct {
	Japanese          string `json:"japanese"`
	Translation       string `json:"translation"`
	StanzaBreakBefore bool   `json:"stanzaBreakBefore,omitempty"`
}

// MoegirlPublicHTMLExtraction contains only the identity and lyrics fields
// needed for the user-authorized single-page exception. It has no romaji field.
type MoegirlPublicHTMLExtraction struct {
	PageURL       string                  `json:"pageUrl"`
	PageTitle     string                  `json:"pageTitle"`
	JapaneseTitle string                  `json:"japaneseTitle"`
	PageID        int                     `json:"pageId"`
	RevisionID    int                     `json:"revisionId"`
	RightsNotice  string                  `json:"rightsNotice"`
	Lines         []MoegirlPublicHTMLLine `json:"lines"`
}

// ParseMoegirlPublicPageHTML parses one already-retained exact public article
// response. The parser accepts only the reviewed no-ruby, Japanese-plus-Chinese
// Lyrics DOM shape and fails closed on any unknown nested markup.
func ParseMoegirlPublicPageHTML(
	raw []byte,
	expectedPageURL string,
) (MoegirlPublicHTMLExtraction, error) {
	target, err := MoegirlPageURLTargetForURL(expectedPageURL)
	if err != nil || len(raw) == 0 || len(raw) > maxResponseBytes || !utf8.Valid(raw) {
		return MoegirlPublicHTMLExtraction{}, ErrMalformedResponse
	}
	content := string(raw)
	if strings.ContainsRune(content, '\x00') || strings.Count(content, moegirlPublicLyricsRightsNotice) != 1 ||
		strings.Count(content, `<meta property="og:url" content="`+expectedPageURL+`">`) != 1 {
		return MoegirlPublicHTMLExtraction{}, ErrMalformedResponse
	}
	pageTitle, err := exactMoegirlPublicHTMLJSONString(content, "wgPageName")
	if err != nil || pageTitle != target.PageTitle {
		return MoegirlPublicHTMLExtraction{}, ErrRevisionChanged
	}
	configuredTitle, err := exactMoegirlPublicHTMLJSONString(content, "wgTitle")
	if err != nil || configuredTitle != pageTitle {
		return MoegirlPublicHTMLExtraction{}, ErrRevisionChanged
	}
	pageID, err := exactMoegirlPublicHTMLJSONInt(content, "wgArticleId")
	if err != nil || pageID <= 0 {
		return MoegirlPublicHTMLExtraction{}, ErrMalformedResponse
	}
	revisionID, err := exactMoegirlPublicHTMLJSONInt(content, "wgCurRevisionId")
	if err != nil || revisionID <= 0 {
		return MoegirlPublicHTMLExtraction{}, ErrMalformedResponse
	}
	if strings.Count(content, `"wgIsArticle":true`) != 1 || strings.Count(content, `"wgIsRedirect":false`) != 1 {
		return MoegirlPublicHTMLExtraction{}, ErrMalformedResponse
	}
	japaneseTitle, err := moegirlPublicHTMLDocumentTitle(content)
	if err != nil {
		return MoegirlPublicHTMLExtraction{}, err
	}
	lines, err := moegirlPublicHTMLLyricsLines(content)
	if err != nil {
		return MoegirlPublicHTMLExtraction{}, err
	}
	return MoegirlPublicHTMLExtraction{
		PageURL: expectedPageURL, PageTitle: pageTitle, JapaneseTitle: japaneseTitle,
		PageID: pageID, RevisionID: revisionID,
		RightsNotice: moegirlPublicLyricsRightsNotice, Lines: lines,
	}, nil
}

func exactMoegirlPublicHTMLJSONString(content, key string) (string, error) {
	marker := `"` + key + `":`
	start := strings.Index(content, marker)
	if start < 0 || strings.Index(content[start+len(marker):], marker) >= 0 {
		return "", ErrMalformedResponse
	}
	value := content[start+len(marker):]
	if len(value) == 0 || value[0] != '"' {
		return "", ErrMalformedResponse
	}
	end := 1
	escaped := false
	for end < len(value) {
		current := value[end]
		if current == '"' && !escaped {
			end++
			break
		}
		if current == '\\' && !escaped {
			escaped = true
		} else {
			escaped = false
		}
		end++
	}
	encoded := value[:end]
	if !moegirlPublicHTMLJSONText.MatchString(encoded) {
		return "", ErrMalformedResponse
	}
	decoded, err := strconv.Unquote(encoded)
	if err != nil || decoded == "" || strings.TrimSpace(decoded) != decoded {
		return "", ErrMalformedResponse
	}
	return decoded, nil
}

func exactMoegirlPublicHTMLJSONInt(content, key string) (int, error) {
	marker := `"` + key + `":`
	start := strings.Index(content, marker)
	if start < 0 || strings.Index(content[start+len(marker):], marker) >= 0 {
		return 0, ErrMalformedResponse
	}
	value := content[start+len(marker):]
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, ErrMalformedResponse
	}
	parsed, err := strconv.Atoi(value[:end])
	if err != nil || parsed <= 0 {
		return 0, ErrMalformedResponse
	}
	return parsed, nil
}

func moegirlPublicHTMLDocumentTitle(content string) (string, error) {
	const startMarker = "<title>"
	const suffix = " - 萌娘百科 万物皆可萌的百科全书</title>"
	start := strings.Index(content, startMarker)
	if start < 0 || strings.Index(content[start+len(startMarker):], startMarker) >= 0 {
		return "", ErrMalformedResponse
	}
	value := content[start+len(startMarker):]
	end := strings.Index(value, suffix)
	if end < 0 || strings.ContainsAny(value[:end], "<>") {
		return "", ErrMalformedResponse
	}
	title := strings.TrimSpace(html.UnescapeString(value[:end]))
	if title == "" || len(title) > maxExtractedLineBytes || !utf8.ValidString(title) {
		return "", ErrMalformedResponse
	}
	return title, nil
}

func moegirlPublicHTMLLyricsLines(content string) ([]MoegirlPublicHTMLLine, error) {
	const prefix = `<div class="Lyrics Lyrics-no-ruby Lyrics-has-translate" style="">`
	const nextHeading = `<div class="mw-heading mw-heading2"><h2 id="注释与外部链接"`
	start := strings.Index(content, prefix)
	if start < 0 || strings.Index(content[start+len(prefix):], prefix) >= 0 {
		return nil, ErrMalformedResponse
	}
	endOffset := strings.Index(content[start:], nextHeading)
	if endOffset < 0 {
		return nil, ErrMalformedResponse
	}
	section := content[start : start+endOffset]
	matches := moegirlPublicHTMLLinePattern.FindAllStringSubmatchIndex(section, -1)
	if len(matches) == 0 || len(matches) > maxExtractedLines {
		return nil, ErrMissingLyrics
	}
	if section[:matches[0][0]] != prefix || section[matches[len(matches)-1][1]:] != `<div style="clear:both"></div></div>`+"\n" {
		return nil, ErrMalformedResponse
	}
	cursor := matches[0][0]
	lines := make([]MoegirlPublicHTMLLine, 0, len(matches))
	stanzaBreak := false
	totalBytes := 0
	for _, match := range matches {
		if match[0] != cursor || len(match) != 6 {
			return nil, ErrMalformedResponse
		}
		cursor = match[1]
		japanese, err := moegirlPublicHTMLText(section[match[2]:match[3]])
		if err != nil {
			return nil, err
		}
		translation, err := moegirlPublicHTMLText(section[match[4]:match[5]])
		if err != nil {
			return nil, err
		}
		if japanese == "" || translation == "" {
			if japanese != translation {
				return nil, ErrMalformedResponse
			}
			stanzaBreak = len(lines) > 0
			continue
		}
		if len(japanese) > maxExtractedLineBytes || len(translation) > maxExtractedLineBytes ||
			totalBytes > maxExtractedTextBytes-len(japanese)-len(translation) {
			return nil, ErrLyricsTooLarge
		}
		totalBytes += len(japanese) + len(translation)
		lines = append(lines, MoegirlPublicHTMLLine{
			Japanese: japanese, Translation: translation, StanzaBreakBefore: stanzaBreak,
		})
		stanzaBreak = false
	}
	if len(lines) == 0 || stanzaBreak {
		return nil, ErrMalformedResponse
	}
	return lines, nil
}

func moegirlPublicHTMLText(value string) (string, error) {
	if strings.ContainsAny(value, "<>") {
		return "", ErrMalformedResponse
	}
	decoded := strings.TrimSpace(html.UnescapeString(value))
	if !utf8.ValidString(decoded) || strings.ContainsAny(decoded, "\r\n\x00") {
		return "", ErrMalformedResponse
	}
	return decoded, nil
}
