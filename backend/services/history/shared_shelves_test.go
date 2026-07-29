package history

import (
	"testing"
	"time"

	"novastream/models"
)

func TestAggregatePopularTitlesCountsUniqueEligibleProfiles(t *testing.T) {
	now := time.Now().UTC()
	service := &Service{
		watchHistory: map[string]map[string]models.WatchHistoryItem{
			"shared-a": {
				"episode-1": {
					MediaType: "episode", ItemID: "tvdb:10:s01e01", SeriesID: "tvdb:10",
					SeriesName: "Shared Show", Watched: true, WatchedAt: now,
				},
				"episode-2": {
					MediaType: "episode", ItemID: "tvdb:10:s01e02", SeriesID: "tvdb:10",
					SeriesName: "Shared Show", Watched: true, WatchedAt: now,
				},
			},
			"shared-b": {
				"episode-1": {
					MediaType: "episode", ItemID: "tvdb:10:s01e03", SeriesID: "tvdb:10",
					SeriesName: "Shared Show", Watched: true, WatchedAt: now,
				},
			},
			"private": {
				"movie": {
					MediaType: "movie", ItemID: "tmdb:99", Name: "Private Movie",
					Watched: true, WatchedAt: now,
				},
			},
		},
	}

	items := service.AggregatePopularTitles(map[string]bool{"shared-a": true, "shared-b": true}, 90)
	if len(items) != 1 {
		t.Fatalf("items = %d, want one shared title: %+v", len(items), items)
	}
	if items[0].ItemID != "tvdb:10" || items[0].MediaType != "series" || items[0].WatchCount != 2 {
		t.Fatalf("unexpected aggregate: %+v", items[0])
	}
}

func TestListRecentWatchesHonorsCapAndAnonymity(t *testing.T) {
	now := time.Now().UTC()
	service := &Service{
		watchHistory: map[string]map[string]models.WatchHistoryItem{
			"anonymous": {
				"new": {
					MediaType: "movie", ItemID: "tmdb:2", Name: "Newest",
					Watched: true, WatchedAt: now,
				},
				"old": {
					MediaType: "movie", ItemID: "tmdb:1", Name: "Older",
					Watched: true, WatchedAt: now.Add(-time.Hour),
				},
			},
		},
	}
	users := map[string]models.User{
		"anonymous": {
			ID: "anonymous", Name: "Secret",
			ActivityPrivacy: models.ActivityPrivacySharedAnonymous,
		},
	}

	items := service.ListRecentWatches(users, 90, 1)
	if len(items) != 1 {
		t.Fatalf("items = %d, want cap of one: %+v", len(items), items)
	}
	if !items[0].IsAnonymous || items[0].UserID != "" || items[0].UserName != "Fellow user" {
		t.Fatalf("anonymous identity leaked: %+v", items[0])
	}
	if items[0].Name != "Newest" {
		t.Fatalf("wrong capped item: %+v", items[0])
	}
}
