package translator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	eventAssociationCacheTTL = time.Hour
	eventAssociationRetryTTL = 5 * time.Minute
)

// EventAssociationIndex maps a translated entity ID to stable related events.
// Cards use their earliest positive eventCards relation as the canonical origin
// event; event songs preserve their exact many-to-many masterdata relations.
// Unrelated categories are deliberately absent.
type EventAssociationIndex struct {
	Categories map[string]map[string][]int `json:"categories"`
}

func buildEventAssociationIndex(eventCards, eventMusics []map[string]any) EventAssociationIndex {
	categories := map[string]map[string][]int{
		"cards": {},
		"music": {},
	}
	add := func(category string, entityID, eventID int) {
		if entityID <= 0 || eventID <= 0 {
			return
		}
		key := strconv.Itoa(entityID)
		for _, existing := range categories[category][key] {
			if existing == eventID {
				return
			}
		}
		categories[category][key] = append(categories[category][key], eventID)
	}
	cardOrigins := map[int]int{}
	for _, relation := range eventCards {
		cardID, eventID := getInt(relation, "cardId"), getInt(relation, "eventId")
		if cardID <= 0 || eventID <= 0 {
			continue
		}
		if current := cardOrigins[cardID]; current == 0 || eventID < current {
			cardOrigins[cardID] = eventID
		}
	}
	for cardID, eventID := range cardOrigins {
		add("cards", cardID, eventID)
	}
	for _, relation := range eventMusics {
		add("music", getInt(relation, "musicId"), getInt(relation, "eventId"))
	}
	for _, entities := range categories {
		for entityID := range entities {
			sort.Ints(entities[entityID])
		}
	}
	return EventAssociationIndex{Categories: categories}
}

func preserveEventAssociationCategory(index, cached EventAssociationIndex, category string, fetchErr error) {
	if fetchErr == nil || cached.Categories == nil {
		return
	}
	stale, ok := cached.Categories[category]
	if !ok {
		return
	}
	copyOfStale := make(map[string][]int, len(stale))
	for entityID, eventIDs := range stale {
		copyOfStale[entityID] = append([]int(nil), eventIDs...)
	}
	index.Categories[category] = copyOfStale
}

// EventAssociations returns a bounded, last-known-good in-memory index. It is
// fetched independently of translation writes and cached so opening or
// reconciling the console cannot repeatedly hit upstream masterdata.
func (t *Translator) EventAssociations(ctx context.Context) (EventAssociationIndex, error) {
	t.eventAssociationMu.Lock()
	defer t.eventAssociationMu.Unlock()

	if err := ctx.Err(); err != nil {
		return EventAssociationIndex{}, err
	}
	if t.eventAssociationCached.Categories != nil && time.Now().Before(t.eventAssociationExpiresAt) {
		return t.eventAssociationCached, nil
	}
	cached := t.eventAssociationCached
	var eventCards, eventMusics []map[string]any
	var cardsErr, musicErr error
	var fetches sync.WaitGroup
	fetches.Add(2)
	go func() {
		defer fetches.Done()
		eventCards, cardsErr = t.fetchMasterdataContext(ctx, "eventCards.json", "jp")
	}()
	go func() {
		defer fetches.Done()
		eventMusics, musicErr = t.fetchMasterdataContext(ctx, "eventMusics.json", "jp")
	}()
	fetches.Wait()
	if err := ctx.Err(); err != nil {
		return EventAssociationIndex{}, err
	}
	refreshedAt := time.Now()
	if cardsErr != nil && musicErr != nil {
		if cached.Categories != nil {
			t.eventAssociationExpiresAt = refreshedAt.Add(eventAssociationRetryTTL)
			return cached, nil
		}
		return EventAssociationIndex{}, fmt.Errorf("load event associations: cards: %v; music: %v", cardsErr, musicErr)
	}
	index := buildEventAssociationIndex(eventCards, eventMusics)
	preserveEventAssociationCategory(index, cached, "cards", cardsErr)
	preserveEventAssociationCategory(index, cached, "music", musicErr)
	ttl := eventAssociationCacheTTL
	if cardsErr != nil || musicErr != nil {
		ttl = eventAssociationRetryTTL
	}
	t.eventAssociationCached = index
	t.eventAssociationExpiresAt = refreshedAt.Add(ttl)
	return index, nil
}
