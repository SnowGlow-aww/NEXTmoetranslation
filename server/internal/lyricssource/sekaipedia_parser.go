package lyricssource

import (
	"fmt"

	"regexp"

	"strings"

	"moesekai/server/internal/model"
)

const (
	sekaipediaSingerAliasVersion             = "sekaipedia-singer-aliases-v3"
	historicalSekaipediaRubyGeneratorVersion = "sekaipedia-ruby-kana-v1"
	sekaipediaRubyGeneratorVersion           = "sekaipedia-ruby-kana-v2"
	sekaipediaSameLyricsNote                 = "''The game cut and full versions contain the same lyrics.''"
	sekaipediaSameLyricsHyphenatedNote       = "''The game-cut and full versions contain the same lyrics.''"
	sekaipediaSameDurationAndLyricsNote      = "''The game cut and full versions contain the same duration and lyrics.''"
)

var sekaipediaLevelThreeHeadingPattern = regexp.MustCompile(`(?m)^===([^=].*?)===[ \t]*$`)

type sekaipediaListTarget struct {
	pageTitle         string
	display           string
	resolvedPageTitle string
}

type sekaipediaSongExtraction struct {
	// The singular fields are a deterministic compatibility view for source-v2
	// callers. Renditions is the source-authoritative peer set and is never
	// filtered by catalog policy.
	Full                    Extraction
	Game                    *Extraction
	GameLineIndexes         []int
	Section                 string
	GameSection             string
	RenditionKey            string
	ReasonCode              model.LyricsSourceVersionReasonCode
	AuthoritativeStructured bool
	AlternateVocals         []sekaipediaAlternateVocalExtraction
	Renditions              []sekaipediaPeerRenditionExtraction
}

type sekaipediaPeerRenditionExtraction struct {
	RenditionKey           string
	Kind                   string
	SourceTabPaths         []model.LyricsSourceTabPath
	Full                   *Extraction
	Game                   *Extraction
	FullSection            string
	GameSection            string
	GameLineIndexes        []int
	ReasonCode             model.LyricsSourceVersionReasonCode
	SourcePerformerIDs     []string
	FullStructuredEvidence sekaipediaPerformerEvidenceState
	GameStructuredEvidence sekaipediaPerformerEvidenceState
	fullProjectionLines    []sekaipediaColumnLine
	gameProjectionLines    []sekaipediaColumnLine
}

type sekaipediaPerformerEvidenceState string

const (
	sekaipediaPerformerEvidenceNone     sekaipediaPerformerEvidenceState = "none"
	sekaipediaPerformerEvidencePartial  sekaipediaPerformerEvidenceState = "source_partial_structured"
	sekaipediaPerformerEvidenceComplete sekaipediaPerformerEvidenceState = "source_complete_structured"
)

type sekaipediaAlternateVocalExtraction struct {
	Key                    string
	TabLabel               string
	SingerLabel            string
	SingerIDs              []string
	DeclaredFull           bool
	DeclaredGame           bool
	Full                   *Extraction
	Game                   *Extraction
	FullTabPath            model.LyricsSourceTabPath
	GameTabPath            model.LyricsSourceTabPath
	FullStructuredEvidence sekaipediaPerformerEvidenceState
	GameStructuredEvidence sekaipediaPerformerEvidenceState
	fullProjectionLines    []sekaipediaColumnLine
	gameProjectionLines    []sekaipediaColumnLine
}

type sekaipediaLyricsHead struct {
	sourceColumn  string
	hasRomaji     bool
	englishSource bool
	declared      map[string]bool
}

type sekaipediaTemplate struct {
	name   string
	fields []string
}

type sekaipediaVersionRecord struct {
	kind    string
	label   string
	singers string
}

type sekaipediaSinger struct {
	id          string
	persistedID string
	name        string
	order       int
	virtual     bool
	aliases     []string
}

type sekaipediaSingerSet struct {
	kind string
	ids  []string
}

type sekaipediaColumnSegment struct {
	text                 string
	performerIDs         []string
	ruby                 []RubySpan
	sourceGroup          int
	sourceSegmentOrdinal int
}

