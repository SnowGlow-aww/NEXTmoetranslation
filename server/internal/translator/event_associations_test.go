package translator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"moesekai/server/internal/config"
)

func TestBuildEventAssociationIndexUsesExactCardAndMusicRelations(t *testing.T) {
	index := buildEventAssociationIndex(
		[]map[string]any{
			{"eventId": float64(7), "cardId": float64(101)},
			{"eventId": float64(3), "cardId": float64(101)},
			{"eventId": float64(7), "cardId": float64(101)},
			{"eventId": float64(0), "cardId": float64(102)},
		},
		[]map[string]any{
			{"eventId": float64(9), "musicId": float64(501)},
			{"eventId": float64(4), "musicId": float64(502)},
		},
	)
	if got := index.Categories["cards"]["101"]; !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("card origin association = %v", got)
	}
	if _, exists := index.Categories["cards"]["102"]; exists {
		t.Fatalf("invalid event association was retained: %+v", index.Categories["cards"])
	}
	if got := index.Categories["music"]["501"]; !reflect.DeepEqual(got, []int{9}) {
		t.Fatalf("music associations = %v", got)
	}
	if got := index.Categories["music"]["502"]; !reflect.DeepEqual(got, []int{4}) {
		t.Fatalf("music associations = %v", got)
	}
}

func TestPreserveEventAssociationCategoryKeepsLastKnownGoodData(t *testing.T) {
	cached := EventAssociationIndex{Categories: map[string]map[string][]int{
		"cards": {"101": {3}},
		"music": {"501": {9}},
	}}
	index := buildEventAssociationIndex(nil, []map[string]any{
		{"eventId": float64(10), "musicId": float64(501)},
	})
	preserveEventAssociationCategory(index, cached, "cards", errors.New("temporary failure"))
	preserveEventAssociationCategory(index, cached, "music", nil)

	if got := index.Categories["cards"]["101"]; !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("stale card associations were not preserved: %v", got)
	}
	if got := index.Categories["music"]["501"]; !reflect.DeepEqual(got, []int{10}) {
		t.Fatalf("successful music refresh was replaced: %v", got)
	}
	index.Categories["cards"]["101"][0] = 99
	if got := cached.Categories["cards"]["101"][0]; got != 3 {
		t.Fatalf("cached association was aliased by refreshed index: %d", got)
	}
}

func configureEventAssociationSource(t *testing.T, cfg interface{ Set(string, string) error }, baseURL string) {
	t.Helper()
	for key, value := range map[string]string{
		config.KeyUpstreamJPMasterdataURL:         baseURL,
		config.KeyUpstreamJPMasterdataFallbackURL: baseURL + "/fallback",
	} {
		if err := cfg.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventAssociationsPreservesFailedCategoryAndRetriesSoon(t *testing.T) {
	translator, _, cfg := openTestTranslator(t)
	var failCards atomic.Bool
	var musicEventID atomic.Int64
	musicEventID.Store(9)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/eventCards.json":
			if failCards.Load() {
				http.Error(w, "temporary", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `[{"eventId":3,"cardId":101}]`)
		case "/eventMusics.json":
			fmt.Fprintf(w, `[{"eventId":%d,"musicId":501}]`, musicEventID.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	configureEventAssociationSource(t, cfg, upstream.URL)

	initial, err := translator.EventAssociations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := initial.Categories["cards"]["101"]; !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("initial cards = %v", got)
	}

	failCards.Store(true)
	musicEventID.Store(10)
	translator.eventAssociationExpiresAt = time.Time{}
	refreshed, err := translator.EventAssociations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Categories["cards"]["101"]; !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("failed card refresh discarded stale data: %v", got)
	}
	if got := refreshed.Categories["music"]["501"]; !reflect.DeepEqual(got, []int{10}) {
		t.Fatalf("successful music refresh = %v", got)
	}
	remaining := time.Until(translator.eventAssociationExpiresAt)
	if remaining <= 0 || remaining > eventAssociationRetryTTL+time.Second {
		t.Fatalf("partial refresh retry window = %s", remaining)
	}
}

func TestEventAssociationsHonorsIndependentRequestContext(t *testing.T) {
	translator, _, cfg := openTestTranslator(t)
	var started atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		started.Add(1)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	configureEventAssociationSource(t, cfg, upstream.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := translator.EventAssociations(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("association context error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("association cancellation took %s", elapsed)
	}
	if started.Load() != 2 {
		t.Fatalf("parallel association requests started = %d", started.Load())
	}
}
