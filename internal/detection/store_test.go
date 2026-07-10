package detection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryStoreMergesSameIDWithoutLosingPrivateCandidateData(t *testing.T) {
	store := NewMemoryStore(10)
	clock := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	id := StableID(Identity{Source: SourceWeChatProxy, Platform: "wechat", PlatformContentID: "finder-1"})

	first, err := store.Upsert(context.Background(), Video{
		ID: id, Source: SourceWeChatProxy, Platform: "wechat", Title: "A title",
		Candidates: []Resource{{
			ID: "720", URL: "https://media.example/video.mp4?token=old",
			DecodeKey: "secret-old", Quality: "720p", Default: true,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, ChangeInserted, first.Type)
	require.Equal(t, uint64(1), first.Snapshot.Revision)
	require.Len(t, first.Snapshot.Videos, 1)
	firstDetectedAt := first.Snapshot.Videos[0].DetectedAt

	clock = clock.Add(time.Minute)
	second, err := store.Upsert(context.Background(), Video{
		ID: id, Source: SourceWeChatProxy, Platform: "wechat", Author: "Creator",
		Candidates: []Resource{{
			ID: "720", URL: "https://media.example/video.mp4?token=new",
			DecodeKey: "secret-new", MimeType: "video/mp4", SizeBytes: 42,
			Headers: map[string]string{"Referer": "https://example.test/"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, ChangeUpdated, second.Type)
	require.Equal(t, uint64(2), second.Snapshot.Revision)
	require.Len(t, second.Snapshot.Videos, 1)
	merged := second.Snapshot.Videos[0]
	require.Equal(t, "A title", merged.Title)
	require.Equal(t, "Creator", merged.Author)
	require.Equal(t, firstDetectedAt, merged.DetectedAt)
	require.Equal(t, clock, merged.LastSeenAt)
	require.Len(t, merged.Candidates, 1)
	require.Equal(t, "https://media.example/video.mp4?token=new", merged.Candidates[0].URL)
	require.Equal(t, "secret-new", merged.Candidates[0].DecodeKey)
	require.Equal(t, int64(42), merged.Candidates[0].SizeBytes)
	require.Equal(t, "https://example.test/", merged.Candidates[0].Headers["Referer"])
}

func TestMemoryStoreCandidateFallbackDeduplicatesNormalizedURLAndFormat(t *testing.T) {
	store := NewMemoryStore(10)
	first, err := store.Upsert(context.Background(), Video{
		ID: "one", Source: SourceWeChatProxy, Platform: "wechat",
		Candidates: []Resource{{URL: "HTTPS://MEDIA.EXAMPLE:443/video.mp4?b=2&a=1", FileFormat: "hd", Quality: "720p"}},
	})
	require.NoError(t, err)
	candidateID := first.Snapshot.Videos[0].Candidates[0].ID

	second, err := store.Upsert(context.Background(), Video{
		ID: "one", Source: SourceWeChatProxy, Platform: "wechat",
		Candidates: []Resource{{URL: "https://media.example/video.mp4?a=1&b=2", FileFormat: "hd", SizeBytes: 99}},
	})
	require.NoError(t, err)
	require.Len(t, second.Snapshot.Videos[0].Candidates, 1)
	require.Equal(t, candidateID, second.Snapshot.Videos[0].Candidates[0].ID)
	require.Equal(t, int64(99), second.Snapshot.Videos[0].Candidates[0].SizeBytes)
}

func TestMemoryStoreUpgradesURLDerivedCandidateToExplicitIDAndNewDefault(t *testing.T) {
	store := NewMemoryStore(10)
	first, err := store.Upsert(context.Background(), Video{
		ID: "one", Source: SourceWeChatProxy, Platform: "wechat",
		Candidates: []Resource{
			{URL: "https://media.example/video.mp4?a=1&b=2", Quality: "SD", Default: true},
			{ID: "legacy-hd", URL: "https://media.example/hd.mp4", Quality: "HD"},
		},
	})
	require.NoError(t, err)
	oldFallbackID := first.Snapshot.Videos[0].Candidates[0].ID

	second, err := store.Upsert(context.Background(), Video{
		ID: "one", Source: SourceWeChatProxy, Platform: "wechat",
		Candidates: []Resource{
			{ID: "original", URL: "HTTPS://MEDIA.EXAMPLE:443/video.mp4?b=2&a=1", Quality: "原始画质", Default: true},
			{ID: "legacy-hd", URL: "https://media.example/hd.mp4", Quality: "HD"},
		},
	})
	require.NoError(t, err)
	candidates := second.Snapshot.Videos[0].Candidates
	require.Len(t, candidates, 2)
	require.NotEqual(t, oldFallbackID, candidates[0].ID, "explicit rendition identity must replace the URL fallback")
	require.True(t, candidates[0].ExplicitID)
	require.True(t, candidates[0].Default, "new explicitly selected default must take over")
	require.False(t, candidates[1].Default)
}

func TestMemoryStoreSortsEvictsRemovesAndClearsWithRevisions(t *testing.T) {
	store := NewMemoryStore(2)
	clock := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { clock = clock.Add(time.Second); return clock }

	var latest Change
	for _, id := range []string{"one", "two", "three"} {
		var err error
		latest, err = store.Upsert(context.Background(), testVideo(id))
		require.NoError(t, err)
	}
	require.Equal(t, uint64(3), latest.Snapshot.Revision)
	require.Equal(t, []string{"three", "two"}, videoIDs(latest.Snapshot.Videos))

	snapshot, err := store.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, latest.Snapshot, snapshot)

	removed, err := store.Remove(context.Background(), "two")
	require.NoError(t, err)
	require.Equal(t, uint64(4), removed.Snapshot.Revision)
	require.Equal(t, []string{"three"}, videoIDs(removed.Snapshot.Videos))

	cleared, err := store.Clear(context.Background())
	require.NoError(t, err)
	require.Equal(t, ChangeCleared, cleared.Type)
	require.Equal(t, uint64(5), cleared.Snapshot.Revision)
	require.Empty(t, cleared.Snapshot.Videos)
}

func TestMemoryStoreLateCallbackCannotRegressLastSeenOrOrdering(t *testing.T) {
	store := NewMemoryStore(10)
	clock := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	newer := testVideo("same")
	newer.LastSeenAt = clock.Add(time.Hour)
	_, err := store.Upsert(context.Background(), newer)
	require.NoError(t, err)

	clock = clock.Add(-time.Hour)
	late := testVideo("same")
	late.LastSeenAt = clock.Add(-time.Hour)
	change, err := store.Upsert(context.Background(), late)
	require.NoError(t, err)
	require.Equal(t, newer.LastSeenAt, change.Snapshot.Videos[0].LastSeenAt)
}

func TestStableIDIsSourceScopedAndConservative(t *testing.T) {
	a := StableID(Identity{Source: SourceWeChatProxy, Platform: "wechat", PageURL: "HTTPS://EXAMPLE.COM:443/watch?b=2&a=1#x"})
	b := StableID(Identity{Source: SourceWeChatProxy, Platform: "wechat", PageURL: "https://example.com/watch?a=1&b=2"})
	c := StableID(Identity{Source: Source("another"), Platform: "wechat", PageURL: "https://example.com/watch?a=1&b=2"})
	d := StableID(Identity{Source: SourceWeChatProxy, Platform: "wechat", PageURL: "https://example.com/watch?a=1&b=3"})
	require.Equal(t, a, b)
	require.NotEqual(t, a, c)
	require.NotEqual(t, a, d, "unknown signed parameters must not be stripped")

	rotatedA := StableID(Identity{Source: SourceWeChatProxy, Platform: "wechat", PlatformContentID: "content-7", PrimaryURL: "https://cdn/a?token=1"})
	rotatedB := StableID(Identity{Source: SourceWeChatProxy, Platform: "wechat", PlatformContentID: "content-7", PrimaryURL: "https://cdn/a?token=2"})
	require.Equal(t, rotatedA, rotatedB)
	require.NotContains(t, rotatedA, "content-7", "opaque public IDs must hash private platform identity material")
}

func TestCurrentItemProducesAuthoritativeSnapshot(t *testing.T) {
	store := NewMemoryStore(10)
	one := testVideo("one")
	one.IsCurrent = true
	_, err := store.Upsert(context.Background(), one)
	require.NoError(t, err)
	incomplete := testVideo("one")
	change, err := store.Upsert(context.Background(), incomplete)
	require.NoError(t, err)
	require.True(t, change.Snapshot.Videos[0].IsCurrent, "an omitted false marker must not clear the current item")
	two := testVideo("two")
	two.IsCurrent = true
	change, err = store.Upsert(context.Background(), two)
	require.NoError(t, err)
	require.Len(t, change.Snapshot.Videos, 2)
	for _, video := range change.Snapshot.Videos {
		if video.ID == "one" {
			require.False(t, video.IsCurrent)
		}
	}
}

func TestPublicProjectionNeverSerializesExecutionSecrets(t *testing.T) {
	store := NewMemoryStore(10)
	change, err := store.Upsert(context.Background(), Video{
		ID: "fixture", Source: Source("fixture_adapter"), Platform: "fixture", Title: "Fixture",
		Candidates: []Resource{{
			ID: "original", URL: "https://private.example/video.mp4?signature=top-secret",
			Headers:   map[string]string{"Authorization": "Bearer top-secret"},
			DecodeKey: "decode-key-top-secret", Default: true,
		}},
	})
	require.NoError(t, err)

	payload, err := json.Marshal(change.Public())
	require.NoError(t, err)
	serialized := strings.ToLower(string(payload))
	for _, forbidden := range []string{"private.example", "top-secret", "decode-key", "authorization", "decodekey", "headers", `"url"`} {
		require.NotContains(t, serialized, forbidden)
	}
	require.Contains(t, serialized, `"fixture_adapter"`)
	require.Contains(t, serialized, `"encrypted":true`)

	video, candidate, err := store.ResolveCandidate(context.Background(), "fixture", "")
	require.NoError(t, err)
	require.Equal(t, "Fixture", video.Title)
	require.Equal(t, "decode-key-top-secret", candidate.DecodeKey)
	require.Equal(t, "Bearer top-secret", candidate.Headers["Authorization"])
}

func TestPublicProjectionHashesSensitivePlatformContentID(t *testing.T) {
	secret := "encfilekey:real-secret-value:m:private-token"
	store := NewMemoryStore(10)
	change, err := store.Upsert(context.Background(), Video{
		ID:     StableID(Identity{Source: SourceWeChatProxy, Platform: "wechat", PlatformContentID: secret}),
		Source: SourceWeChatProxy, Platform: "wechat", Title: "Fixture",
		Candidates: []Resource{{ID: "original", URL: "https://media.example/video.mp4", Default: true}},
	})
	require.NoError(t, err)
	payload, err := json.Marshal(change.Public())
	require.NoError(t, err)
	require.NotContains(t, string(payload), secret)
	require.NotContains(t, string(payload), "real-secret-value")
}

func testVideo(id string) Video {
	return Video{
		ID: id, Source: SourceWeChatProxy, Platform: "wechat",
		Candidates: []Resource{{ID: "original", URL: "https://example.com/" + id, Default: true}},
	}
}

func videoIDs(videos []Video) []string {
	result := make([]string, len(videos))
	for i, video := range videos {
		result[i] = video.ID
	}
	return result
}
