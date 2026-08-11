package lyricssource

import "testing"

func TestSekaipediaRecoveryExactTargetAndInfoboxSongIDAreAuthoritative(t *testing.T) {
	content := sekaipediaFixturePageContent(t, "Roki")
	identity := rokiSekaipediaIdentity()
	identity.JapaneseTitle = "catalog title deliberately differs"
	identity.ProducerMetadata = "catalog credits deliberately differ"
	identity.Lyricist = "different lyricist"
	identity.Composer = "different composer"
	identity.Arranger = "different arranger"

	ordinary := newSekaipediaProvider(historicalSekaipediaProviderConfig(), nil)
	if ordinary.catalogIdentityMatches(content, "Roki", identity) {
		t.Fatal("ordinary discovery accepted recovery-only exact identity authority")
	}

	recoveryConfig := historicalSekaipediaProviderConfig()
	recoveryConfig.RecoveryExactCapture = true
	recovery := newSekaipediaProvider(recoveryConfig, nil)
	if !recovery.catalogIdentityMatches(content, "Roki", identity) {
		t.Fatal("recovery exact target plus exact Infobox song ID was rejected")
	}

	wrongPage := identity
	if recovery.catalogIdentityMatches(content, "Journey", wrongPage) {
		t.Fatal("recovery exact identity accepted a different resolved page title")
	}
	wrongMusic := identity
	wrongMusic.MusicID++
	if recovery.catalogIdentityMatches(content, "Roki", wrongMusic) {
		t.Fatal("recovery exact identity accepted a different catalog music ID")
	}
}

func TestSekaipediaRecoveryExactIdentityRequiresPlanTarget(t *testing.T) {
	config := historicalSekaipediaProviderConfig()
	config.RecoveryExactCapture = true
	config.SekaipediaTargets = nil
	provider := newSekaipediaProvider(config, nil)
	identity := rokiSekaipediaIdentity()
	identity.JapaneseTitle = "different"
	identity.ProducerMetadata = "different"
	identity.Lyricist = "different"
	identity.Composer = "different"
	identity.Arranger = "different"
	if provider.catalogIdentityMatches(sekaipediaFixturePageContent(t, "Roki"), "Roki", identity) {
		t.Fatal("recovery exact identity guessed authority without a plan target")
	}
}
