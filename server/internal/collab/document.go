package collab

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/reearth/ygo/crdt"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

const lyricsSchemaVersion = 1

const (
	structuredItemIDKey         = "__yjsId"
	structuredItemGenerationKey = "__yjsGeneration"
	structuredItemOriginKey     = "__yjsOrigin"
)

type documentKind uint8

const (
	documentLegacy documentKind = iota
	documentRenditions
)

func blankLyrics(musicID int) model.SongLyrics {
	return model.SongLyrics{
		MusicID: musicID,
		Status:  "draft",
		Lines:   []model.LyricLine{},
	}
}

func kindAndRevision(document any) (documentKind, int, error) {
	switch value := document.(type) {
	case model.SongLyrics:
		return documentLegacy, value.Revision, nil
	case store.LyricsRenditionDocument:
		return documentRenditions, value.Revision, nil
	default:
		return 0, 0, fmt.Errorf("unsupported lyrics document type %T", document)
	}
}

func canonicalDocument(document any) ([]byte, string, int, documentKind, error) {
	kind, revision, err := kindAndRevision(document)
	if err != nil {
		return nil, "", 0, 0, err
	}
	body, err := json.Marshal(document)
	if err != nil {
		return nil, "", 0, 0, err
	}
	digest := sha256.Sum256(body)
	return body, hex.EncodeToString(digest[:]), revision, kind, nil
}

func documentUpdate(document any) ([]byte, error) {
	body, _, _, _, err := canonicalDocument(document)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, err
	}
	documentCRDT := crdt.New()
	root := documentCRDT.GetMap("lyrics")
	documentCRDT.Transact(func(txn *crdt.Transaction) {
		root.Set(txn, "schemaVersion", lyricsSchemaVersion)
		for key, value := range values {
			setMapValue(txn, root, key, value, "lyrics."+key)
		}
	})
	return crdt.EncodeStateAsUpdateV1(documentCRDT, nil), nil
}

func setMapValue(txn *crdt.Transaction, target *crdt.YMap, key string, value any, path string) {
	if textField(key) {
		if text, ok := value.(string); ok {
			target.Set(txn, key, newText(txn, text))
			return
		}
	}
	target.Set(txn, key, nestedValue(txn, value, key, path))
}

func nestedValue(txn *crdt.Transaction, value any, key, path string) any {
	switch value := value.(type) {
	case map[string]any:
		result := crdt.NewMapPrelim()
		for key, item := range value {
			setMapValue(txn, result, key, item, path+"."+key)
		}
		return result
	case []any:
		result := crdt.NewArrayPrelim()
		for index, item := range value {
			itemPath := fmt.Sprintf("%s[%d]", path, index)
			nested := nestedValue(txn, item, "", itemPath)
			if (key == "segments" || key == "ruby") && nested != nil {
				if itemMap, ok := nested.(*crdt.YMap); ok {
					id := key + ":" + itemPath
					itemMap.Set(txn, structuredItemIDKey, id)
					itemMap.Set(txn, structuredItemGenerationKey, "seed:"+id)
				}
			}
			switch nested := nested.(type) {
			case *crdt.YMap:
				result.PushType(txn, nested)
			case *crdt.YArray:
				result.PushType(txn, nested)
			case *crdt.YText:
				result.PushType(txn, nested)
			default:
				result.Push(txn, []any{nested})
			}
		}
		return result
	default:
		return value
	}
}

func newText(txn *crdt.Transaction, value string) *crdt.YText {
	text := crdt.NewTextPrelim()
	if value != "" {
		text.Insert(txn, 0, value, nil)
	}
	return text
}

func textField(key string) bool {
	switch key {
	case "attribution", "translationCredit", "proofreadingCredit", "sourceNote", "licenseNote",
		"japanese", "zh-CN", "en-US", "label", "translation", "proofreading", "text", "reading", "name":
		return true
	default:
		return false
	}
}

