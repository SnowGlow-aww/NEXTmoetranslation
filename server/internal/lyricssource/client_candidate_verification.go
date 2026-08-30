package lyricssource

import (
	"fmt"
	"html"

	"net/url"

	"sort"
	"strings"

	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func canonicalURL(title string, revisionID int) string {
	canonical := url.URL{
		Scheme: "https",
		Host:   "vocaloid.fandom.com",
		Path:   "/wiki/" + strings.ReplaceAll(title, " ", "_"),
	}
	if revisionID > 0 {
		query := canonical.Query()
		query.Set("oldid", fmt.Sprintf("%d", revisionID))
		canonical.RawQuery = query.Encode()
	}
	return canonical.String()
}

func normalizeTitle(value string) string {
	var result strings.Builder
	for _, r := range strings.ToLower(norm.NFKC.String(html.UnescapeString(value))) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			result.WriteRune(r)
		}
	}
	return result.String()
}

type candidateVerificationOutcome uint8

const (
	candidateVerified candidateVerificationOutcome = iota
	candidateTitleMismatch
	candidateCreditMismatch
	candidateSignalMismatch
)

func verifyCandidate(identity MusicIdentity, title, content string, categories []string) bool {
	return candidateVerification(identity, title, content, categories) == candidateVerified
}

func candidateVerification(identity MusicIdentity, title, content string, categories []string) candidateVerificationOutcome {
	if hasWrongEntityEvidence(categories) {
		return candidateSignalMismatch
	}
	creditCorpus := strings.ToLower(content + "\n" + strings.Join(categories, "\n"))
	if !candidateTitleMatches(title, identity.JapaneseTitle) {
		return candidateTitleMismatch
	}
	if identity.Lyricist != "" || identity.Composer != "" || identity.Arranger != "" {
		if !roleBoundCreditsMatch(identity, content) {
			return candidateCreditMismatch
		}
	} else {
		producerCredits := identityFields(identity.ProducerMetadata)
		if len(producerCredits) == 0 {
			return candidateCreditMismatch
		}
		for _, credit := range producerCredits {
			if !containsIdentityField(creditCorpus, credit, true) {
				return candidateCreditMismatch
			}
		}
	}
	if hasSongSignal(identity.JapaneseTitle, content, categories) {
		return candidateVerified
	}
	return candidateSignalMismatch
}

type creditRole string

const (
	creditRoleLyricist creditRole = "lyricist"
	creditRoleComposer creditRole = "composer"
	creditRoleArranger creditRole = "arranger"
)

func roleBoundCreditsMatch(identity MusicIdentity, content string) bool {
	scopes := wikiRoleCreditScopes(identity, content)
	if len(scopes) == 0 {
		return false
	}
	// Every matching same-title song-box scope must independently establish the
	// exact role sets. Accepting any one scope would let a conflicting duplicate
	// box hide extra or different contributors.
	for _, credits := range scopes {
		if !roleCreditScopeMatches(identity, credits) {
			return false
		}
	}
	return true
}

func roleCreditScopeMatches(identity MusicIdentity, credits map[creditRole][]string) bool {
	matchedRoles := 0
	for _, expected := range roleBoundCreditExpectations(identity) {
		if strings.TrimSpace(expected.credit) == "" {
			continue
		}
		expectedSet, ok := contributorSet(expected.credit)
		if !ok {
			return false
		}
		values := credits[expected.role]
		if len(values) == 0 {
			continue
		}
		actualSet, ok := roleCreditContributorSet(values)
		if !ok || !contributorSetsEqual(expectedSet, actualSet) {
			// Missing roles may be corroborated by another exact authoritative
			// role, but every explicit contributor set remains mandatory.
			return false
		}
		matchedRoles++
	}
	return matchedRoles > 0
}

func contributorSet(value string) (map[string]string, bool) {
	contributors, ok := splitTopLevelContributors(value)
	if !ok {
		return nil, false
	}
	result := make(map[string]string, len(contributors))
	for _, contributor := range contributors {
		key := normalizeTitle(contributor)
		if key == "" {
			return nil, false
		}
		if _, found := result[key]; !found {
			result[key] = contributor
		}
	}
	return result, len(result) > 0
}

