// Package publiclyricsbundle exposes the accepted, public-only 700-song Public
// Lyrics v3 runtime projection. It contains no producer database, recovery
// evidence, manifest, receipt, or authenticated application state.
package publiclyricsbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

const (
	ReleaseID                = "runtime-rebind-700-release-final-hardened-20260808b"
	BatchSHA256              = "6559b9b21fff20418ec97e1a965cbff3f516f18205e122355e0e96cd19472bd7"
	RootSHA256               = "fe486efcd029659519b411a88dfe5688d2a67f351bc19bb61fa970784a39b3ad"
	ExpectedArchiveSHA256    = "6a987c5ed796b4609e4bcbc5c67126196eb660258ad19bea672408cb42f9136b"
	ExpectedInventorySHA256  = "604aae68e3cd6824a8960a3cbbec5e015af48e5fcdd9895f785ff61e019d1f4b"
	ExpectedTarSHA256        = "c08f53d7ad0dda1e5a32042608d5d7b9d570292c36371f618dec0529f90cac96"
	ExpectedTarBytes         = 48066560
	ExpectedRuntimeBytes     = 47561072
	ExpectedAssetCount       = 654
	ExpectedCatalogCount     = 700
	ExpectedDetailCount      = 653
	maxPublicLyricsAssetSize = 2 << 20
)

//go:embed public-v3.tar.gz
var archiveFS embed.FS

var (
	loadOnce          sync.Once
	loaded            map[string][]byte
	loadedErr         error
	metadataOnce      sync.Once
	loadedMetadata    map[int]model.RuntimeLyricsMetadata
	loadedMetadataErr error
	detailRE          = regexp.MustCompile(`^music_([1-9][0-9]*)\.json$`)
)

type inventoryEntry struct {
	path   string
	sha256 string
	bytes  int
}

type countWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countWriter) Write(body []byte) (int, error) {
	written, err := writer.writer.Write(body)
	writer.count += int64(written)
	return written, err
}

type zeroPaddingWriter struct {
	nonzero bool
}

func (writer *zeroPaddingWriter) Write(body []byte) (int, error) {
	for _, value := range body {
		if value != 0 {
			writer.nonzero = true
			break
		}
	}
	return len(body), nil
}

// Load verifies and returns the immutable runtime assets under their files
// service keys (translation/lyrics/index.json and music_<id>.json). The map is a
// fresh shallow copy; callers must treat the byte slices as read-only.
func Load() (map[string][]byte, error) {
	loadOnce.Do(load)
	if loadedErr != nil {
		return nil, loadedErr
	}
	assets := make(map[string][]byte, len(loaded))
	for key, body := range loaded {
		assets[key] = body
	}
	return assets, nil
}

// CatalogRuntimeMetadata returns the immutable Public v3 index projection used
// only to explain what the embedded runtime serves. These records do not
// represent authenticated editor drafts or database publication state.
func CatalogRuntimeMetadata() (map[int]model.RuntimeLyricsMetadata, error) {
	metadataOnce.Do(loadCatalogRuntimeMetadata)
	if loadedMetadataErr != nil {
		return nil, loadedMetadataErr
	}
	metadata := make(map[int]model.RuntimeLyricsMetadata, len(loadedMetadata))
	for musicID, item := range loadedMetadata {
		item.AvailableVersions = append([]string{}, item.AvailableVersions...)
		metadata[musicID] = item
	}
	return metadata, nil
}

func loadCatalogRuntimeMetadata() {
	assets, err := Load()
	if err != nil {
		loadedMetadataErr = err
		return
	}
	index, err := store.DecodePublicLyricsV3Index(assets["translation/lyrics/index.json"])
	if err != nil {
		loadedMetadataErr = err
		return
	}
	metadata := make(map[int]model.RuntimeLyricsMetadata, len(index.Songs))
	for _, song := range index.Songs {
		versions := append([]string(nil), song.AvailableVersions...)
		metadata[song.MusicID] = model.RuntimeLyricsMetadata{
			ReleaseID:         ReleaseID,
			ImmutableOverlay:  true,
			State:             string(song.State),
			HasDetail:         song.State == store.PublicLyricsStateComplete || song.State == store.PublicLyricsStateGameOnly,
			AvailableVersions: versions,
			Revision:          song.Revision,
			UpdatedAt:         song.UpdatedAt,
			BatchSHA256:       BatchSHA256,
			RootSHA256:        RootSHA256,
		}
	}
	loadedMetadata = metadata
}