type sekaipediaColumnLine struct {
	segments                   []sekaipediaColumnSegment
	stanzaBreakBefore          bool
	rubyFallback               []RubySpan
	allowUniqueDictionaryProbe bool
}

type sekaipediaRenditionExtraction struct {
	extraction      Extraction
	projectionLines []sekaipediaColumnLine
	usedIDs         map[string]struct{}
	aligned         bool
	japaneseOnly    bool
	sourceTagged    bool
	segments        int
	set             sekaipediaSingerSet
}

var sekaipediaSingers = []sekaipediaSinger{
	{id: "ichika", persistedID: "歌唱者-01", name: "星乃一歌", order: 1, aliases: []string{"Ichika", "Hoshino Ichika"}},
	{id: "saki", persistedID: "歌唱者-02", name: "天馬咲希", order: 2, aliases: []string{"Saki", "Tenma Saki", "HSaki"}},
	{id: "honami", persistedID: "歌唱者-03", name: "望月穂波", order: 3, aliases: []string{"Honami", "Mochizuki Honami"}},
	{id: "shiho", persistedID: "歌唱者-04", name: "日野森志歩", order: 4, aliases: []string{"Shiho", "Hinomori Shiho"}},
	{id: "minori", persistedID: "歌唱者-05", name: "花里みのり", order: 5, aliases: []string{"Minori", "Hanasato Minori"}},
	{id: "haruka", persistedID: "歌唱者-06", name: "桐谷遥", order: 6, aliases: []string{"Haruka", "Haurka", "Kiritani Haruka"}},
	{id: "airi", persistedID: "歌唱者-07", name: "桃井愛莉", order: 7, aliases: []string{"Airi", "Momoi Airi"}},
	{id: "shizuku", persistedID: "歌唱者-08", name: "日野森雫", order: 8, aliases: []string{"Shizuku", "Hinomori Shizuku"}},
	{id: "kohane", persistedID: "歌唱者-09", name: "小豆沢こはね", order: 9, aliases: []string{"Kohane", "Azusawa Kohane"}},
	{id: "an", persistedID: "歌唱者-10", name: "白石杏", order: 10, aliases: []string{"An", "Shiraishi An"}},
	{id: "akito", persistedID: "歌唱者-11", name: "東雲彰人", order: 11, aliases: []string{"Akito", "Shinonome Akito"}},
	{id: "toya", persistedID: "歌唱者-12", name: "青柳冬弥", order: 12, aliases: []string{"Toya", "Aoyagi Toya"}},
	{id: "tsukasa", persistedID: "歌唱者-13", name: "天馬司", order: 13, aliases: []string{"Tsukasa", "Tenma Tsukasa", "Tsukase"}},
	{id: "emu", persistedID: "歌唱者-14", name: "鳳えむ", order: 14, aliases: []string{"Emu", "Otori Emu"}},
	{id: "nene", persistedID: "歌唱者-15", name: "草薙寧々", order: 15, aliases: []string{"Nene", "Kusanagi Nene"}},
	{id: "rui", persistedID: "歌唱者-16", name: "神代類", order: 16, aliases: []string{"Rui", "Kamishiro Rui"}},
	{id: "kanade", persistedID: "歌唱者-17", name: "宵崎奏", order: 17, aliases: []string{"Kanade", "Yoisaki Kanade"}},
	{id: "mafuyu", persistedID: "歌唱者-18", name: "朝比奈まふゆ", order: 18, aliases: []string{"Mafuyu", "Asahina Mafuyu"}},
	{id: "ena", persistedID: "歌唱者-19", name: "東雲絵名", order: 19, aliases: []string{"Ena", "Shinonome Ena"}},
	{id: "mizuki", persistedID: "歌唱者-20", name: "暁山瑞希", order: 20, aliases: []string{"Mizuki", "Akiyama Mizuki"}},
	{id: "miku", persistedID: "歌唱者-21", name: "初音ミク", order: 21, virtual: true, aliases: []string{"Miku", "Hatsune Miku"}},
	{id: "rin", persistedID: "歌唱者-22", name: "鏡音リン", order: 22, virtual: true, aliases: []string{"Rin", "Kagamine Rin"}},
	{id: "len", persistedID: "歌唱者-23", name: "鏡音レン", order: 23, virtual: true, aliases: []string{"Len", "Kagamine Len"}},
	{id: "luka", persistedID: "歌唱者-24", name: "巡音ルカ", order: 24, virtual: true, aliases: []string{"Luka", "Megurine Luka"}},
	{id: "meiko", persistedID: "歌唱者-25", name: "MEIKO", order: 25, virtual: true, aliases: []string{"MEIKO", "Meiko"}},
	{id: "kaito", persistedID: "歌唱者-26", name: "KAITO", order: 26, virtual: true, aliases: []string{"KAITO", "Kaito"}},
	{id: "gumi", persistedID: "外部歌唱者-01", name: "GUMI", order: 27, virtual: true, aliases: []string{"GUMI", "Gumi"}},
	{id: "teto", persistedID: "外部歌唱者-02", name: "Kasane Teto", order: 28, virtual: true, aliases: []string{"Kasane Teto", "Teto"}},
	{id: "flower", persistedID: "外部歌唱者-03", name: "flower", order: 29, virtual: true, aliases: []string{"flower"}},
	{id: "nenerobo", persistedID: "外部歌唱者-04", name: "Nenerobo", order: 30, aliases: []string{"Nenerobo"}},
	{id: "mikudayo", persistedID: "外部歌唱者-05", name: "Mikudayo", order: 31, aliases: []string{"Mikudayo"}},
	{id: "gakupo", persistedID: "外部歌唱者-06", name: "Kamui Gakupo", order: 32, virtual: true, aliases: []string{"Kamui Gakupo", "Gakupo", "Gackpo"}},
	{id: "kafu", persistedID: "外部歌唱者-07", name: "KAFU", order: 33, virtual: true, aliases: []string{"KAFU", "Kafu"}},
	{id: "gekiyaku", persistedID: "外部歌唱者-08", name: "Gekiyaku", order: 34, virtual: true, aliases: []string{"Gekiyaku"}},
	{id: "sekai_voice", persistedID: "外部歌唱者-09", name: "SEKAI", order: 35, virtual: true, aliases: []string{"SEKAI"}},
	{id: "kiritan", persistedID: "外部歌唱者-10", name: "Tohoku Kiritan", order: 36, virtual: true, aliases: []string{"Tohoku Kiritan", "Kiritan"}},
	{id: "zundamon", persistedID: "外部歌唱者-11", name: "Zundamon", order: 37, virtual: true, aliases: []string{"Zundamon"}},
	{id: "kaai_yuki", persistedID: "外部歌唱者-12", name: "Kaai Yuki", order: 38, virtual: true, aliases: []string{"Kaai Yuki", "Yuki"}},
	{id: "adachi_rei", persistedID: "外部歌唱者-13", name: "Adachi Rei", order: 39, virtual: true, aliases: []string{"Adachi Rei", "Rei"}},
	{id: "rime", persistedID: "外部歌唱者-14", name: "RIME", order: 40, virtual: true, aliases: []string{"RIME", "Rime"}},
	{id: "hanakuma_chifuyu", persistedID: "外部歌唱者-15", name: "Hanakuma Chifuyu", order: 41, virtual: true, aliases: []string{"Hanakuma Chifuyu"}},
	{id: "vy1", persistedID: "外部歌唱者-16", name: "VY1", order: 42, virtual: true, aliases: []string{"VY1"}},
	{id: "solaria", persistedID: "外部歌唱者-17", name: "SOLARIA", order: 43, virtual: true, aliases: []string{"SOLARIA"}},
	{id: "kotonoha_aoi", persistedID: "外部歌唱者-18", name: "Kotonoha Aoi", order: 44, virtual: true, aliases: []string{"Kotonoha Aoi", "Aoi"}},
	{id: "kotonoha_akane", persistedID: "外部歌唱者-19", name: "Kotonoha Akane", order: 45, virtual: true, aliases: []string{"Kotonoha Akane", "Akane"}},
	{id: "merrow", persistedID: "外部歌唱者-20", name: "Merrow", order: 46, virtual: true, aliases: []string{"Merrow"}},
	{id: "meika_mikoto", persistedID: "外部歌唱者-21", name: "Meika Mikoto", order: 47, virtual: true, aliases: []string{"Meika Mikoto", "Mikoto"}},
	{id: "sazanami_jun", persistedID: "外部歌唱者-22", name: "Sazanami Jun", order: 48, aliases: []string{"Sazanami Jun", "Jun"}},
	{id: "sena_izumi", persistedID: "外部歌唱者-23", name: "Sena Izumi", order: 49, aliases: []string{"Sena Izumi", "Izumi"}},
	{id: "morisawa_chiaki", persistedID: "外部歌唱者-24", name: "Morisawa Chiaki", order: 50, aliases: []string{"Morisawa Chiaki", "Chiaki"}},
	{id: "sakasaki_natsume", persistedID: "外部歌唱者-25", name: "Sakasaki Natsume", order: 51, aliases: []string{"Sakasaki Natsume", "Natsume"}},
	{id: "natsuki_karin", persistedID: "外部歌唱者-26", name: "Natsuki Karin", order: 52, virtual: true, aliases: []string{"Natsuki Karin"}},
	{id: "otomachi_una", persistedID: "外部歌唱者-27", name: "Otomachi Una", order: 53, virtual: true, aliases: []string{"Otomachi Una", "Una"}},
	{id: "ia", persistedID: "外部歌唱者-28", name: "IA", order: 54, virtual: true, aliases: []string{"IA"}},
	{id: "yuzuki_yukari", persistedID: "外部歌唱者-29", name: "Yuzuki Yukari", order: 55, virtual: true, aliases: []string{"Yuzuki Yukari", "Yukari"}},
	{id: "haru", persistedID: "外部歌唱者-30", name: "HARU", order: 56, virtual: true, aliases: []string{"HARU", "Haru"}},
	{id: "coko", persistedID: "外部歌唱者-31", name: "COKO", order: 57, virtual: true, aliases: []string{"COKO", "Coko"}},
	{id: "hiiro_amagi", persistedID: "外部歌唱者-32", name: "Hiiro Amagi", order: 58, aliases: []string{"Hiiro Amagi", "Hiiro"}},
	{id: "aira_shiratori", persistedID: "外部歌唱者-33", name: "Aira Shiratori", order: 59, aliases: []string{"Aira Shiratori", "Aira"}},
	{id: "mayoi_ayase", persistedID: "外部歌唱者-34", name: "Mayoi Ayase", order: 60, aliases: []string{"Mayoi Ayase", "Mayoi"}},
	{id: "tatsumi_kazehaya", persistedID: "外部歌唱者-35", name: "Tatsumi Kazehaya", order: 61, aliases: []string{"Tatsumi Kazehaya", "Tatsumi"}},
	{id: "rinne_amagi", persistedID: "外部歌唱者-36", name: "Rinne Amagi", order: 62, aliases: []string{"Rinne Amagi", "Rinne"}},
	{id: "himeru", persistedID: "外部歌唱者-37", name: "Himeru", order: 63, aliases: []string{"Himeru"}},
	{id: "kohaku_oukawa", persistedID: "外部歌唱者-38", name: "Kohaku Oukawa", order: 64, aliases: []string{"Kohaku Oukawa", "Kohaku"}},
	{id: "niki_shiina", persistedID: "外部歌唱者-39", name: "Niki Shiina", order: 65, aliases: []string{"Niki Shiina", "Niki"}},
}