func roleCreditContributorSet(values []string) (map[string]string, bool) {
	result := map[string]string{}
	for _, value := range values {
		contributors, ok := splitTopLevelContributors(identityDisplayText(value))
		if !ok {
			return nil, false
		}
		for _, contributor := range contributors {
			key := normalizeTitle(contributor)
			if key == "" {
				return nil, false
			}
			if _, found := result[key]; !found {
				result[key] = contributor
			}
		}
	}
	return result, true
}

func contributorSetsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, found := right[key]; !found {
			return false
		}
	}
	return true
}

type roleBoundCreditExpectation struct {
	role   creditRole
	credit string
}

func roleBoundCreditExpectations(identity MusicIdentity) []roleBoundCreditExpectation {
	return []roleBoundCreditExpectation{
		{creditRoleLyricist, identity.Lyricist},
		{creditRoleComposer, identity.Composer},
		{creditRoleArranger, identity.Arranger},
	}
}

type roleCreditFailure struct {
	role    creditRole
	missing bool
}

func addCreditDiagnostics(diagnostics *SearchDiagnostics, identity MusicIdentity, content string) {
	scopes := wikiRoleCreditScopes(identity, content)
	if len(scopes) == 0 {
		scopes = []map[creditRole][]string{nil}
	}
	failuresByRole := map[creditRole]roleCreditFailure{}
	for _, credits := range scopes {
		for _, failure := range roleCreditScopeFailures(identity, credits) {
			previous, found := failuresByRole[failure.role]
			if !found || previous.missing && !failure.missing {
				// A concrete conflict is more informative than a missing value.
				failuresByRole[failure.role] = failure
			}
		}
	}
	for _, expected := range roleBoundCreditExpectations(identity) {
		failure, found := failuresByRole[expected.role]
		if !found {
			continue
		}
		switch failure.role {
		case creditRoleLyricist:
			if failure.missing {
				diagnostics.LyricistCreditMissing++
			} else {
				diagnostics.LyricistCreditMismatch++
			}
		case creditRoleComposer:
			if failure.missing {
				diagnostics.ComposerCreditMissing++
			} else {
				diagnostics.ComposerCreditMismatch++
			}
		case creditRoleArranger:
			if failure.missing {
				diagnostics.ArrangerCreditMissing++
			} else {
				diagnostics.ArrangerCreditMismatch++
			}
		}
	}
}

func roleCreditScopeFailures(identity MusicIdentity, credits map[creditRole][]string) []roleCreditFailure {
	missing := make([]roleCreditFailure, 0, 3)
	mismatched := make([]roleCreditFailure, 0, 3)
	matchedRoles := 0
	for _, expected := range roleBoundCreditExpectations(identity) {
		wanted := strings.TrimSpace(expected.credit)
		if wanted == "" {
			continue
		}
		values := credits[expected.role]
		if len(values) == 0 {
			missing = append(missing, roleCreditFailure{role: expected.role, missing: true})
			continue
		}
		expectedSet, expectedOK := contributorSet(wanted)
		actualSet, actualOK := roleCreditContributorSet(values)
		if expectedOK && actualOK && contributorSetsEqual(expectedSet, actualSet) {
			matchedRoles++
			continue
		}
		mismatched = append(mismatched, roleCreditFailure{role: expected.role})
	}
	if len(mismatched) > 0 {
		return mismatched
	}
	if matchedRoles > 0 {
		return nil
	}
	return missing
}

func wikiRoleCreditScopes(identity MusicIdentity, content string) []map[creditRole][]string {
	metadata := primaryPageMetadata(content)
	blocks := wikiSongBoxMetadataBlocks(metadata)
	if len(blocks) == 0 {
		return []map[creditRole][]string{wikiRoleCreditsFromMetadata(metadata)}
	}
	scopes := make([]map[creditRole][]string, 0, len(blocks))
	hasParsedTitle := false
	for _, block := range blocks {
		title, ok := wikiSongBoxTitle(block)
		if !ok {
			continue
		}
		hasParsedTitle = true
		if titleFormMatches(title, identity.JapaneseTitle) {
			scopes = append(scopes, wikiRoleCreditsFromMetadata(block))
		}
	}
	if len(scopes) > 0 {
		return scopes
	}
	if len(blocks) == 1 {
		return []map[creditRole][]string{wikiRoleCreditsFromMetadata(blocks[0])}
	}
	if hasParsedTitle {
		return nil
	}
	return []map[creditRole][]string{wikiRoleCreditsFromMetadata(metadata)}
}

