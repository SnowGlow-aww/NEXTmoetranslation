package legacy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEventStoryRejectsDuplicateObjectKeysRecursively(t *testing.T) {
	for name, body := range map[string]string{
		"top level": `{"meta":{"source":"cn"},"meta":{"source":"cn"},"episodes":{}}`,
		"episode":   `{"meta":{"source":"cn"},"episodes":{"1":{"scenarioId":"one","title":"first","title":"second","talkData":{}}}}`,
		"talk data": `{"meta":{"source":"cn"},"episodes":{"1":{"scenarioId":"one","title":"title","talkData":{"line":"first","line":"second"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "event_1.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadEventStory(path); err == nil || !strings.Contains(err.Error(), "duplicate object key") {
				t.Fatalf("duplicate event JSON error=%v", err)
			}
		})
	}
}