func parseSekaipediaListAuthority(content string) ([]sekaipediaListTarget, error) {
	if !utf8ValidBounded(content, maxResponseBytes) {
		return nil, ErrMalformedResponse
	}
	listSection, err := sekaipediaTopLevelSection(content, "List")
	if err != nil {
		return nil, err
	}
	matches := sekaipediaLevelThreeHeadingPattern.FindAllStringSubmatchIndex(listSection, -1)
	wantHeadings := []string{"Pre-existing songs", "Commissioned songs", "Upcoming announced songs", "Removed songs"}
	if len(matches) != len(wantHeadings) {
		return nil, ErrUnsupportedTable
	}

	result := []sekaipediaListTarget{}
	seenTargets := map[string]struct{}{}
	for index, match := range matches {
		heading := strings.TrimSpace(listSection[match[2]:match[3]])
		if heading != wantHeadings[index] {
			return nil, ErrUnsupportedTable
		}
		end := len(listSection)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		targets, err := parseSekaipediaAuthorityTable(listSection[match[1]:end], heading)
		if err != nil {
			return nil, fmt.Errorf("Sekaipedia List %s: %w", heading, err)
		}
		for _, target := range targets {
			key := normalizeCatalogTitle(target.pageTitle)
			if key == "" {
				return nil, ErrUnsupportedTable
			}
			if _, duplicate := seenTargets[key]; duplicate {
				return nil, ErrAmbiguous
			}
			seenTargets[key] = struct{}{}
			result = append(result, target)
		}
	}
	if len(result) == 0 || len(result) > 1000 {
		return nil, ErrUnsupportedTable
	}
	return result, nil
}