func load() {
	archiveBytes, err := archiveFS.ReadFile("public-v3.tar.gz")
	if err != nil {
		loadedErr = fmt.Errorf("read embedded public lyrics archive: %w", err)
		return
	}
	archiveDigest := sha256.Sum256(archiveBytes)
	if hex.EncodeToString(archiveDigest[:]) != ExpectedArchiveSHA256 {
		loadedErr = fmt.Errorf("public lyrics archive SHA-256 differs")
		return
	}

	archiveReader := bytes.NewReader(archiveBytes)
	compressed, err := gzip.NewReader(archiveReader)
	if err != nil {
		loadedErr = fmt.Errorf("open public lyrics archive: %w", err)
		return
	}
	defer compressed.Close()
	compressed.Multistream(false)
	if compressed.Name != "" || compressed.Comment != "" || len(compressed.Extra) != 0 || !compressed.ModTime.IsZero() {
		loadedErr = fmt.Errorf("public lyrics gzip metadata is not canonical")
		return
	}
	decompressedDigest := sha256.New()
	decompressed := &countWriter{writer: decompressedDigest}
	stream := io.TeeReader(io.LimitReader(compressed, ExpectedTarBytes+1), decompressed)

	assets := make(map[string][]byte, ExpectedAssetCount)
	inventory := make([]inventoryEntry, 0, ExpectedAssetCount)
	totalBytes := 0
	reader := tar.NewReader(stream)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			loadedErr = fmt.Errorf("read public lyrics archive: %w", err)
			return
		}
		if header.Typeflag != tar.TypeReg || header.Mode != 0o444 || header.Uid != 0 || header.Gid != 0 ||
			header.Uname != "" || header.Gname != "" || header.Linkname != "" || header.Devmajor != 0 || header.Devminor != 0 ||
			header.ModTime.Unix() != 0 || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() ||
			len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 || path.Clean(header.Name) != header.Name || strings.Contains(header.Name, "/") ||
			(header.Name != "index.json" && !detailRE.MatchString(header.Name)) {
			loadedErr = fmt.Errorf("invalid public lyrics archive entry %q", header.Name)
			return
		}
		if header.Size < 1 || header.Size > maxPublicLyricsAssetSize {
			loadedErr = fmt.Errorf("invalid public lyrics asset size for %q", header.Name)
			return
		}
		if _, exists := assets[header.Name]; exists {
			loadedErr = fmt.Errorf("duplicate public lyrics asset %q", header.Name)
			return
		}
		body, err := io.ReadAll(io.LimitReader(reader, maxPublicLyricsAssetSize+1))
		if err != nil || int64(len(body)) != header.Size {
			loadedErr = fmt.Errorf("read public lyrics asset %q: size mismatch", header.Name)
			return
		}
		digest := sha256.Sum256(body)
		inventory = append(inventory, inventoryEntry{path: header.Name, sha256: hex.EncodeToString(digest[:]), bytes: len(body)})
		assets[header.Name] = body
		totalBytes += len(body)
		if len(assets) > ExpectedAssetCount || totalBytes > ExpectedRuntimeBytes {
			loadedErr = fmt.Errorf("public lyrics archive exceeds its closed inventory")
			return
		}
	}

	padding := &zeroPaddingWriter{}
	if _, err := io.Copy(padding, stream); err != nil {
		loadedErr = fmt.Errorf("finish public lyrics archive: %w", err)
		return
	}
	if padding.nonzero || decompressed.count != ExpectedTarBytes || hex.EncodeToString(decompressedDigest.Sum(nil)) != ExpectedTarSHA256 {
		loadedErr = fmt.Errorf("public lyrics tar stream differs")
		return
	}
	if archiveReader.Len() != 0 {
		loadedErr = fmt.Errorf("public lyrics archive contains a second gzip member or trailing bytes")
		return
	}

	if len(assets) != ExpectedAssetCount || totalBytes != ExpectedRuntimeBytes {
		loadedErr = fmt.Errorf("public lyrics inventory count/size differs")
		return
	}
	if inventorySHA256(inventory) != ExpectedInventorySHA256 {
		loadedErr = fmt.Errorf("public lyrics inventory SHA-256 differs")
		return
	}
	if err := validateDocuments(assets); err != nil {
		loadedErr = err
		return
	}

	loaded = make(map[string][]byte, len(assets))
	for name, body := range assets {
		loaded["translation/lyrics/"+name] = body
	}
}

