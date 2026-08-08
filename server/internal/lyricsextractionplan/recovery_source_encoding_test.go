package lyricsextractionplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestLegacySnapshotV1CanonicalDigestVectorIsUnchanged(t *testing.T) {
	files := []SourceFileIdentity{{Path: "a.go", SizeBytes: 1, SHA256: sha256Hex([]byte("x"))}}
	got, err := SourceSnapshotSHA256(files)
	if err != nil {
		t.Fatal(err)
	}
	const want = "9f6dedf9ccc49cc9a477b9be02c926366a2ee160113589aac24436a60169897b"
	if got != want {
		t.Fatalf("legacy snapshot-v1 digest=%s want=%s", got, want)
	}
	zero := []SourceFileIdentity{{Path: "a.empty", SizeBytes: 0, SHA256: sha256Hex(nil)}}
	if _, err := SourceSnapshotSHA256(zero); err == nil {
		t.Fatal("legacy snapshot-v1 unexpectedly admitted a zero-byte identity")
	}
}

func TestRecoverySnapshotV2CanonicalDigestVectorAdmitsZeroBytes(t *testing.T) {
	files := []SourceFileIdentity{
		{Path: "a.empty", SizeBytes: 0, SHA256: sha256Hex(nil)},
		{Path: "b.go", SizeBytes: 1, SHA256: sha256Hex([]byte("x"))},
	}
	got, err := RecoverySourceSnapshotSHA256(files)
	if err != nil {
		t.Fatal(err)
	}
	const want = "367694a96118c1cc33995f90f67a9af970024208f24e294a91fdfc6e94c951a5"
	if got != want {
		t.Fatalf("recovery snapshot-v2 digest=%s want=%s", got, want)
	}
}

func TestRecoveryFixtureManifestV2CanonicalDigestVector(t *testing.T) {
	manifest := RecoveryFixtureManifestV2{
		SchemaVersion: RecoveryFixtureManifestSchemaVersionV2, SelectionPolicy: RecoverySourceSelectionPolicyV2,
		SnapshotAlgorithm: RecoverySourceSnapshotAlgorithmV2,
		Fixtures: []RecoveryFixtureIdentityV2{
			syntheticFixtureIdentityV2(t, testMediaFixturePath, RecoveryFixtureFormatMediaWikiPageV1, syntheticMediaWikiFixtureBody()),
			syntheticFixtureIdentityV2(t, testRawFixturePath, RecoveryFixtureFormatRawFileV1, []byte("raw fixture\n")),
		},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRecoveryFixtureManifestV2(body); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	got := hex.EncodeToString(digest[:])
	const want = "1972b29b0cbe44f5f920f4b0d70bdcf9f5eb2d1f895053390f6158d816caccca"
	if got != want {
		t.Fatalf("recovery fixture manifest-v2 digest=%s want=%s", got, want)
	}
}
