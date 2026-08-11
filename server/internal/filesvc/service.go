// Package filesvc serves the public, CDN-cacheable translation files under
// /files/*. Content is generated from the DB and held in memory with strong
// ETags; regeneration is debounced and triggered by DB changes. Responses carry
// long max-age + stale-while-revalidate so a CDN can cache aggressively while
// the ETag lets clients revalidate cheaply.
package filesvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"moesekai/server/internal/files"
	"moesekai/server/internal/model"
	"moesekai/server/internal/publiclyricsbundle"
	"moesekai/server/internal/store"
)

type asset struct {
	body        []byte
	etag        string
	contentType string
	modTime     time.Time
}

// ProjectionStatus distinguishes durable database writes from publication of a
// complete regenerated public asset set.
type ProjectionStatus struct {
	Generation    uint64 `json:"generation"`
	Pending       bool   `json:"pending"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

// Service holds generated assets in memory and serves them.
type Service struct {
	gen             *files.Generator
	store           *store.Store
	events          *store.EventStore
	publicLyrics    map[string][]byte
	publicLyricsErr error
	maxAge          time.Duration
	swr             time.Duration
	debounce        time.Duration

	mu        sync.RWMutex
	assets    map[string]asset // path key e.g. "translation/cards.json"
	rebuildMu sync.Mutex
	statusMu  sync.RWMutex
	status    ProjectionStatus
	requested uint64
	published uint64
	running   bool

	rebuildCh       chan struct{}
	rebuildAssetsFn func() error
	retryMin        time.Duration
	retryMax        time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
	startOnce       sync.Once
	stopOnce        sync.Once
	wg              sync.WaitGroup
}

func New(s *store.Store, es *store.EventStore, gen *files.Generator) *Service {
	publicLyrics, publicLyricsErr := publiclyricsbundle.Load()
	ctx, cancel := context.WithCancel(context.Background())
	svc := &Service{
		gen:             gen,
		store:           s,
		events:          es,
		publicLyrics:    publicLyrics,
		publicLyricsErr: publicLyricsErr,
		maxAge:          5 * time.Minute,
		swr:             time.Hour,
		debounce:        2 * time.Second,
		assets:          map[string]asset{},
		rebuildCh:       make(chan struct{}, 1),
		retryMin:        time.Second,
		retryMax:        30 * time.Second,
		ctx:             ctx,
		cancel:          cancel,
	}
	svc.rebuildAssetsFn = svc.rebuildAssets
	return svc
}

// Start launches the tracked publication worker and returns without waiting for
// the initial generation. Readiness remains false until that worker succeeds.
func (svc *Service) Start() {
	svc.startOnce.Do(func() {
		if svc.ctx.Err() != nil {
			return
		}
		svc.statusMu.Lock()
		if svc.requested <= svc.published {
			svc.requested = svc.published + 1
		}
		svc.statusMu.Unlock()
		svc.wg.Add(1)
		go func() {
			defer svc.wg.Done()
			svc.loop()
		}()
	})
}

// Stop cancels pending retries and debounced work. Wait must be called before
// closing SQLite to ensure an active generation has returned.
func (svc *Service) Stop() { svc.stopOnce.Do(svc.cancel) }

func (svc *Service) Wait() { svc.wg.Wait() }

// Trigger schedules a debounced rebuild (safe to call from DB change hooks).
func (svc *Service) Trigger() {
	if svc.ctx.Err() != nil {
		return
	}
	svc.statusMu.Lock()
	svc.requested++
	svc.statusMu.Unlock()
	select {
	case svc.rebuildCh <- struct{}{}:
	default:
	}
}

// Status returns a race-safe projection publication snapshot.
func (svc *Service) Status() ProjectionStatus {
	svc.statusMu.RLock()
	defer svc.statusMu.RUnlock()
	status := svc.status
	status.Generation = svc.published
	status.Pending = svc.running || svc.requested > svc.published
	return status
}

func (svc *Service) loop() {
	retryMin, retryMax := svc.retryBounds()
	retryDelay := retryMin
	err := svc.rebuild(svc.ctx)
	svc.logRebuildError(err)
	for {
		if svc.ctx.Err() != nil {
			return
		}
		svc.drainRebuildNotifications()
		pending := svc.hasPendingPublication()
		switch {
		case err != nil && pending:
			if !svc.waitForRetry(retryDelay) {
				return
			}
			if retryDelay < retryMax-retryDelay {
				retryDelay *= 2
			} else {
				retryDelay = retryMax
			}
		case pending:
			retryDelay = retryMin
			if !svc.waitForDebounce() {
				return
			}
		default:
			retryDelay = retryMin
			select {
			case <-svc.ctx.Done():
				return
			case <-svc.rebuildCh:
			}
			if !svc.waitForDebounce() {
				return
			}
		}
		err = svc.rebuild(svc.ctx)
		svc.logRebuildError(err)
	}
}

func (svc *Service) retryBounds() (time.Duration, time.Duration) {
	minimum := svc.retryMin
	if minimum <= 0 {
		minimum = time.Second
	}
	maximum := svc.retryMax
	if maximum < minimum {
		maximum = minimum
	}
	return minimum, maximum
}

func (svc *Service) waitForRetry(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-svc.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (svc *Service) waitForDebounce() bool {
	if svc.debounce <= 0 {
		return svc.ctx.Err() == nil
	}
	timer := time.NewTimer(svc.debounce)
	defer timer.Stop()
	for {
		select {
		case <-svc.ctx.Done():
			return false
		case <-svc.rebuildCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(svc.debounce)
		case <-timer.C:
			return true
		}
	}
}

func (svc *Service) drainRebuildNotifications() {
	for {
		select {
		case <-svc.rebuildCh:
		default:
			return
		}
	}
}

func (svc *Service) hasPendingPublication() bool {
	svc.statusMu.RLock()
	defer svc.statusMu.RUnlock()
	return svc.requested > svc.published
}

func (svc *Service) logRebuildError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[projection] generation failed; retrying: %v", err)
	}
}

// Rebuild regenerates all in-memory assets from the DB.
func (svc *Service) Rebuild() {
	if err := svc.rebuild(svc.ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[projection] generation failed: %v", err)
		select {
		case svc.rebuildCh <- struct{}{}:
		default:
		}
	}
}

func (svc *Service) rebuild(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	svc.rebuildMu.Lock()
	defer svc.rebuildMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	svc.statusMu.Lock()
	if svc.requested <= svc.published {
		svc.requested = svc.published + 1
	}
	targetGeneration := svc.requested
	svc.running = true
	svc.statusMu.Unlock()

	err := svc.rebuildAssetsFn()
	svc.statusMu.Lock()
	svc.running = false
	if err != nil {
		svc.status.LastError = "projection_generation_failed"
	} else {
		svc.published = targetGeneration
		svc.status.LastSuccessAt = time.Now().UTC().Format(time.RFC3339Nano)
		svc.status.LastError = ""
	}
	svc.statusMu.Unlock()
	return err
}

func (svc *Service) rebuildAssets() error {
	return svc.rebuildAssetsContext(svc.ctx)
}

func (svc *Service) rebuildAssetsContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if svc.publicLyricsErr != nil {
		return fmt.Errorf("public lyrics bundle: %w", svc.publicLyricsErr)
	}
	releaseContent, err := svc.store.LockContentExclusiveContext(ctx)
	if err != nil {
		return err
	}
	defer releaseContent()

	next := map[string]asset{}
	now := time.Now()

	for _, cat := range model.SupportedCategories {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := svc.gen.CategoryFlatJSON(cat)
		if err != nil {
			return fmt.Errorf("flat %s: %w", cat, err)
		}
		next["translation/"+cat+".json"] = makeAsset(b, "application/json; charset=utf-8", now)
		b, err = svc.gen.CategoryFullJSON(cat)
		if err != nil {
			return fmt.Errorf("full %s: %w", cat, err)
		}
		next["translation/"+cat+".full.json"] = makeAsset(b, "application/json; charset=utf-8", now)
	}
	for _, locale := range model.SupportedLocales {
		for _, cat := range model.SupportedCategories {
			if err := ctx.Err(); err != nil {
				return err
			}
			b, err := svc.gen.CategoryLocaleFlatJSON(cat, locale)
			if err != nil {
				return fmt.Errorf("locale flat %s/%s: %w", locale, cat, err)
			}
			key := fmt.Sprintf("v2/%s/translation/%s.json", locale, cat)
			next[key] = makeAsset(b, "application/json; charset=utf-8", now)
			b, err = svc.gen.CategoryLocaleFullJSON(cat, locale)
			if err != nil {
				return fmt.Errorf("locale full %s/%s: %w", locale, cat, err)
			}
			key = fmt.Sprintf("v2/%s/translation/%s.full.json", locale, cat)
			next[key] = makeAsset(b, "application/json; charset=utf-8", now)
		}
	}
	summaries, err := svc.events.List()
	if err != nil {
		return fmt.Errorf("event list: %w", err)
	}
	for _, sum := range summaries {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := svc.gen.EventStoryJSON(sum.EventID)
		if err != nil {
			return fmt.Errorf("event %d: %w", sum.EventID, err)
		}
		key := fmt.Sprintf("translation/eventStory/event_%d.json", sum.EventID)
		next[key] = makeAsset(b, "application/json; charset=utf-8", now)
		for _, locale := range model.SupportedLocales {
			b, err := svc.gen.EventStoryLocaleJSON(sum.EventID, locale)
			if err != nil {
				return fmt.Errorf("locale event %s/%d: %w", locale, sum.EventID, err)
			}
			key := fmt.Sprintf("v2/%s/translation/eventStory/event_%d.json", locale, sum.EventID)
			next[key] = makeAsset(b, "application/json; charset=utf-8", now)
		}
	}
	// The accepted recovery-v3 projection is public, immutable release content.
	// When present it is the complete lyrics publication source, so malformed or
	// legacy database publications cannot block or replace the reviewed 700-song
	// contract. Canonical and locale-mirror paths publish in one asset generation.
	lyrics := svc.publicLyrics
	if lyrics == nil {
		lyrics, err = svc.gen.PublishedLyricsJSON()
		if err != nil {
			return fmt.Errorf("lyrics: %w", err)
		}
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
	return nil
}

// SetAsset stores a pre-rendered asset (e.g. data/search-index.json) under key.
func (svc *Service) SetAsset(key string, body []byte, contentType string) {
	svc.SetAssets(map[string][]byte{key: body}, contentType)
}

// SetAssets publishes a related asset set under one lock so readers cannot
// observe mixed generations.
func (svc *Service) SetAssets(bodies map[string][]byte, contentType string) {
	now := time.Now()
	prepared := make(map[string]asset, len(bodies))
	for key, body := range bodies {
		if svc.publicLyrics != nil && isPublicLyricsAssetKey(key) {
			continue
		}
		prepared[key] = makeAsset(bytes.Clone(body), contentType, now)
	}
	svc.mu.Lock()
	for key, value := range prepared {
		svc.assets[key] = value
	}
	svc.mu.Unlock()
}

func isPublicLyricsAssetKey(key string) bool {
	if strings.HasPrefix(key, "translation/lyrics/") {
		return true
	}
	for _, locale := range model.SupportedLocales {
		if strings.HasPrefix(key, fmt.Sprintf("v2/%s/translation/lyrics/", locale)) {
			return true
		}
	}
	return false
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
