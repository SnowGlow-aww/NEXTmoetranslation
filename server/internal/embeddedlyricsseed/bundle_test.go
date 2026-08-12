package embeddedlyricsseed

import "testing"

func TestEmbeddedLyricsEditorSeedLoads(t *testing.T) {
	bundle, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ArchiveSHA256 != ExpectedArchiveSHA256 || bundle.Manifest.CatalogCount != ExpectedCatalogCount ||
		len(bundle.Manifest.Items) != ExpectedCatalogCount || len(bundle.Documents) != ExpectedSourceV3 ||
		len(bundle.Artifacts) != ExpectedArtifacts || len(bundle.Contributions) != ExpectedContributions ||
		len(bundle.LegacyDocuments) != ExpectedLegacy || len(bundle.Availability) != ExpectedAvailability {
		t.Fatalf("embedded lyrics editor seed counts differ: archive=%s manifest=%+v docs=%d artifacts=%d contributions=%d legacy=%d availability=%d",
			bundle.ArchiveSHA256, bundle.Manifest, len(bundle.Documents), len(bundle.Artifacts), len(bundle.Contributions),
			len(bundle.LegacyDocuments), len(bundle.Availability))
	}
}