func parseSekaipediaAuthorityTable(section, heading string) ([]sekaipediaListTarget, error) {
	open := strings.Index(section, "{| class=\"wikitable sortable\" style=\"width: 100%; text-align:center;\"")
	if open < 0 || strings.Contains(section[open+2:], "{| class=\"wikitable sortable\"") {
		return nil, ErrUnsupportedTable
	}
	closeRelative := strings.Index(section[open:], "\n|}")
	if closeRelative < 0 {
		return nil, ErrUnsupportedTable
	}
	close := open + closeRelative
	table := section[open : close+3]

	expectedHeaders := []string{
		"Song title", "Producer", "VIRTUAL SINGER ver. singers", "SEKAI ver. singers", "Unit",
	}
	switch heading {
	case "Pre-existing songs":
		expectedHeaders = append(expectedHeaders, "3DMV", "Date added", "Description")
	case "Commissioned songs":
		expectedHeaders = append(expectedHeaders, "3DPV", "Date added", "Description")
	case "Upcoming announced songs":
		expectedHeaders = append(expectedHeaders, "3DPV", "Expected release date", "Description")
	case "Removed songs":
		expectedHeaders = append(expectedHeaders, "Date released", "Date removed", "Reason")
	default:
		return nil, ErrUnsupportedTable
	}

	lines := strings.Split(strings.ReplaceAll(table, "\r", ""), "\n")
	headers := []string{}
	rows := [][]string{}
	var cells []string
	inRows := false
	flush := func() error {
		if cells == nil {
			return nil
		}
		if len(cells) == 7 && heading == "Pre-existing songs" && cells[0] == "[[Mousou Aspartame]]" {
			cells = append(cells, "")
		}
		if len(cells) != len(expectedHeaders) {
			return fmt.Errorf("%w: List row has %d cells", ErrUnsupportedTable, len(cells))
		}
		rows = append(rows, append([]string(nil), cells...))
		cells = nil
		return nil
	}
	for lineIndex, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "!") && !inRows:
			separator := strings.LastIndex(trimmed, "|")
			if separator < 0 || separator+1 >= len(trimmed) {
				return nil, ErrUnsupportedTable
			}
			headers = append(headers, strings.TrimSpace(trimmed[separator+1:]))
		case trimmed == "|-":
			// The fixed authority contains one exact duplicated separator after
			// Snow Fairy Story. Accept only that known empty boundary.
			if len(cells) == 0 && heading == "Pre-existing songs" && len(rows) > 0 &&
				rows[len(rows)-1][0] == "[[Snow Fairy Story]]" {
				continue
			}
			if err := flush(); err != nil {
				return nil, err
			}
			inRows = true
			cells = []string{}
		case strings.HasPrefix(line, "|") && trimmed != "|}":
			if !inRows || cells == nil {
				return nil, ErrUnsupportedTable
			}
			cells = append(cells, strings.TrimSpace(line[1:]))
		case trimmed == "|}":
			// The fixed Pre-existing table has one harmless trailing row
			// separator immediately before its close. Interior empty rows still
			// fail when the next separator runs through flush.
			if len(cells) == 0 {
				cells = nil
			} else if err := flush(); err != nil {
				return nil, err
			}
		case line == "Horie Shota (kemu)" && heading == "Commissioned songs" &&
			len(cells) == 2 && cells[0] == "[[88☆彡]]" && cells[1] == "marasy":
			cells[1] += "\n" + line
		case trimmed == "":
		default:
			return nil, fmt.Errorf("%w: invalid List table line %d", ErrUnsupportedTable, lineIndex+2)
		}
	}
	if len(headers) != len(expectedHeaders) {
		return nil, fmt.Errorf("%w: List has %d headers", ErrUnsupportedTable, len(headers))
	}
	for index := range headers {
		if headers[index] != expectedHeaders[index] {
			return nil, fmt.Errorf("%w: List header %d mismatch", ErrUnsupportedTable, index+1)
		}
	}
	if len(rows) == 0 {
		return nil, ErrUnsupportedTable
	}

	result := []sekaipediaListTarget{}
	for rowIndex, row := range rows {
		target, linked, err := parseSekaipediaListTitleCell(row[0])
		if err != nil {
			return nil, fmt.Errorf("%w: List title row %d", err, rowIndex+1)
		}
		if linked {
			result = append(result, target)
			continue
		}
		if heading != "Upcoming announced songs" || strings.TrimSpace(row[0]) == "" || strings.ContainsAny(row[0], "{}[]<>|") {
			return nil, fmt.Errorf("%w: unlinked List title row %d", ErrUnsupportedTable, rowIndex+1)
		}
	}
	return result, nil
}

