package detection

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChangeType describes a store mutation.
type ChangeType string

const (
	ChangeInserted ChangeType = "inserted"
	ChangeUpdated  ChangeType = "updated"
	ChangeRemoved  ChangeType = "removed"
	ChangeCleared  ChangeType = "cleared"
)

var (
	ErrVideoNotFound     = errors.New("detected video not found")
	ErrCandidateNotFound = errors.New("detected resource candidate not found")
)

// Snapshot is the private authoritative domain list.
type Snapshot struct {
	Revision uint64
	Videos   []Video
}

// Change carries the mutation type and complete authoritative snapshot.
type Change struct {
	Type      ChangeType
	ChangedID string
	Snapshot  Snapshot
}

func (snapshot Snapshot) Public() PublicSnapshot {
	videos := make([]VideoDTO, 0, len(snapshot.Videos))
	for _, video := range snapshot.Videos {
		videos = append(videos, video.publicDTO())
	}
	return PublicSnapshot{Revision: snapshot.Revision, Videos: videos}
}

func (change Change) Public() PublicChange {
	return PublicChange{Type: change.Type, ChangedID: change.ChangedID, Snapshot: change.Snapshot.Public()}
}

// Store owns detected-media identity, merge, ordering, retention, and private
// candidate resolution.
type Store interface {
	Upsert(ctx context.Context, video Video) (Change, error)
	List(ctx context.Context) (Snapshot, error)
	Remove(ctx context.Context, id string) (Change, error)
	Clear(ctx context.Context) (Change, error)
	ResolveCandidate(ctx context.Context, videoID, candidateID string) (Video, Resource, error)
}

// MemoryStore is a bounded, session-only Store.
type MemoryStore struct {
	mu       sync.RWMutex
	capacity int
	now      func() time.Time
	revision uint64
	videos   map[string]Video
}

// NewMemoryStore returns a bounded in-memory store. Non-positive capacities
// use the conservative session default of 100 records.
func NewMemoryStore(capacity int) *MemoryStore {
	if capacity <= 0 {
		capacity = 100
	}
	return &MemoryStore{capacity: capacity, now: time.Now, videos: make(map[string]Video, capacity)}
}