func inventorySHA256(entries []inventoryEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	digest := sha256.New()
	for _, entry := range entries {
		digest.Write([]byte(entry.path))
		digest.Write([]byte{0})
		digest.Write([]byte(entry.sha256))
		digest.Write([]byte{0})
		digest.Write([]byte(strconv.Itoa(entry.bytes)))
		digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validateDocuments(assets map[string][]byte) error {
	index, err := store.DecodePublicLyricsV3Index(assets["index.json"])
	if err != nil {
		return err
	}
	if index.Version != 3 || len(index.Songs) != ExpectedCatalogCount {
		return fmt.Errorf("public lyrics index version/catalog count differs")
	}

	expectedDetails := make(map[int]store.PublicLyricsIndexSong, ExpectedDetailCount)
	seenMusic := make(map[int]struct{}, ExpectedCatalogCount)
	states := map[string]int{}
	for _, song := range index.Songs {
		if song.MusicID <= 0 {
			return fmt.Errorf("public lyrics index contains invalid music ID")
		}
		if _, exists := seenMusic[song.MusicID]; exists {
			return fmt.Errorf("public lyrics index contains duplicate music ID %d", song.MusicID)
		}
		seenMusic[song.MusicID] = struct{}{}
		states[string(song.State)]++
		switch song.State {
		case store.PublicLyricsStateComplete, store.PublicLyricsStateGameOnly:
			expectedDetails[song.MusicID] = song
		case store.PublicLyricsStateSatisfiedNoLyrics, store.PublicLyricsStateIncomplete:
		default:
			return fmt.Errorf("public lyrics index contains unexpected state %q", song.State)
		}
	}
	if states["complete"] != 645 || states["game_only"] != 8 || states["satisfied_no_lyrics"] != 15 || states["incomplete"] != 32 || len(expectedDetails) != ExpectedDetailCount {
		return fmt.Errorf("public lyrics state/detail counts differ")
	}

	seenDetails := make(map[int]struct{}, ExpectedDetailCount)
	for name, body := range assets {
		match := detailRE.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		musicID, _ := strconv.Atoi(match[1])
		detail, err := store.DecodePublicLyricsV3Detail(body)
		if err != nil {
			return fmt.Errorf("decode public lyrics detail %q: %w", name, err)
		}
		if detail.Version != 3 || detail.MusicID != musicID {
			return fmt.Errorf("public lyrics detail identity differs for %q", name)
		}
		song, expected := expectedDetails[musicID]
		if !expected {
			return fmt.Errorf("unexpected public lyrics detail %q", name)
		}
		if detail.Revision != song.Revision || detail.UpdatedAt != song.UpdatedAt || detail.State != song.State {
			return fmt.Errorf("public lyrics detail/index metadata differs for %q", name)
		}
		if !sameStringSlices(detailAvailableVersions(detail), song.AvailableVersions) {
			return fmt.Errorf("public lyrics detail/index versions differ for %q", name)
		}
		seenDetails[musicID] = struct{}{}
	}
	if len(seenDetails) != len(expectedDetails) {
		return fmt.Errorf("public lyrics detail inventory differs from index")
	}
	return nil
}

func detailAvailableVersions(detail store.PublicLyricsV3DetailDocument) []string {
	seen := map[string]bool{}
	for _, rendition := range detail.Renditions {
		for _, version := range rendition.AvailableVersions {
			seen[version] = true
		}
	}
	versions := make([]string, 0, 2)
	for _, version := range []string{"full", "game"} {
		if seen[version] {
			versions = append(versions, version)
		}
	}
	return versions
}

func sameStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