func materializeDocument(root *crdt.YMap, kind documentKind, musicID int) (any, error) {
	body, err := root.ToJSON()
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, err
	}
	var schemaVersion int
	if raw, ok := values["schemaVersion"]; !ok || json.Unmarshal(raw, &schemaVersion) != nil || schemaVersion != lyricsSchemaVersion {
		return nil, ErrSchemaMismatch
	}
	delete(values, "schemaVersion")
	if err := stripAndValidateStructuredMetadata(values); err != nil {
		return nil, err
	}
	body, err = json.Marshal(values)
	if err != nil {
		return nil, err
	}
	if err := rejectNullJSON(body); err != nil {
		return nil, err
	}
	switch kind {
	case documentLegacy:
		var document model.SongLyrics
		if err := decodeStrictJSON(body, &document); err != nil {
			return nil, fmt.Errorf("decode collaborative lyrics: %w", err)
		}
		if document.MusicID != musicID {
			return nil, ErrDocumentMismatch
		}
		return document, nil
	case documentRenditions:
		var document store.LyricsRenditionDocument
		if err := decodeStrictJSON(body, &document); err != nil {
			return nil, fmt.Errorf("decode collaborative renditions: %w", err)
		}
		if document.MusicID != musicID {
			return nil, ErrDocumentMismatch
		}
		return document, nil
	default:
		return nil, ErrDocumentMismatch
	}
}

func stripAndValidateStructuredMetadata(values map[string]json.RawMessage) error {
	var document any
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return err
	}
	if err := walkStructuredMetadata(document, ""); err != nil {
		return err
	}
	clean, ok := document.(map[string]any)
	if !ok {
		return ErrDocumentMismatch
	}
	for key := range values {
		delete(values, key)
	}
	for key, value := range clean {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		values[key] = raw
	}
	return nil
}

func walkStructuredMetadata(value any, parentKey string) error {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if err := walkStructuredMetadata(item, key); err != nil {
				return err
			}
		}
	case []any:
		if parentKey != "segments" && parentKey != "ruby" {
			for _, item := range value {
				if err := walkStructuredMetadata(item, ""); err != nil {
					return err
				}
			}
			return nil
		}
		ids := make(map[string]struct{}, len(value))
		originGenerations := make(map[string]map[string]struct{})
		legacyItems := 0
		identifiedItems := 0
		for _, item := range value {
			entry, ok := item.(map[string]any)
			if !ok {
				return ErrDocumentMismatch
			}
			idValue, hasID := entry[structuredItemIDKey]
			generationValue, hasGeneration := entry[structuredItemGenerationKey]
			originValue, hasOrigin := entry[structuredItemOriginKey]
			if !hasID && !hasGeneration && !hasOrigin {
				legacyItems++
				if err := walkStructuredMetadata(entry, ""); err != nil {
					return err
				}
				continue
			}
			id, idOK := idValue.(string)
			generation, generationOK := generationValue.(string)
			if !idOK || id == "" || !generationOK || generation == "" {
				return ErrDocumentMismatch
			}
			identifiedItems++
			if _, duplicate := ids[id]; duplicate {
				return ErrDocumentMismatch
			}
			ids[id] = struct{}{}
			if hasOrigin {
				originString, ok := originValue.(string)
				if !ok || originString == "" {
					return ErrDocumentMismatch
				}
				generations := originGenerations[originString]
				if generations == nil {
					generations = make(map[string]struct{})
					originGenerations[originString] = generations
				}
				generations[generation] = struct{}{}
			}
			delete(entry, structuredItemIDKey)
			delete(entry, structuredItemGenerationKey)
			delete(entry, structuredItemOriginKey)
			if err := walkStructuredMetadata(entry, ""); err != nil {
				return err
			}
		}
		if legacyItems > 0 && identifiedItems > 0 {
			return ErrDocumentMismatch
		}
		for _, generations := range originGenerations {
			if len(generations) > 1 {
				return ErrDocumentMismatch
			}
		}
	}
	return nil
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectNullJSON(body []byte) error {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case nil:
			return ErrDocumentMismatch
		case map[string]any:
			for _, item := range value {
				if err := walk(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range value {
				if err := walk(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}
