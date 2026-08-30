package main

import (
	"bytes"
	"errors"
	"fmt"

	"moesekai/server/internal/lyricsrootmanifest"
	"moesekai/server/internal/lyricsstaging"
)

type releaseBindings struct {
	Root               lyricsrootmanifest.Manifest
	RootFileSHA256     string
	RootFileBytes      int64
	Manifest           lyricsstaging.Manifest
	ManifestFileSHA256 string
	ManifestFileBytes  int64
}

func loadReleaseBindings(rootPath, manifestPath string) (releaseBindings, error) {
	if !canonicalAbsolutePath(rootPath) || !canonicalAbsolutePath(manifestPath) {
		return releaseBindings{}, errors.New("root and import manifest paths must be canonical and absolute")
	}
	rootBody, rootFileSHA256, err := readPinnedRegular(rootPath, "root manifest", maxFreshRootBytes, 0o600)
	if err != nil {
		return releaseBindings{}, err
	}
	if err := rejectJSONKeys(rootBody, compactRootForbiddenFields, "compact root manifest"); err != nil {
		return releaseBindings{}, err
	}
	root, err := lyricsrootmanifest.DecodeCanonical(rootBody)
	if err != nil {
		return releaseBindings{}, err
	}
	if root.Scope.Kind != lyricsrootmanifest.ScopeFinal || root.Catalog.RecordCount != releaseCatalogTargetCount ||
		root.Catalog.SourceSHA256 != releaseCatalogSHA256 || root.Catalog.MusicIDsSHA256 != releaseCatalogMusicIDsSHA256 ||
		len(root.Songs) != releaseCatalogTargetCount || root.Coverage.Total != releaseCatalogTargetCount ||
		root.Coverage.Complete != releaseCatalogTargetCount {
		return releaseBindings{}, errors.New("root is not the exact complete final 698 release root")
	}

	manifestBody, manifestFileSHA256, err := readPinnedRegular(manifestPath, "import manifest", lyricsstaging.MaxManifestBytes, 0o600)
	if err != nil {
		return releaseBindings{}, err
	}
	manifest, err := lyricsstaging.DecodeManifest(manifestBody)
	if err != nil {
		return releaseBindings{}, err
	}
	canonicalManifest, err := lyricsstaging.MarshalManifest(manifest)
	if err != nil || !bytes.Equal(canonicalManifest, manifestBody) {
		return releaseBindings{}, errors.New("import manifest is not the canonical producer encoding")
	}
	if manifest.Preflight.CatalogCount != releaseCatalogTargetCount ||
		manifest.Preflight.UniqueCompleteCount != releaseCatalogTargetCount ||
		len(manifest.Items) != releaseCatalogTargetCount || len(manifest.CatalogReference) != releaseCatalogTargetCount {
		return releaseBindings{}, errors.New("import manifest is not the exact complete 698 release batch")
	}
	for index, item := range manifest.Items {
		if item.MusicID != root.Songs[index].MusicID {
			return releaseBindings{}, fmt.Errorf("import manifest item %d does not follow the root music-ID order", index)
		}
	}
	return releaseBindings{
		Root: root, RootFileSHA256: rootFileSHA256, RootFileBytes: int64(len(rootBody)),
		Manifest: manifest, ManifestFileSHA256: manifestFileSHA256, ManifestFileBytes: int64(len(manifestBody)),
	}, nil
}
