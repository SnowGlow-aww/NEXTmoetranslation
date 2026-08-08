package lyricssource

import (
	"strings"
	"testing"
)

func TestValidateExactPublicFileBindingAcceptsExternalRecoverySessionRoot(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, path := range []string{
		"/private/tmp/moesekai-recovery/response.html",
		"/Volumes/Amia/Akiyama_mizuki/Coding/sessions/moesekai-recovery/response.html",
	} {
		t.Run(path, func(t *testing.T) {
			err := validateExactPublicFileBinding(ExactPublicFileBinding{
				Path:      path,
				SizeBytes: 1,
				SHA256:    sha,
			}, 1024)
			if err != nil {
				t.Fatalf("validateExactPublicFileBinding(%q) error = %v", path, err)
			}
		})
	}
}

func TestValidateExactPublicFileBindingRejectsPathsOutsideRecoveryRoots(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, path := range []string{
		"/Users/amia/Downloads/temp/response.html",
		"/Volumes/Amia/Akiyama_mizuki/Coding/sessions-evil/response.html",
		"/Volumes/Amia/Akiyama_mizuki/Coding/sessions/moesekai-recovery/../response.html",
	} {
		t.Run(path, func(t *testing.T) {
			err := validateExactPublicFileBinding(ExactPublicFileBinding{
				Path:      path,
				SizeBytes: 1,
				SHA256:    sha,
			}, 1024)
			if err == nil {
				t.Fatalf("validateExactPublicFileBinding(%q) unexpectedly succeeded", path)
			}
		})
	}
}
