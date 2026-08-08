package lyricssource

import (
	"sort"
	"strconv"
)

// recoveryExactCatalogIdentityMatches applies only after an immutable recovery
// plan selected one exact List title for the catalog music ID. The acquired
// page/revision remains bound by exact ledger evidence; the page wikitext must
// independently repeat the same music ID in its sole Infobox song template.
// Ordinary discovery never enters this authority path and retains strict
// title-and-credit matching.
func (provider *sekaipediaProvider) recoveryExactCatalogIdentityMatches(
	content string,
	pageTitle string,
	identity MusicIdentity,
) bool {
	if provider == nil || !provider.config.RecoveryExactCapture || identity.MusicID <= 0 ||
		len(provider.config.SekaipediaTargets) == 0 {
		return false
	}
	targets := provider.config.SekaipediaTargets
	index := sort.Search(len(targets), func(index int) bool {
		return targets[index].MusicID >= identity.MusicID
	})
	if index == len(targets) || targets[index].MusicID != identity.MusicID {
		return false
	}
	target := targets[index]
	expectedTitle := target.ResolvedPageTitle
	if expectedTitle == "" {
		expectedTitle = target.PageTitle
	}
	if pageTitle != expectedTitle {
		return false
	}
	params, err := parseSekaipediaInfoboxSongParams(content)
	if err != nil {
		return false
	}
	musicID, err := strconv.Atoi(params["song id"])
	return err == nil && musicID == identity.MusicID
}