func wikiSongBoxMetadataBlocks(content string) []string {
	content = inactiveRestrictionMarkupPattern.ReplaceAllString(content, "")
	starts := wikiSongBoxStartPattern.FindAllStringIndex(content, -1)
	blocks := make([]string, 0, len(starts))
	for _, start := range starts {
		depth := 0
		for index := start[0]; index+1 < len(content); {
			switch content[index : index+2] {
			case "{{":
				depth++
				index += 2
			case "}}":
				depth--
				index += 2
				if depth == 0 {
					blocks = append(blocks, content[start[0]:index])
					index = len(content)
				}
			default:
				_, size := utf8.DecodeRuneInString(content[index:])
				index += size
			}
		}
	}
	return blocks
}

func wikiSongBoxTitle(content string) (string, bool) {
	match := wikiSongBoxTitlePattern.FindStringSubmatch(content)
	if match == nil {
		return "", false
	}
	title := strings.TrimSpace(identityDisplayText(match[1]))
	return title, normalizeTitle(title) != ""
}

func wikiRoleCredits(content string) map[creditRole][]string {
	return wikiRoleCreditsFromMetadata(primaryPageMetadata(content))
}

func wikiRoleCreditsFromMetadata(content string) map[creditRole][]string {
	content = wikiBreakPattern.ReplaceAllString(content, "\n")
	result := map[creditRole][]string{}
	var pending creditRole
	producerBlock := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(content, "\r", ""), "\n") {
		line := strings.TrimSpace(strings.ReplaceAll(rawLine, "'''", ""))
		line = strings.TrimSpace(strings.ReplaceAll(line, "''", ""))
		if line == "" {
			pending = ""
			producerBlock = false
			continue
		}
		if role, value, ok := parseWikiRoleCredit(line); ok {
			result[role] = append(result[role], value)
			pending = ""
			producerBlock = false
			continue
		}
		if value, ok := parseWikiProducerAssignment(line); ok {
			appendAnnotatedProducerCredits(result, value)
			pending = ""
			producerBlock = true
			continue
		}
		if producerBlock && strings.HasPrefix(line, "*") {
			appendAnnotatedProducerCredits(result, strings.TrimSpace(strings.TrimPrefix(line, "*")))
			continue
		}
		producerBlock = false
		if role, ok := parseWikiRoleLabel(line); ok {
			pending = role
			continue
		}
		if pending != "" && strings.HasPrefix(line, "|") {
			value := strings.TrimSpace(strings.TrimLeft(line, "|"))
			if value != "" && !strings.HasPrefix(value, "-") {
				result[pending] = append(result[pending], value)
			}
		}
		pending = ""
	}
	return result
}

func parseWikiProducerAssignment(line string) (string, bool) {
	match := wikiProducerAssignmentPattern.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func appendAnnotatedProducerCredits(result map[creditRole][]string, value string) {
	nameStart := 0
	for scan := 0; scan < len(value); {
		open := strings.IndexByte(value[scan:], '(')
		if open < 0 {
			return
		}
		open += scan
		closeOffset := strings.IndexByte(value[open+1:], ')')
		if closeOffset < 0 {
			return
		}
		closeIndex := open + 1 + closeOffset
		roles := make([]creditRole, 0, 3)
		for _, label := range strings.FieldsFunc(value[open+1:closeIndex], func(r rune) bool {
			return strings.ContainsRune(",，/／;&＆+＋", r)
		}) {
			if role, ok := annotatedProducerRole(label); ok {
				roles = append(roles, role)
			}
		}
		if len(roles) > 0 {
			name := strings.TrimSpace(strings.TrimLeft(value[nameStart:open], ",;；，|"))
			if name != "" {
				for _, role := range roles {
					result[role] = append(result[role], name)
				}
			}
			nameStart = closeIndex + 1
		}
		scan = closeIndex + 1
	}
}

func annotatedProducerRole(label string) (creditRole, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "lyrics", "lyric", "words", "written":
		return creditRoleLyricist, true
	case "music", "composition", "composed":
		return creditRoleComposer, true
	case "arrangement", "arrange", "arranged":
		return creditRoleArranger, true
	default:
		return "", false
	}
}