func (s *MemoryStore) Upsert(ctx context.Context, video Video) (Change, error) {
	if err := ctx.Err(); err != nil {
		return Change{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Change{}, err
	}
	// Allocate ingestion time inside the same serialization boundary as the
	// mutation. Concurrent callbacks therefore cannot acquire timestamps in one
	// order and commit in another.
	now := s.now().UTC()
	video = cloneVideo(video)
	video = normalizeVideo(video, now)
	if len(video.Candidates) == 0 {
		return Change{}, errors.New("detected video has no media candidate")
	}

	changeType := ChangeInserted
	if previous, exists := s.videos[video.ID]; exists {
		video = mergeVideo(previous, video, now)
		changeType = ChangeUpdated
	}
	if video.IsCurrent {
		for id, existing := range s.videos {
			if id != video.ID && existing.Source == video.Source && existing.Platform == video.Platform && existing.IsCurrent {
				existing.IsCurrent = false
				s.videos[id] = existing
			}
		}
	}
	s.videos[video.ID] = cloneVideo(video)
	s.evictLocked()
	s.revision++
	return Change{Type: changeType, ChangedID: video.ID, Snapshot: s.snapshotLocked()}, nil
}

func (s *MemoryStore) List(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(), nil
}

func (s *MemoryStore) Remove(ctx context.Context, id string) (Change, error) {
	if err := ctx.Err(); err != nil {
		return Change{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.videos, strings.TrimSpace(id))
	s.revision++
	return Change{Type: ChangeRemoved, ChangedID: strings.TrimSpace(id), Snapshot: s.snapshotLocked()}, nil
}

func (s *MemoryStore) Clear(ctx context.Context) (Change, error) {
	if err := ctx.Err(); err != nil {
		return Change{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.videos = make(map[string]Video, s.capacity)
	s.revision++
	return Change{Type: ChangeCleared, Snapshot: s.snapshotLocked()}, nil
}

func (s *MemoryStore) ResolveCandidate(ctx context.Context, videoID, candidateID string) (Video, Resource, error) {
	if err := ctx.Err(); err != nil {
		return Video{}, Resource{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	video, exists := s.videos[strings.TrimSpace(videoID)]
	if !exists {
		return Video{}, Resource{}, ErrVideoNotFound
	}
	candidateID = strings.TrimSpace(candidateID)
	for _, resource := range video.Candidates {
		if resource.ID == candidateID || (candidateID == "" && resource.Default) {
			return cloneVideo(video), cloneResource(resource), nil
		}
	}
	return Video{}, Resource{}, ErrCandidateNotFound
}

func (s *MemoryStore) evictLocked() {
	if len(s.videos) <= s.capacity {
		return
	}
	snapshot := s.snapshotLocked()
	for _, video := range snapshot.Videos[s.capacity:] {
		delete(s.videos, video.ID)
	}
}

func (s *MemoryStore) snapshotLocked() Snapshot {
	result := make([]Video, 0, len(s.videos))
	for _, video := range s.videos {
		result = append(result, cloneVideo(video))
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].LastSeenAt.Equal(result[j].LastSeenAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].LastSeenAt.After(result[j].LastSeenAt)
	})
	return Snapshot{Revision: s.revision, Videos: result}
}

func mergeVideo(previous, next Video, now time.Time) Video {
	merged := cloneVideo(previous)
	merged.LastSeenAt = latestTime(previous.LastSeenAt, next.LastSeenAt, now)
	if merged.DetectedAt.IsZero() || (!next.DetectedAt.IsZero() && next.DetectedAt.Before(merged.DetectedAt)) {
		merged.DetectedAt = next.DetectedAt
	}

	merged.Title = preferValid(merged.Title, next.Title, isBadTitle)
	merged.Author = preferValid(merged.Author, next.Author, isBadAuthor)
	merged.CoverURL = preferExisting(merged.CoverURL, next.CoverURL)
	merged.PageURL = preferExisting(merged.PageURL, next.PageURL)
	merged.AuthorAvatar = preferExisting(merged.AuthorAvatar, next.AuthorAvatar)
	merged.Candidates = mergeResources(merged.Candidates, next.Candidates)
	ensureDefaultCandidate(merged.Candidates)

	if merged.DurationMS <= 0 && next.DurationMS > 0 {
		merged.DurationMS = next.DurationMS
	}
	if merged.Width <= 0 && next.Width > 0 {
		merged.Width = next.Width
	}
	if merged.Height <= 0 && next.Height > 0 {
		merged.Height = next.Height
	}
	merged.Metadata = mergeMap(merged.Metadata, next.Metadata)
	// Older callback shapes may omit the current marker (zero value false).
	// Preserve the marker until another item from the same source/platform is
	// explicitly marked current, at which point Upsert clears the old one.
	merged.IsCurrent = previous.IsCurrent || next.IsCurrent
	return merged
}

func latestTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}

func mergeResources(existing, incoming []Resource) []Resource {
	result := make([]Resource, 0, len(existing)+len(incoming))
	idIndex := make(map[string]int, len(existing)+len(incoming))
	appendResource := func(resource Resource) {
		resource.URL = strings.TrimSpace(resource.URL)
		resource.ID = strings.TrimSpace(resource.ID)
		if resource.URL == "" || resource.ID == "" {
			return
		}
		resource.Headers = cloneMap(resource.Headers)
		idIndex[resource.ID] = len(result)
		result = append(result, resource)
	}
	for _, resource := range existing {
		appendResource(resource)
	}

	incomingHasDefault := false
	for _, resource := range incoming {
		incomingHasDefault = incomingHasDefault || resource.Default
	}
	if incomingHasDefault {
		for i := range result {
			result[i].Default = false
		}
	}

	for _, resource := range incoming {
		resource.URL = strings.TrimSpace(resource.URL)
		resource.ID = strings.TrimSpace(resource.ID)
		if resource.URL == "" || resource.ID == "" {
			continue
		}
		pos, found := idIndex[resource.ID]
		if !found {
			pos, found = findURLFallback(result, resource)
		}
		if !found {
			appendResource(resource)
			continue
		}

		current := &result[pos]
		oldID := current.ID
		// A stable adapter rendition ID outranks a prior URL-derived fallback.
		if resource.ExplicitID && !current.ExplicitID {
			current.ID = resource.ID
			current.ExplicitID = true
			delete(idIndex, oldID)
		}
		// Same ID, or secondarily the same normalized URL, denotes the same
		// rendition. Prefer newest non-empty execution data so signatures rotate.
		current.URL = preferIncoming(current.URL, resource.URL)
		current.DecodeKey = preferIncoming(current.DecodeKey, resource.DecodeKey)
		current.FileFormat = preferIncoming(current.FileFormat, resource.FileFormat)
		current.MimeType = preferIncoming(current.MimeType, resource.MimeType)
		current.Quality = preferIncoming(current.Quality, resource.Quality)
		if resource.Width > 0 {
			current.Width = resource.Width
		}
		if resource.Height > 0 {
			current.Height = resource.Height
		}
		if resource.DurationMS > 0 {
			current.DurationMS = resource.DurationMS
		}
		if resource.SizeBytes > 0 {
			current.SizeBytes = resource.SizeBytes
		}
		current.Headers = mergeMap(current.Headers, resource.Headers)
		if incomingHasDefault && resource.Default {
			current.Default = true
		}
		idIndex[current.ID] = pos
	}
	return result
}

func findURLFallback(existing []Resource, incoming Resource) (int, bool) {
	incomingURL := normalizeURL(incoming.URL)
	if incomingURL == "" {
		return 0, false
	}
	for i, current := range existing {
		// Two distinct explicit rendition IDs intentionally may share one media
		// URL (for example WeChat original/hd/sd). URL is only a secondary
		// identity when at least one side lacks that stronger adapter identity.
		if current.ExplicitID && incoming.ExplicitID {
			continue
		}
		if normalizeURL(current.URL) != incomingURL {
			continue
		}
		currentFormat := strings.TrimSpace(current.FileFormat)
		incomingFormat := strings.TrimSpace(incoming.FileFormat)
		if currentFormat != "" && incomingFormat != "" && currentFormat != incomingFormat {
			continue
		}
		return i, true
	}
	return 0, false
}

func preferExisting(current, candidate string) string {
	if strings.TrimSpace(current) == "" && strings.TrimSpace(candidate) != "" {
		return candidate
	}
	return current
}

func preferIncoming(current, candidate string) string {
	if strings.TrimSpace(candidate) != "" {
		return candidate
	}
	return current
}

func preferValid(current, candidate string, invalid func(string) bool) string {
	if invalid(current) && !invalid(candidate) {
		return candidate
	}
	return current
}

func isBadTitle(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "unknown title" || value == "未知标题" {
		return true
	}
	bad := []string{"this is a modal window", "beginning of dialog window", "escape will cancel", "play video", "video player is loading", "restore all settings to the default values"}
	for _, marker := range bad {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func isBadAuthor(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "unknown author" || value == "未知作者" {
		return true
	}
	return strings.Contains(value, "play video") || strings.Contains(value, "beginning of dialog window")
}

func mergeMap(existing, incoming map[string]string) map[string]string {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	result := cloneMap(existing)
	if result == nil {
		result = make(map[string]string, len(incoming))
	}
	for key, value := range incoming {
		if strings.TrimSpace(value) != "" {
			result[key] = value
		}
	}
	return result
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneResource(resource Resource) Resource {
	resource.Headers = cloneMap(resource.Headers)
	return resource
}

func cloneVideo(video Video) Video {
	video.Candidates = append([]Resource(nil), video.Candidates...)
	for i := range video.Candidates {
		video.Candidates[i] = cloneResource(video.Candidates[i])
	}
	video.Metadata = cloneMap(video.Metadata)
	return video
}