func parseSekaipediaListTitleCell(value string) (sekaipediaListTarget, bool, error) {
	value = strings.TrimSpace(value)
	for _, suffix := range []string{"<nowiki/>", "<nowiki />"} {
		value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
	}
	if !strings.HasPrefix(value, "[[") || !strings.HasSuffix(value, "]]") {
		return sekaipediaListTarget{}, false, nil
	}
	fields, ok := splitTopLevelSekaipediaFields(value[2:len(value)-2], "|")
	if !ok || len(fields) < 1 || len(fields) > 2 {
		return sekaipediaListTarget{}, false, ErrUnsupportedTable
	}
	pageTitle := strings.TrimSpace(fields[0])
	display := pageTitle
	if len(fields) == 2 {
		display = strings.TrimSpace(fields[1])
	}
	if pageTitle == "" || display == "" || strings.ContainsAny(pageTitle, "#[]{}<>|") ||
		strings.ContainsAny(display, "[]{}<>|") {
		return sekaipediaListTarget{}, false, ErrUnsupportedTable
	}
	return sekaipediaListTarget{pageTitle: pageTitle, display: display}, true, nil
}

func selectSekaipediaListTarget(
	targets []sekaipediaListTarget,
	catalogTitle string,
) (sekaipediaListTarget, bool, error) {
	var result sekaipediaListTarget
	matches := 0
	for _, target := range targets {
		if titleFormMatches(target.pageTitle, catalogTitle) || titleFormMatches(target.display, catalogTitle) {
			result = target
			matches++
		}
	}
	if matches > 1 {
		return sekaipediaListTarget{}, false, ErrAmbiguous
	}
	return result, matches == 1, nil
}

func exactSekaipediaListTarget(
	targets []sekaipediaListTarget,
	pageTitle string,
) (sekaipediaListTarget, bool) {
	var result sekaipediaListTarget
	matches := 0
	for _, target := range targets {
		if target.pageTitle == pageTitle {
			result = target
			matches++
		}
	}
	return result, matches == 1
}
