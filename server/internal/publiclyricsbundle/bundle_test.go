package publiclyricsbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"moesekai/server/internal/store"
)

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
