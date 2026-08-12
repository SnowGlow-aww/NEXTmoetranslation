package publiclyricsbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"moesekai/server/internal/store"
)

func TestCatalogRuntimeMetadataSeparatesEmbeddedOverlayFromDatabaseState(t *testing.T) {
	metadata, err := CatalogRuntimeMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != ExpectedCatalogCount {
		t.Fatalf("runtime metadata count=%d expected=%d", len(metadata), ExpectedCatalogCount)
	}
	complete := metadata[307]
	if complete.ReleaseID != ReleaseID || !complete.ImmutableOverlay || complete.State != "complete" || !complete.HasDetail ||
		complete.Revision != 1 || complete.UpdatedAt != "2026-08-08T13:24:16Z" ||
		len(complete.AvailableVersions) != 2 || complete.AvailableVersions[0] != "full" || complete.AvailableVersions[1] != "game" ||
		complete.BatchSHA256 != BatchSHA256 || complete.RootSHA256 != RootSHA256 {
		t.Fatalf("complete runtime metadata=%+v", complete)
	}
	incomplete := metadata[789]
	if incomplete.ReleaseID != ReleaseID || !incomplete.ImmutableOverlay || incomplete.State != "incomplete" || incomplete.HasDetail ||
		incomplete.Revision != 1 || incomplete.UpdatedAt != "2026-08-08T13:24:16Z" || incomplete.AvailableVersions == nil ||
		len(incomplete.AvailableVersions) != 0 {
		t.Fatalf("incomplete runtime metadata=%+v", incomplete)
	}
	if _, ok := metadata[999999]; ok {
		t.Fatal("non-bundle music unexpectedly has runtime metadata")
	}
	complete.AvailableVersions[0] = "mutated"
	again, err := CatalogRuntimeMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if got := again[307].AvailableVersions[0]; got != "full" {
		t.Fatalf("runtime metadata cache leaked caller mutation: %q", got)
	}
}

func TestBundleIsClosedPublicV3Inventory(t *testing.T) {
	assets, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != ExpectedAssetCount {
		t.Fatalf("asset count=%d expected=%d", len(assets), ExpectedAssetCount)
	}
	index := assets["translation/lyrics/index.json"]
	if len(index) == 0 {
		t.Fatal("missing embedded public lyrics index")
	}
	document, err := store.DecodePublicLyricsV3Index(index)
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != 3 || len(document.Songs) != ExpectedCatalogCount {
		t.Fatalf("index version=%d songs=%d", document.Version, len(document.Songs))
	}
	for _, musicID := range []int{307, 754, 765, 83} {
		if _, ok := assets["translation/lyrics/music_"+itoa(musicID)+".json"]; !ok {
			t.Fatalf("missing representative detail musicId=%d", musicID)
		}
	}
	if _, ok := assets["translation/lyrics/music_789.json"]; ok {
		t.Fatal("incomplete musicId=789 unexpectedly has a public detail")
	}
	if got := sha256Hex(index); got != "6fe7261b364cebc5383cf2ab6a784c365606bc5aa97eaea429ecce489b4cd3d4" {
		t.Fatalf("index hash=%s", got)
	}
	for key := range assets {
		if !bytes.HasPrefix([]byte(key), []byte("translation/lyrics/")) {
			t.Fatalf("asset escaped public lyrics prefix: %q", key)
		}
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
