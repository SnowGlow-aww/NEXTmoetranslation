package lyricssource

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestStructuredLanguageRolesSelectSourceForMusicTabs(t *testing.T) {
	tests := []struct {
		musicID int
		name    string
		labels  []string
		want    string
		wantErr error
	}{
		{musicID: 41001, name: "Chinese label", labels: []string{"Japanese", "Chinese"}, want: "Japanese"},
		{musicID: 41002, name: "Mandarin label", labels: []string{"Japanese lyrics", "Mandarin"}, want: "Japanese lyrics"},
		{musicID: 41003, name: "Chinese native label", labels: []string{"日本語", "中文"}, want: "日本語"},
		{musicID: 41004, name: "Chinese lyrics native label", labels: []string{"日本語歌詞", "中文歌词"}, want: "日本語歌詞"},
		{musicID: 41005, name: "Pinyin label", labels: []string{"Japanese", "Pinyin"}, want: "Japanese"},
		{
			musicID: 41006,
			name:    "approved English and Chinese translations",
			labels:  []string{"Japanese", "Approved English", "Approved Chinese"},
			want:    "Japanese",
		},
		{musicID: 41007, name: "Korean translation", labels: []string{"Japanese", "Korean Translation"}, want: "Japanese"},
		{musicID: 41008, name: "Korean native label", labels: []string{"Japanese", "한국어 가사"}, want: "Japanese"},
		{musicID: 41009, name: "Spanish translation", labels: []string{"Japanese", "Spanish Translation"}, want: "Japanese"},
		{musicID: 41010, name: "Spanish native label", labels: []string{"Japanese", "Español"}, want: "Japanese"},
		{musicID: 41015, name: "parenthesized translation labels", labels: []string{"Japanese", "Chinese (中文歌词)", "Korean (한국어)", "Spanish (Español)"}, want: "Japanese"},
		{musicID: 41016, name: "parenthesized Japanese label", labels: []string{"Japanese (日本語歌詞)", "Chinese"}, want: "Japanese (日本語歌詞)"},
		{musicID: 41011, name: "English original single block", labels: []string{"English lyrics"}, want: "English lyrics"},
		{
			musicID: 41012,
			name:    "translation tabs without source",
			labels:  []string{"English Translation", "Approved Chinese", "Korean", "Spanish"},
			wantErr: ErrMissingLyrics,
		},
		{
			musicID: 41013,
			name:    "two genuine source versions",
			labels:  []string{"Radio edit", "Single edit"},
			wantErr: ErrAmbiguous,
		},
		{
			musicID: 41014,
			name:    "English original remains source beside source version",
			labels:  []string{"English", "Acoustic Version"},
			wantErr: ErrAmbiguous,
		},
		{musicID: 41017, name: "sole English translation is not source", labels: []string{"English Translation"}, wantErr: ErrMissingLyrics},
		{musicID: 41018, name: "English source beside Japanese translation", labels: []string{"English", "Japanese Translation"}, want: "English"},
		{musicID: 41019, name: "sole generic translation is not source", labels: []string{"Translation"}, wantErr: ErrMissingLyrics},
		{musicID: 41020, name: "sole approved English is not assumed original", labels: []string{"Approved English"}, wantErr: ErrMissingLyrics},
		{musicID: 41021, name: "conflicting language label is not source", labels: []string{"Japanese English"}, wantErr: ErrMissingLyrics},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("music_%d_%s", test.musicID, test.name), func(t *testing.T) {
			blocks := make([]structuredVersionBlock, len(test.labels))
			for index, label := range test.labels {
				blocks[index] = structuredVersionBlock{label: label, kind: structuredVersionKind(label)}
			}
			assignStructuredVersionLanguageRoles(blocks)

			selected, err := selectStructuredLyricsVersion(blocks)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("selection error = %v, want %v; blocks = %+v", err, test.wantErr, blocks)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if selected.label != test.want || selected.languageRole != "source" {
				t.Fatalf("selected = %+v, want source label %q; blocks = %+v", selected, test.want, blocks)
			}
		})
	}
}

func TestStructuredLyricsVersionBlocksApplyBoundedLanguageRoles(t *testing.T) {
	content := `== Lyrics ==
<tabber>Japanese =
{|
! Japanese
|-
|歌う
|}
|-|中文歌词 =
{|
! Lyrics
|-
|歌唱
|}
</tabber>`

	section := content[strings.Index(content, "== Lyrics ==")+len("== Lyrics =="):]
	blocks, err := structuredLyricsVersionBlocks(section)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].languageRole != "source" || blocks[1].languageRole != "translation" {
		t.Fatalf("blocks = %+v", blocks)
	}
}
