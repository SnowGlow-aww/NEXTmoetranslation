// Package filesvc serves the public, CDN-cacheable translation files under
// /files/*. Content is generated from the DB and held in memory with strong
// ETags; regeneration is debounced and triggered by DB changes. Responses carry
// long max-age + stale-while-revalidate so a CDN can cache aggressively while
// the ETag lets clients revalidate cheaply.
package filesvc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"moesekai/server/internal/files"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

type asset struct {
	body        []byte
	etag        string
	contentType string
	modTime     time.Time
}

// Service holds generated assets in memory and serves them.
type Service struct {
	gen      *files.Generator
	store    *store.Store
	events   *store.EventStore
	maxAge   time.Duration
	swr      time.Duration
	debounce time.Duration

	mu        sync.RWMutex
	assets    map[string]asset // path key e.g. "translation/cards.json"
	rebuildMu sync.Mutex

	rebuildCh chan struct{}
}

func New(s *store.Store, es *store.EventStore, gen *files.Generator) *Service {
	return &Service{
		gen:       gen,
		store:     s,
		events:    es,
		maxAge:    5 * time.Minute,
		swr:       time.Hour,
		debounce:  2 * time.Second,
		assets:    map[string]asset{},
		rebuildCh: make(chan struct{}, 1),
	}
}

// Start builds assets once and launches the debounced rebuild loop.
func (svc *Service) Start() {
	svc.Rebuild()
	go svc.loop()
}

// Trigger schedules a debounced rebuild (safe to call from DB change hooks).
func (svc *Service) Trigger() {
	select {
	case svc.rebuildCh <- struct{}{}:
	default:
	}
}

func (svc *Service) loop() {
	var timer *time.Timer
	for range svc.rebuildCh {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(svc.debounce, svc.Rebuild)
	}
}

// Rebuild regenerates all in-memory assets from the DB.
func (svc *Service) Rebuild() {
	svc.rebuildMu.Lock()
	defer svc.rebuildMu.Unlock()
	releaseContent := svc.store.LockContentExclusive()
	defer releaseContent()

	next := map[string]asset{}
	now := time.Now()

	for _, cat := range model.SupportedCategories {
		b, err := svc.gen.CategoryFlatJSON(cat)
		if err != nil {
			return
		}
		next["translation/"+cat+".json"] = makeAsset(b, "application/json; charset=utf-8", now)
		b, err = svc.gen.CategoryFullJSON(cat)
		if err != nil {
			return
		}
		next["translation/"+cat+".full.json"] = makeAsset(b, "application/json; charset=utf-8", now)
	}
	for _, locale := range model.SupportedLocales {
		for _, cat := range model.SupportedCategories {
			b, err := svc.gen.CategoryLocaleFlatJSON(cat, locale)
			if err != nil {
				return
			}
			key := fmt.Sprintf("v2/%s/translation/%s.json", locale, cat)
			next[key] = makeAsset(b, "application/json; charset=utf-8", now)
			b, err = svc.gen.CategoryLocaleFullJSON(cat, locale)
			if err != nil {
				return
			}
			key = fmt.Sprintf("v2/%s/translation/%s.full.json", locale, cat)
			next[key] = makeAsset(b, "application/json; charset=utf-8", now)
		}
	}
	summaries, err := svc.events.List()
	if err != nil {
		return
	}
	for _, sum := range summaries {
		b, err := svc.gen.EventStoryJSON(sum.EventID)
		if err != nil {
			return
		}
		key := fmt.Sprintf("translation/eventStory/event_%d.json", sum.EventID)
		next[key] = makeAsset(b, "application/json; charset=utf-8", now)
		for _, locale := range model.SupportedLocales {
			b, err := svc.gen.EventStoryLocaleJSON(sum.EventID, locale)
			if err != nil {
				return
			}
			key := fmt.Sprintf("v2/%s/translation/eventStory/event_%d.json", locale, sum.EventID)
			next[key] = makeAsset(b, "application/json; charset=utf-8", now)
		}
	}
	lyrics, err := svc.gen.PublishedLyricsJSON()
	if err != nil {
		return
	}
	for key, body := range lyrics {
		next[key] = makeAsset(body, "application/json; charset=utf-8", now)
		for _, locale := range model.SupportedLocales {
			localizedKey := fmt.Sprintf("v2/%s/%s", locale, key)
			next[localizedKey] = makeAsset(body, "application/json; charset=utf-8", now)
		}
	}

	svc.mu.Lock()
	// Preserve only externally-set assets. Locale mirrors are generated above and
	// must disappear when their source event or lyrics publication disappears.
	for k, v := range svc.assets {
		if _, ok := next[k]; !ok && (k == "data/search-index.json" || k == "v2/data/search-index.json" || strings.HasSuffix(k, "/data/search-index.json")) {
			next[k] = v
		}
	}
	svc.assets = next
	svc.mu.Unlock()
}

// SetAsset stores a pre-rendered asset (e.g. data/search-index.json) under key.
func (svc *Service) SetAsset(key string, body []byte, contentType string) {
	svc.mu.Lock()
	svc.assets[key] = makeAsset(body, contentType, time.Now())
	svc.mu.Unlock()
}

func makeAsset(body []byte, contentType string, t time.Time) asset {
	sum := sha256.Sum256(body)
	return asset{
		body:        body,
		etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
		contentType: contentType,
		modTime:     t,
	}
}

// Handler serves GET /files/<path>. Path traversal is impossible because lookup
// is a map key match, not a filesystem path.
func (svc *Service) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/files/")
		key = strings.TrimPrefix(key, "/")

		svc.mu.RLock()
		a, ok := svc.assets[key]
		svc.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}

		h := w.Header()
		h.Set("Content-Type", a.contentType)
		h.Set("ETag", a.etag)
		h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d, stale-while-revalidate=%d",
			int(svc.maxAge.Seconds()), int(svc.swr.Seconds())))
		h.Set("Access-Control-Allow-Origin", "*")

		if match := r.Header.Get("If-None-Match"); match != "" && etagMatch(match, a.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.ServeContent(w, r, key, a.modTime, newReadSeeker(a.body))
	}
}

func etagMatch(ifNoneMatch, etag string) bool {
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}
