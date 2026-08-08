package translator

import "testing"

func TestCatalogLyricsVersionRequiresExplicitSupportedEvidence(t *testing.T) {
	for name, test := range map[string]struct {
		music map[string]any
		value string
		known bool
	}{
		"boolean full":             {map[string]any{"isFullLength": true}, "full", true},
		"boolean game size":        {map[string]any{"isFullLength": false}, "game_size", true},
		"explicit full":            {map[string]any{"lyricsVersion": "full"}, "full", true},
		"explicit game":            {map[string]any{"versionType": "game-size"}, "game_size", true},
		"consistent full signals":  {map[string]any{"isFullLength": true, "musicVersion": "long", "lyricsVersion": "full-length"}, "full", true},
		"consistent game signals":  {map[string]any{"isFullLength": false, "musicVersion": "game", "versionType": "game_size"}, "game_size", true},
		"full versus game":         {map[string]any{"isFullLength": true, "lyricsVersion": "game-size"}, "unknown", true},
		"game versus full":         {map[string]any{"isFullLength": false, "lyricsVersion": "full"}, "unknown", true},
		"short excluded":           {map[string]any{"musicVersion": "short"}, "unknown", true},
		"full plus short":          {map[string]any{"isFullLength": true, "musicVersion": "short"}, "unknown", true},
		"full plus preview":        {map[string]any{"isFullLength": true, "musicVersion": "preview"}, "unknown", true},
		"full plus partial":        {map[string]any{"isFullLength": true, "lyricsVersion": "partial"}, "unknown", true},
		"full plus cover":          {map[string]any{"isFullLength": true, "versionType": "cover"}, "unknown", true},
		"full plus medley":         {map[string]any{"isFullLength": true, "musicVersion": "medley"}, "unknown", true},
		"game plus excluded":       {map[string]any{"isFullLength": false, "musicVersion": "short-version"}, "unknown", true},
		"unknown explicit value":   {map[string]any{"musicVersion": "complete"}, "unknown", true},
		"empty explicit value":     {map[string]any{"musicVersion": ""}, "unknown", true},
		"invalid boolean value":    {map[string]any{"isFullLength": "true"}, "unknown", true},
		"invalid boolean conflict": {map[string]any{"isFullLength": "true", "musicVersion": "full"}, "unknown", true},
		"missing unknown":          {map[string]any{}, "unknown", false},
	} {
		t.Run(name, func(t *testing.T) {
			value, known := catalogLyricsVersion(test.music)
			if value != test.value || known != test.known {
				t.Fatalf("catalog version=(%q,%t) want=(%q,%t)", value, known, test.value, test.known)
			}
		})
	}
}
