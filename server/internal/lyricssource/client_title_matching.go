package lyricssource

import (
	"html"

	"strings"

	"unicode"

	"golang.org/x/text/unicode/norm"
)

func candidateTitleMatches(title, wanted string) bool {
	title = strings.TrimSpace(norm.NFKC.String(html.UnescapeString(title)))
	wanted = strings.TrimSpace(norm.NFKC.String(html.UnescapeString(wanted)))
	if normalizeCatalogTitle(wanted) == "" || title == "" {
		return false
	}
	// A title containing a slash may itself be the complete catalog title. Only
	// after the complete form fails may slash boundaries be interpreted as a
	// creator suffix.
	if titleFormMatches(title, wanted) {
		return true
	}

	boundaries, ok := topLevelSlashBoundaries(title)
	if !ok {
		return false
	}
	matches := 0
	for _, boundary := range boundaries {
		prefix := strings.TrimSpace(title[:boundary])
		suffix := strings.TrimSpace(title[boundary+1:])
		if titleFormMatches(prefix, wanted) && validCreatorTitleSuffix(suffix) {
			matches++
		}
	}
	return matches == 1
}

func titleFormMatches(value, wanted string) bool {
	value = strings.TrimSpace(norm.NFKC.String(html.UnescapeString(value)))
	normalizedWanted := normalizeCatalogTitle(wanted)
	if normalizedWanted == "" {
		return false
	}
	if normalizeCatalogTitle(value) == normalizedWanted {
		return true
	}
	baseTitle, alternateTitle, hasTrailingParenthetical := splitTrailingParenthetical(value)
	return hasTrailingParenthetical && normalizeCatalogTitle(baseTitle) == normalizedWanted &&
		isWellFormedAlternateTitle(alternateTitle, normalizedWanted)
}

func normalizeCatalogTitle(value string) string {
	return canonicalCatalogTitle(value, true)
}

func canonicalCatalogTitle(value string, lowercase bool) string {
	value = norm.NFKC.String(html.UnescapeString(value))
	var typography strings.Builder
	for _, current := range value {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) {
			return ""
		}
		if isUnicodeVariationSelector(current) {
			continue
		}
		switch current {
		case '‘', '’', '‚', '‛', '`', '´', 'ʼ':
			typography.WriteRune('\'')
		case '“', '”', '„', '‟':
			typography.WriteRune('"')
		case '‐', '‑', '‒', '–', '—', '―', '−', '﹘':
			typography.WriteRune('-')
		case '〜', '∼', '˜':
			typography.WriteRune('~')
		case '…', '⋯':
			typography.WriteString("...")
		case '·', '･', '•', '‧':
			typography.WriteRune('・')
		default:
			typography.WriteRune(current)
		}
	}
	value = strings.Join(strings.Fields(typography.String()), " ")
	if value == "" {
		return ""
	}
	runes := []rune(value)
	var spaced strings.Builder
	for index, current := range runes {
		if current == ' ' && index > 0 && index+1 < len(runes) && optionalCatalogTitleSpace(runes[index-1], runes[index+1]) {
			continue
		}
		spaced.WriteRune(current)
	}
	value = spaced.String()
	if lowercase {
		value = strings.ToLower(value)
	}
	return value
}

func optionalCatalogTitleSpace(left, right rune) bool {
	return unicode.IsPunct(left) || unicode.IsSymbol(left) || unicode.IsPunct(right) || unicode.IsSymbol(right) ||
		isCJKCatalogTitleRune(left) || isCJKCatalogTitleRune(right)
}

func isCJKCatalogTitleRune(current rune) bool {
	return unicode.In(current, unicode.Han, unicode.Hiragana, unicode.Katakana) || strings.ContainsRune("々〆ヶヵー", current)
}

func isUnicodeVariationSelector(current rune) bool {
	return current >= '\ufe00' && current <= '\ufe0f' || current >= '\U000e0100' && current <= '\U000e01ef'
}

func isWellFormedAlternateTitle(value, normalizedBase string) bool {
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\t{}[]<>|") || isReservedEntityDisambiguator(value) {
		return false
	}
	letterOrNumberCount := 0
	for _, current := range norm.NFKC.String(value) {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) {
			return false
		}
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			letterOrNumberCount++
		}
	}
	normalized := normalizeCatalogTitle(value)
	return letterOrNumberCount >= 2 && normalized != "" && normalized != normalizedBase
}

func topLevelSlashBoundaries(title string) ([]int, bool) {
	depth := 0
	boundaries := []int{}
	for index, r := range title {
		switch r {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return nil, false
			}
			depth--
		case '/':
			if depth == 0 {
				boundaries = append(boundaries, index)
			}
		}
	}
	return boundaries, depth == 0
}

func validCreatorTitleSuffix(value string) bool {
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\t{}[]|=") {
		return false
	}
	contributors, ok := splitTopLevelContributors(value)
	if !ok || len(contributors) == 0 || len(contributors) > 8 {
		return false
	}
	for _, contributor := range contributors {
		if normalizeCatalogTitle(contributor) == "" || isReservedEntityDisambiguator(contributor) {
			return false
		}
	}
	return true
}

