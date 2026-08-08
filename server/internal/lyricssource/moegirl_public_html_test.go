package lyricssource

import (
	"strings"
	"testing"
)

const testMoegirlPublicPageURL = "https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B"

func TestParseMoegirlPublicPageHTMLExtractsNoRubyJapaneseTranslationPairs(t *testing.T) {
	raw := []byte(`<!doctype html><html><head><title>一億年恋してる - 萌娘百科 万物皆可萌的百科全书</title>` +
		`<script>RLCONF={"wgPageName":"亿年爱恋","wgTitle":"亿年爱恋","wgCurRevisionId":8500224,"wgArticleId":649688,"wgIsArticle":true,"wgIsRedirect":false};</script>` +
		`<meta property="og:url" content="` + testMoegirlPublicPageURL + `"></head><body>` +
		moegirlPublicLyricsRightsNotice +
		`<div class="Lyrics Lyrics-no-ruby Lyrics-has-translate" style="">` +
		`<div class="Lyrics-line"><div class="Lyrics-original" style="color:#000"><span lang="ja">一行目</span></div><div class="Lyrics-translated" style="color:#000"><span lang="zh">第一行</span></div></div>` +
		`<div class="Lyrics-line"><div class="Lyrics-original" style="color:#000"><span lang="ja"></span></div><div class="Lyrics-translated" style="color:#000"><span lang="zh"></span></div></div>` +
		`<div class="Lyrics-line"><div class="Lyrics-original" style="color:#000"><span lang="ja">二行目&amp;</span></div><div class="Lyrics-translated" style="color:#000"><span lang="zh">第二行&amp;</span></div></div>` +
		`<div style="clear:both"></div></div>` + "\n" +
		`<div class="mw-heading mw-heading2"><h2 id="注释与外部链接"></h2></div></body></html>`)

	extraction, err := ParseMoegirlPublicPageHTML(raw, testMoegirlPublicPageURL)
	if err != nil {
		t.Fatal(err)
	}
	if extraction.PageTitle != "亿年爱恋" || extraction.JapaneseTitle != "一億年恋してる" ||
		extraction.PageID != 649688 || extraction.RevisionID != 8500224 || len(extraction.Lines) != 2 ||
		extraction.Lines[0].Japanese != "一行目" || extraction.Lines[0].Translation != "第一行" ||
		extraction.Lines[1].Japanese != "二行目&" || extraction.Lines[1].Translation != "第二行&" ||
		!extraction.Lines[1].StanzaBreakBefore {
		t.Fatalf("public HTML extraction=%+v", extraction)
	}
}

func TestParseMoegirlPublicPageHTMLFailsClosedOnUnknownLyricsMarkup(t *testing.T) {
	valid := `<!doctype html><html><head><title>一億年恋してる - 萌娘百科 万物皆可萌的百科全书</title>` +
		`<script>RLCONF={"wgPageName":"亿年爱恋","wgTitle":"亿年爱恋","wgCurRevisionId":1,"wgArticleId":2,"wgIsArticle":true,"wgIsRedirect":false};</script>` +
		`<meta property="og:url" content="` + testMoegirlPublicPageURL + `"></head><body>` +
		moegirlPublicLyricsRightsNotice +
		`<div class="Lyrics Lyrics-no-ruby Lyrics-has-translate" style="">` +
		`<div class="Lyrics-line"><div class="Lyrics-original"><span lang="ja">歌</span></div><div class="Lyrics-translated"><span lang="zh">译</span></div></div>` +
		`<div style="clear:both"></div></div>` + "\n" +
		`<div class="mw-heading mw-heading2"><h2 id="注释与外部链接"></h2></div></body></html>`
	for name, body := range map[string]string{
		"ruby markup":         strings.Replace(valid, "<span lang=\"ja\">歌</span>", "<span lang=\"ja\"><ruby>歌</ruby></span>", 1),
		"missing translation": strings.Replace(valid, "<span lang=\"zh\">译</span>", "<span lang=\"zh\"></span>", 1),
		"changed URL":         strings.Replace(valid, testMoegirlPublicPageURL, testMoegirlPublicPageURL+"x", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMoegirlPublicPageHTML([]byte(body), testMoegirlPublicPageURL); err == nil {
				t.Fatal("changed public HTML was accepted")
			}
		})
	}
}
