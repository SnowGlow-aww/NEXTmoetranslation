package lyricssource

import (
	"reflect"
	"testing"
)

func TestClassifyStructuredVersionCompletenessUsesWholeExplicitTokens(t *testing.T) {
	for name, test := range map[string]struct {
		label string
		want  structuredVersionCompleteness
	}{
		"full":                 {label: "SEKAI Version (Full)", want: structuredVersionCompletenessComplete},
		"long":                 {label: "Long Version", want: structuredVersionCompletenessComplete},
		"complete":             {label: "Complete", want: structuredVersionCompletenessComplete},
		"unicode full":         {label: "ＳＥＫＡＩ ＦＵＬＬ", want: structuredVersionCompletenessComplete},
		"game size":            {label: "SEKAI Version (Game Size)", want: structuredVersionCompletenessTruncated},
		"game hyphen size":     {label: "game-size", want: structuredVersionCompletenessTruncated},
		"short":                {label: "Short Version", want: structuredVersionCompletenessTruncated},
		"preview":              {label: "Preview", want: structuredVersionCompletenessTruncated},
		"partial":              {label: "Partial", want: structuredVersionCompletenessTruncated},
		"conflicting full":     {label: "Full Preview", want: structuredVersionCompletenessConflicting},
		"conflicting complete": {label: "Complete Game Size", want: structuredVersionCompletenessConflicting},
		"radio edit":           {label: "Radio edit", want: structuredVersionCompletenessUnknown},
		"single edit":          {label: "Single edit", want: structuredVersionCompletenessUnknown},
		"fuller":               {label: "Fuller Mix", want: structuredVersionCompletenessUnknown},
		"longing":              {label: "Longing Version", want: structuredVersionCompletenessUnknown},
		"completely":           {label: "Completely New Mix", want: structuredVersionCompletenessUnknown},
		"shortcake":            {label: "Shortcake Edition", want: structuredVersionCompletenessUnknown},
		"previewer":            {label: "Previewer Cut", want: structuredVersionCompletenessUnknown},
		"partially":            {label: "Partially Yours", want: structuredVersionCompletenessUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			if got := classifyStructuredVersionCompleteness(test.label); got != test.want {
				t.Fatalf("classification=%d want=%d", got, test.want)
			}
		})
	}
}

func TestPreferExplicitCompleteStructuredVersionsIsConservativeAndOrderIndependent(t *testing.T) {
	full := structuredVersionBlock{label: "VOCALOID Version (Full)", kind: "vocaloid", languageRole: "source"}
	preview := structuredVersionBlock{label: "SEKAI Preview", kind: "sekai", languageRole: "source"}
	unknown := structuredVersionBlock{label: "Radio edit", kind: "original", languageRole: "source"}
	conflicting := structuredVersionBlock{label: "Full Preview", kind: "original", languageRole: "source"}

	for _, input := range [][]structuredVersionBlock{
		{preview, full, unknown, conflicting},
		{conflicting, unknown, full, preview},
	} {
		got := preferExplicitCompleteStructuredVersions(input)
		if !reflect.DeepEqual(got, []structuredVersionBlock{full}) {
			t.Fatalf("preferred=%+v", got)
		}
	}

	withoutComplete := []structuredVersionBlock{preview, unknown, conflicting}
	if got := preferExplicitCompleteStructuredVersions(withoutComplete); !reflect.DeepEqual(got, withoutComplete) {
		t.Fatalf("unknown/truncated candidates were ranked: %+v", got)
	}
}

