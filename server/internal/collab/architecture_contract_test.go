package collab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"
	"moesekai/server/internal/auth"
	"moesekai/server/internal/db"
	"moesekai/server/internal/editorgate"
	"moesekai/server/internal/model"
	"moesekai/server/internal/store"
)

type contractServiceFixture struct {
	service *Service
	claims  *auth.Claims
	bearer  string
}

func setupContractService(t *testing.T) contractServiceFixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "collaboration-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	lyricsStore := store.New(database)
	if err := lyricsStore.UpsertMusicCatalog([]store.MusicCatalogRecord{{MusicID: 42, JapaneseTitle: "協作曲"}}); err != nil {
		t.Fatal(err)
	}
	authService := auth.New(database, "collaboration-contract-secret-at-least-32-bytes", time.Hour)
	user, err := authService.CreateUser("editor", "strong-password-123", auth.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	bearer, _, err := authService.IssueToken(user)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := authService.VerifyToken(bearer)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := editorgate.New()
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(database, lyricsStore, authService, gate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	return contractServiceFixture{service: service, claims: claims, bearer: bearer}
}

func TestYjsUpgradeRejectsLongLivedCredentialsInTheQuery(t *testing.T) {
	fixture := setupContractService(t)
	for _, parameter := range []string{"token", "jwt", "access_token", "authorization"} {
		t.Run(parameter, func(t *testing.T) {
			ticket, err := fixture.service.IssueTicket(t.Context(), fixture.claims, fixture.bearer, 42, fixture.service.gate.Status())
			if err != nil {
				t.Fatal(err)
			}
			query := url.Values{"ticket": {ticket.Ticket}, parameter: {fixture.bearer}}
			request := httptest.NewRequest(http.MethodGet, "/yjs/lyrics/42?"+query.Encode(), nil)
			request.SetPathValue("musicId", "42")
			ctx, cancel := context.WithCancel(request.Context())
			request = request.WithContext(ctx)
			_, accepted := fixture.service.authorize(request)
			cancel()
			if accepted {
				t.Fatalf("Yjs upgrade accepted long-lived %s query credential", parameter)
			}
			replay := httptest.NewRequest(http.MethodGet, "/yjs/lyrics/42?ticket="+url.QueryEscape(ticket.Ticket), nil)
			replay.SetPathValue("musicId", "42")
			if _, accepted := fixture.service.authorize(replay); accepted {
				t.Fatalf("ticket survived rejected %s query credential attempt", parameter)
			}
		})
	}
}

func TestYgoCollaborationResourceLimitsRemainBounded(t *testing.T) {
	server := setupContractService(t).service.server
	if server.MaxConnections != 50 || server.MaxPeersPerRoom != 10 || server.MaxRooms != 256 || server.MaxResidentRooms != 128 {
		t.Fatalf("connection limits connections=%d peers=%d rooms=%d resident=%d",
			server.MaxConnections, server.MaxPeersPerRoom, server.MaxRooms, server.MaxResidentRooms)
	}
	if server.MaxMessageBytes != 8<<20+(64<<10) || server.MaxUpdateBytes != 8<<20 || maxDocumentUpdateBytes != 8<<20 {
		t.Fatalf("payload limits message=%d update=%d document=%d",
			server.MaxMessageBytes, server.MaxUpdateBytes, maxDocumentUpdateBytes)
	}
	if server.MessageRateLimit != 20 || server.MessageRateBurst != 40 || server.MaxAwarenessBytesPerRoom != 256<<10 ||
		server.MaxAwarenessClientsPerRoom != 256 || server.AwarenessExpiry != 90*time.Second || server.RoomIdleTimeout != 5*time.Minute ||
		server.MaxPendingItems != 100_000 || server.HandshakeTimeout != 10*time.Second || server.PersistCoalesceMaxWait != time.Second ||
		server.PeerWriteQueueSize != 256 || server.CompactEvery != 100 {
		t.Fatalf("rate/resource limits rate=%v burst=%d awarenessBytes=%d awarenessClients=%d expiry=%s idle=%s pending=%d handshake=%s coalesce=%s queue=%d compact=%d",
			server.MessageRateLimit, server.MessageRateBurst, server.MaxAwarenessBytesPerRoom,
			server.MaxAwarenessClientsPerRoom, server.AwarenessExpiry, server.RoomIdleTimeout, server.MaxPendingItems,
			server.HandshakeTimeout, server.PersistCoalesceMaxWait, server.PeerWriteQueueSize, server.CompactEvery)
	}
}

func TestAuthoritativeSeedUsesNestedSharedYTypes(t *testing.T) {
	update, err := documentUpdate(model.SongLyrics{
		MusicID: 42, Status: "draft", Revision: 0,
		Lines: []model.LyricLine{{
			ID: "line-1", Order: 0, Japanese: "歌", Chinese: "歌", English: "Song",
			Segments: []model.LyricSegment{{
				Text: "歌", PerformerIDs: []int{1}, Ruby: []model.LyricRubySpan{{Text: "歌", Reading: "うた"}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	document := crdt.New()
	if err := crdt.ApplyUpdateV1(document, update, nil); err != nil {
		t.Fatal(err)
	}
	root := document.GetMap("lyrics")
	linesValue, ok := root.Get("lines")
	if !ok {
		t.Fatal("authoritative seed omitted lines")
	}
	lines, ok := linesValue.(*crdt.YArray)
	if !ok {
		t.Fatalf("lines type = %T, want *crdt.YArray", linesValue)
	}
	line, ok := lines.Get(0).(*crdt.YMap)
	if !ok {
		t.Fatalf("line type = %T, want *crdt.YMap", lines.Get(0))
	}
	japanese, ok := line.Get("japanese")
	if !ok {
		t.Fatal("nested line omitted japanese")
	}
	if _, ok := japanese.(*crdt.YText); !ok {
		t.Fatalf("japanese type = %T, want *crdt.YText", japanese)
	}
	segmentsValue, ok := line.Get("segments")
	if !ok {
		t.Fatal("nested line omitted segments")
	}
	segments, ok := segmentsValue.(*crdt.YArray)
	if !ok {
		t.Fatalf("segments type = %T, want *crdt.YArray", segmentsValue)
	}
	segment, ok := segments.Get(0).(*crdt.YMap)
	if !ok {
		t.Fatalf("segment type = %T, want *crdt.YMap", segments.Get(0))
	}
	textValue, ok := segment.Get("text")
	if !ok {
		t.Fatal("nested segment omitted text")
	}
	if _, ok := textValue.(*crdt.YText); !ok {
		t.Fatalf("segment text type = %T, want *crdt.YText", textValue)
	}
}
