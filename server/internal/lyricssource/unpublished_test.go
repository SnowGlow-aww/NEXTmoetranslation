package lyricssource

import (
	"errors"
	"os"
	"testing"
)

func TestExplicitOmittedLyricsIsDeterministicUnpublishedEvidence(t *testing.T) {
	content := "== Lyrics ==\n{{OmittedLyrics}}\n\n== Derivatives ==\n{{Lyrics|must not leak}}"
	if !hasExplicitUnpublishedLyrics(content) {
		t.Fatal("exact omitted-lyrics section was not recognized")
	}
	if _, err := extractCategoryAwareLyrics(content, []string{"Japanese songs"}); !errors.Is(err, ErrLyricsUnpublished) {
		t.Fatalf("error = %v, want %v", err, ErrLyricsUnpublished)
	}
}

func TestMindBrandFixedRevisionIsExplicitlyUnpublished(t *testing.T) {
	content, err := os.ReadFile("testdata/mind-brand-1492103.wiki")
	if err != nil {
		t.Fatal(err)
	}
	if !hasExplicitUnpublishedLyrics(string(content)) {
		t.Fatal("fixed Mind Brand revision did not retain exact OmittedLyrics evidence")
	}
	if _, err := extractCategoryAwareLyrics(string(content), []string{"Japanese songs"}); !errors.Is(err, ErrLyricsUnpublished) {
		t.Fatalf("error = %v, want %v", err, ErrLyricsUnpublished)
	}
}

func TestUnpublishedEvidenceStaysSectionBoundAndExact(t *testing.T) {
	for name, content := range map[string]string{
		"derivative only":          "== Lyrics ==\n歌う\n== Derivatives ==\n{{OmittedLyrics}}",
		"template with arguments":  "== Lyrics ==\n{{OmittedLyrics|copied text}}",
		"surrounding lyric text":   "== Lyrics ==\n歌う\n{{OmittedLyrics}}",
		"similar unknown template": "== Lyrics ==\n{{OmittedLyric}}",
	} {
		t.Run(name, func(t *testing.T) {
			if hasExplicitUnpublishedLyrics(content) {
				t.Fatal("unsafe unpublished evidence was accepted")
			}
		})
	}
}