func parseWikiRoleCredit(line string) (creditRole, string, bool) {
	if match := wikiRoleAssignmentPattern.FindStringSubmatch(line); match != nil {
		role, ok := wikiCreditRole(match[1])
		value := strings.TrimSpace(match[2])
		return role, value, ok && value != ""
	}
	if match := wikiRoleByPattern.FindStringSubmatch(line); match != nil {
		role, ok := wikiCreditRole(match[1])
		value := strings.TrimSpace(match[2])
		return role, value, ok && value != ""
	}
	return "", "", false
}

func parseWikiRoleLabel(line string) (creditRole, bool) {
	match := wikiRoleLabelPattern.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}
	return wikiCreditRole(match[1])
}

func wikiCreditRole(label string) (creditRole, bool) {
	label = strings.ToLower(strings.TrimSpace(label))
	switch label {
	case "lyrics", "lyricist", "words", "written", "written by", "作詞":
		return creditRoleLyricist, true
	case "music", "composer", "composition", "composed", "composed by", "作曲":
		return creditRoleComposer, true
	case "arrangement", "arranger", "arranged", "arranged by", "編曲":
		return creditRoleArranger, true
	default:
		return "", false
	}
}

func identityDisplayText(value string) string {
	value = markupPattern.ReplaceAllString(value, "")
	for range 4 {
		changed := false
		value = simpleIdentityTemplatePattern.ReplaceAllStringFunc(value, func(template string) string {
			changed = true
			parts := strings.Split(template[2:len(template)-2], "|")
			for index := len(parts) - 1; index >= 1; index-- {
				part := strings.TrimSpace(parts[index])
				if part == "" || strings.Contains(part, "=") {
					continue
				}
				return part
			}
			return ""
		})
		if !changed {
			break
		}
	}
	value = linkPattern.ReplaceAllString(value, "$1")
	value = strings.ReplaceAll(value, "'''", "")
	return strings.TrimSpace(strings.ReplaceAll(value, "''", ""))
}

func splitTopLevelContributors(value string) ([]string, bool) {
	value = strings.TrimSpace(norm.NFKC.String(html.UnescapeString(value)))
	value = spacedContributorXPattern.ReplaceAllString(value, " & ")
	if value == "" {
		return nil, false
	}
	contributors := []string{}
	closers := []rune{}
	start := 0
	for index, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return nil, false
		}
		if closer, ok := contributorGroupCloser(r); ok {
			closers = append(closers, closer)
			continue
		}
		if isContributorGroupCloser(r) {
			if len(closers) == 0 || closers[len(closers)-1] != r {
				return nil, false
			}
			closers = closers[:len(closers)-1]
			continue
		}
		if len(closers) == 0 && isContributorSeparator(r) {
			contributor := strings.TrimSpace(value[start:index])
			if contributor == "" || normalizeTitle(contributor) == "" {
				return nil, false
			}
			contributors = append(contributors, contributor)
			start = index + utf8.RuneLen(r)
		}
	}
	if len(closers) != 0 {
		return nil, false
	}
	contributor := strings.TrimSpace(value[start:])
	if contributor == "" || normalizeTitle(contributor) == "" {
		return nil, false
	}
	contributors = append(contributors, contributor)
	seen := make(map[string]struct{}, len(contributors))
	unique := contributors[:0]
	for _, contributor := range contributors {
		key := normalizeTitle(contributor)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, contributor)
	}
	return unique, true
}

func contributorGroupCloser(r rune) (rune, bool) {
	switch r {
	case '(':
		return ')', true
	case '[':
		return ']', true
	case '{':
		return '}', true
	case '<':
		return '>', true
	case '「':
		return '」', true
	case '『':
		return '』', true
	case '【':
		return '】', true
	case '〈':
		return '〉', true
	case '《':
		return '》', true
	default:
		return 0, false
	}
}