func TestSelectStructuredLyricsVersionPrefersExplicitCompleteBeforeRenditionKind(t *testing.T) {
	vocaloidFull := structuredVersionBlock{label: "VOCALOID Version (Full)", kind: "vocaloid", languageRole: "source"}
	sekaiGameSize := structuredVersionBlock{label: "SEKAI Version (Game Size)", kind: "sekai", languageRole: "source"}
	for _, input := range [][]structuredVersionBlock{
		{sekaiGameSize, vocaloidFull},
		{vocaloidFull, sekaiGameSize},
	} {
		selected, err := selectStructuredLyricsVersion(input)
		if err != nil || selected.label != vocaloidFull.label {
			t.Fatalf("selected=%+v err=%v", selected, err)
		}
	}

	sekaiComplete := structuredVersionBlock{label: "SEKAI Version (Complete)", kind: "sekai", languageRole: "source"}
	vocaloidPreview := structuredVersionBlock{label: "VOCALOID Preview", kind: "vocaloid", languageRole: "source"}
	selected, err := selectStructuredLyricsVersion([]structuredVersionBlock{vocaloidPreview, sekaiComplete})
	if err != nil || selected.label != sekaiComplete.label {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
}

func TestSelectStructuredLyricsVersionDoesNotInferBaseRenditionFromLabelAlone(t *testing.T) {
	pairs := [][]structuredVersionBlock{
		{
			{label: "Japanese lyrics", kind: "original", languageRole: "source"},
			{label: "Piapro Characters Version", kind: "original", languageRole: "source"},
		},
		{
			{label: "Original version", kind: "original", languageRole: "source"},
			{label: "Remastered version", kind: "original", languageRole: "source"},
		},
		{
			{label: "Original Singer Version", kind: "original", languageRole: "source"},
			{label: "Album Version", kind: "original", languageRole: "source"},
		},
	}
	for _, pair := range pairs {
		for _, input := range [][]structuredVersionBlock{pair, []structuredVersionBlock{pair[1], pair[0]}} {
			if selected, err := selectStructuredLyricsVersion(input); err != ErrAmbiguous {
				t.Fatalf("selected=%+v err=%v input=%+v", selected, err, input)
			}
		}
	}

	sekai := structuredVersionBlock{label: "SEKAI Version", kind: "sekai", languageRole: "source"}
	original := structuredVersionBlock{label: "Original version", kind: "original", languageRole: "source"}
	selected, err := selectStructuredLyricsVersion([]structuredVersionBlock{original, sekai})
	if err != nil || selected.label != sekai.label {
		t.Fatalf("selected=%+v err=%v want=%q", selected, err, sekai.label)
	}
}

func TestSelectStructuredLyricsVersionKeepsUnprovedCompletenessTiesAmbiguous(t *testing.T) {
	for name, blocks := range map[string][]structuredVersionBlock{
		"radio and single edits": {
			{label: "Radio edit", kind: "original", languageRole: "source"},
			{label: "Single edit", kind: "original", languageRole: "source"},
		},
		"Niconico and SCP releases": {
			{label: "Niconico ver", kind: "original", languageRole: "source"},
			{label: "SCP ver", kind: "original", languageRole: "source"},
		},
		"two base candidates": {
			{label: "Japanese lyrics", kind: "original", languageRole: "source"},
			{label: "Original Version", kind: "original", languageRole: "source"},
		},
		"two complete candidates": {
			{label: "Full Version A", kind: "original", languageRole: "source"},
			{label: "Complete Version B", kind: "original", languageRole: "source"},
		},
		"conflicting labels": {
			{label: "Full Preview", kind: "original", languageRole: "source"},
			{label: "Complete Game Size", kind: "original", languageRole: "source"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if selected, err := selectStructuredLyricsVersion(blocks); err != ErrAmbiguous {
				t.Fatalf("selected=%+v err=%v", selected, err)
			}
		})
	}
}

func TestSelectStructuredLyricsVersionIgnoresCompleteTranslationCandidate(t *testing.T) {
	source := structuredVersionBlock{label: "Radio edit", kind: "original", languageRole: "source"}
	translation := structuredVersionBlock{label: "Official English Full Translation", kind: "original", languageRole: "translation"}
	selected, err := selectStructuredLyricsVersion([]structuredVersionBlock{translation, source})
	if err != nil || selected.label != source.label {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
}
