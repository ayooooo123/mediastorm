package handlers

import (
	"context"
	"strings"
	"sync"

	"novastream/models"
)

// Older list entries may have lost their text poster on save. Keep the first
// shelf response fast, then recover missing posters during artwork completion.
func enrichCustomListShelfTextPosters(ctx context.Context, items []models.TrendingItem, meta metadataService, deferArtwork bool) bool {
	if meta == nil {
		return false
	}
	missing := make([]int, 0, len(items))
	for i, item := range items {
		if item.Title.TextPoster != nil && strings.TrimSpace(item.Title.TextPoster.URL) != "" {
			continue
		}
		if item.Title.TMDBID > 0 || item.Title.TVDBID > 0 {
			missing = append(missing, i)
		}
	}
	if deferArtwork {
		return len(missing) > 0
	}
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, index := range missing {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			meta.ApplyLocalizedArtwork(ctx, &items[index].Title)
		}(index)
	}
	wg.Wait()
	return false
}
