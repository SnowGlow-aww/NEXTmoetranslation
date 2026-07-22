package lyricssource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExtractLyricsPreservesOrderAndStanzas(t *testing.T) {
	lines, err := extractLyrics("intro\n== Lyrics ==\n歌う\n\n踊る\n== Other ==\nignored")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Japanese != "歌う" || lines[0].StanzaBreakBefore ||
		lines[1].Japanese != "踊る" || !lines[1].StanzaBreakBefore {
		t.Fatalf("extracted lines = %+v", lines)
	}
}

func TestExtractLyricsRejectsRestrictedAndAmbiguousMarkup(t *testing.T) {
	if _, err := extractLyrics("== Lyrics ==\n歌う\n無断転載禁止"); !errors.Is(err, ErrRestrictedReprint) {
		t.Fatalf("restricted source error = %v", err)
	}
	if _, err := extractLyrics("== Lyrics ==\n歌う\n{{No reprint}}"); !errors.Is(err, ErrRestrictedReprint) {
		t.Fatalf("No reprint template error = %v", err)
	}
	if !hasReprintRestriction("== Lyrics ==\n歌う", []string{"Songs with reprints prohibited"}) {
		t.Fatal("restriction category was not detected")
	}
	if _, err := extractLyrics("== Lyrics ==\n{|\n| 歌う\n|}\n{|\n| 踊る\n|}"); !errors.Is(err, ErrUnsupportedTable) {
		t.Fatalf("ambiguous table error = %v", err)
	}
}

func TestPreviewRejectsRevisionFromAnotherPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"query":{"pages":{"999":{"pageid":999,"title":"新曲","categories":[{"title":"Category:Lyrics"}],"revisions":[{"revid":34,"sha1":"sha","slots":{"main":{"content":"作者 original song\n== Lyrics ==\n歌う"}}}]}}}}`)
	}))
	defer server.Close()
	client := New()
	client.endpoint = server.URL
	_, err := client.Preview(context.Background(), MusicIdentity{MusicID: 1, JapaneseTitle: "新曲", ProducerMetadata: "作者"}, 12, 34)
	if !errors.Is(err, ErrRevisionChanged) {
		t.Fatalf("cross-page revision error = %v", err)
	}
}

func TestRequestRejectsCrossOriginRedirect(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client := New()
	client.endpoint = source.URL
	_, err := client.Search(context.Background(), MusicIdentity{JapaneseTitle: "新曲", ProducerMetadata: "作者"})
	if err == nil || targetRequests.Load() != 0 {
		t.Fatalf("cross-origin redirect err=%v targetRequests=%d", err, targetRequests.Load())
	}
}

func TestCandidateRequiresTitleProducerAndSongSignal(t *testing.T) {
	identity := MusicIdentity{MusicID: 10, JapaneseTitle: "新曲", ProducerMetadata: "作者"}
	if !verifyCandidate(identity, "新曲", "作者による original song の Lyrics", nil) {
		t.Fatal("verified source candidate was rejected")
	}
	if verifyCandidate(identity, "新曲", "別人による Lyrics", nil) {
		t.Fatal("candidate without producer identity was accepted")
	}
}