func isContributorGroupCloser(r rune) bool {
	return strings.ContainsRune(")]}>」』】〉》", r)
}

func isContributorSeparator(r rune) bool {
	return strings.ContainsRune("&/,;+|×、،؛", r)
}

func unresolvedRoleCredits(identity MusicIdentity, content string) ([]roleBoundCreditExpectation, bool) {
	scopes := wikiRoleCreditScopes(identity, content)
	if len(scopes) == 0 {
		return nil, false
	}
	unresolvedByKey := map[string]roleBoundCreditExpectation{}
	for _, credits := range scopes {
		unresolved, resolvable := unresolvedRoleCreditScope(identity, credits)
		if !resolvable {
			return nil, false
		}
		for _, expected := range unresolved {
			unresolvedByKey[creditAliasKey(expected.role, expected.credit)] = expected
		}
	}
	keys := make([]string, 0, len(unresolvedByKey))
	for key := range unresolvedByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	unresolved := make([]roleBoundCreditExpectation, 0, len(keys))
	for _, key := range keys {
		unresolved = append(unresolved, unresolvedByKey[key])
	}
	return unresolved, true
}

func unresolvedRoleCreditScope(identity MusicIdentity, credits map[creditRole][]string) ([]roleBoundCreditExpectation, bool) {
	unresolved := make([]roleBoundCreditExpectation, 0, 3)
	observedRoles := 0
	for _, expected := range roleBoundCreditExpectations(identity) {
		if strings.TrimSpace(expected.credit) == "" {
			continue
		}
		expectedSet, ok := contributorSet(expected.credit)
		if !ok {
			return nil, false
		}
		values := credits[expected.role]
		if len(values) == 0 {
			continue
		}
		observedRoles++
		actualSet, ok := roleCreditContributorSet(values)
		if !ok || len(actualSet) != len(expectedSet) {
			return nil, false
		}
		for key, contributor := range expectedSet {
			if _, found := actualSet[key]; !found {
				unresolved = append(unresolved, roleBoundCreditExpectation{role: expected.role, credit: contributor})
			}
		}
	}
	return unresolved, observedRoles > 0
}

func roleBoundCreditsMatchWithAliases(identity MusicIdentity, content string, aliases map[string]string) bool {
	scopes := wikiRoleCreditScopes(identity, content)
	if len(scopes) == 0 {
		return false
	}
	for _, credits := range scopes {
		if !roleCreditScopeMatchesWithAliases(identity, credits, aliases) {
			return false
		}
	}
	return true
}

func roleCreditScopeMatchesWithAliases(identity MusicIdentity, credits map[creditRole][]string, aliases map[string]string) bool {
	matchedRoles := 0
	for _, expected := range roleBoundCreditExpectations(identity) {
		if strings.TrimSpace(expected.credit) == "" {
			continue
		}
		expectedSet, ok := contributorSet(expected.credit)
		if !ok {
			return false
		}
		values := credits[expected.role]
		if len(values) == 0 {
			continue
		}
		actualSet, ok := roleCreditContributorSet(values)
		if !ok || len(actualSet) != len(expectedSet) {
			return false
		}
		matchedActual := map[string]struct{}{}
		for expectedKey, contributor := range expectedSet {
			actualKey := expectedKey
			if _, found := actualSet[actualKey]; !found {
				canonical, found := aliases[creditAliasKey(expected.role, contributor)]
				if !found {
					return false
				}
				actualKey = normalizeTitle(canonical)
				if actualKey == "" {
					return false
				}
				if _, found := actualSet[actualKey]; !found {
					return false
				}
			}
			if _, duplicate := matchedActual[actualKey]; duplicate {
				return false
			}
			matchedActual[actualKey] = struct{}{}
		}
		if len(matchedActual) != len(actualSet) {
			return false
		}
		matchedRoles++
	}
	return matchedRoles > 0
}

func creditAliasKey(role creditRole, value string) string {
	return string(role) + "\x00" + normalizeTitle(value)
}