func splitTrailingParenthetical(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[len(value)-1] != ')' {
		return "", "", false
	}
	depth := 0
	for index := len(value) - 1; index >= 0; index-- {
		switch value[index] {
		case ')':
			depth++
		case '(':
			depth--
			if depth < 0 {
				return "", "", false
			}
			if depth == 0 {
				baseTitle := strings.TrimSpace(value[:index])
				parenthetical := strings.TrimSpace(value[index+1 : len(value)-1])
				return baseTitle, parenthetical, baseTitle != ""
			}
		}
	}
	return "", "", false
}

func isReservedEntityDisambiguator(value string) bool {
	normalized := strings.ToLower(norm.NFKC.String(html.UnescapeString(value)))
	tokens := strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for index, token := range tokens {
		switch token {
		case "album", "albums", "single", "singles", "ep", "eps", "cover", "covers", "remix", "remixes",
			"rerecorded", "rerecording", "reloaded", "rerec", "reunion", "anniversary", "short", "preview", "partial",
			"medley", "version", "versions", "ver":
			return true
		}
		if hasNumericSuffix(token, "ver") || hasNumericSuffix(token, "version") {
			return true
		}
		if index+1 < len(tokens) && ((token == "game" && tokens[index+1] == "size") ||
			(token == "re" && (tokens[index+1] == "rec" || tokens[index+1] == "recorded" || tokens[index+1] == "recording"))) {
			return true
		}
	}
	compact := strings.Map(func(current rune) rune {
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			return current
		}
		return -1
	}, normalized)
	for _, reserved := range []string{
		"曖昧さ回避", "消歧义", "消歧義",
		"アルバム", "专辑", "專輯",
		"シングル", "单曲", "單曲",
		"カバー", "翻唱",
		"リミックス", "混音", "重混",
		"リレコーディング", "再レコーディング", "再録", "再录", "再錄", "再収録", "再收录", "再收錄", "重録", "重录", "重錄", "録り直し", "重新录制", "重新錄製", "录音重制", "錄音重製",
		"アニバーサリー", "周年", "記念", "纪念", "紀念",
		"ゲームサイズ", "游戏尺寸", "遊戲尺寸", "游戏版", "遊戲版",
		"ショート", "短版", "短版本", "短尺",
		"プレビュー", "试听", "試聴", "試聽", "预览", "預覽", "预告", "預告",
		"パーシャル", "部分", "一部",
		"メドレー", "组曲", "組曲", "串烧", "串燒",
		"バージョン", "版本",
	} {
		if strings.Contains(compact, reserved) {
			return true
		}
	}
	for _, current := range compact {
		if current == '版' {
			return true
		}
	}
	return false
}

func hasNumericSuffix(value, prefix string) bool {
	suffix := strings.TrimPrefix(value, prefix)
	if suffix == "" || suffix == value {
		return false
	}
	for _, r := range suffix {
		if !unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

func hasExcludedVersionSignal(title, content string, categories []string) bool {
	// Version identity belongs to the page title, page-level categories, and
	// lead metadata. Later sections commonly document covers, game-size edits,
	// or medleys derived from an otherwise valid full/original song; scanning
	// those sections would misclassify the primary page.
	combined := strings.ToLower(title + "\n" + strings.Join(categories, "\n") + "\n" + primaryPageMetadata(content))
	for _, signal := range []string{
		"album version", "album ver", "single version", "single ver", "ep version", "ep ver",
		"cover version", "cover ver", "remix", "re-recorded", "rerecorded", "anniversary version", "anniversary ver",
		"game size", "game-size", "short version", "short ver", "preview version", "preview ver",
		"partial version", "partial ver", "medley",
		"アルバムバージョン", "シングルバージョン", "カバー", "リミックス", "再録", "再レコーディング",
		"アニバーサリー", "周年記念版", "ゲームサイズ", "ショートバージョン", "プレビュー", "一部版", "メドレー", "別バージョン",
		"专辑版", "專輯版", "单曲版", "單曲版", "翻唱版", "混音版", "重新录制", "重新錄製",
		"周年纪念版", "周年紀念版", "游戏尺寸", "遊戲尺寸", "游戏版", "遊戲版", "短版本",
		"预览版", "預覽版", "部分版", "组曲版", "組曲版", "其他版本",
	} {
		if strings.Contains(combined, signal) {
			return true
		}
	}
	return false
}

func primaryPageMetadata(content string) string {
	content = inactiveRestrictionMarkupPattern.ReplaceAllString(content, "")
	content = strings.ReplaceAll(content, "\r", "")
	if heading := topLevelHeadingPattern.FindStringIndex(content); heading != nil {
		content = content[:heading[0]]
	}
	return content
}

func identityFields(value string) []string {
	raw := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("|/／,，;；:=：[]{}()（）<>\"'", r)
	})
	fields := make([]string, 0, len(raw))
	for _, field := range raw {
		if normalized := normalizeTitle(field); normalized != "" {
			fields = append(fields, normalized)
		}
	}
	return fields
}

func containsIdentityField(value, wanted string, allowJapaneseSuffix bool) bool {
	for _, field := range identityFields(value) {
		if field == wanted {
			return true
		}
		if allowJapaneseSuffix && strings.HasPrefix(field, wanted) {
			suffix := strings.TrimPrefix(field, wanted)
			for _, allowed := range []string{"による", "の", "制作", "作詞", "作曲"} {
				if suffix == allowed || strings.HasPrefix(suffix, allowed) {
					return true
				}
			}
		}
	}
	return false
}
