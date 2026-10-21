package handlers

import (
	"context"
	"strings"
	"sync"

	"novastream/models"
)

// shelfLogoProvider is the optional logo lookup that fetches the shelf images
// (one TMDB request, no image downloads) when no logo is cached.
type shelfLogoProvider interface {
	ShelfLogo(ctx context.Context, mediaType string, tmdbID int64) *models.Image
}

// fillMissingTitleLogos gives cards without a logo the cached one and, unless
// artwork is deferred, fetches the rest with bounded fan-out. Cards built from
// lists the client never opened (recommendations, saved lists) otherwise show
// the name as text. Returns true while logos are still to be fetched.
func fillMissingTitleLogos(ctx context.Context, items []models.TrendingItem, meta metadataService, deferArtwork bool) bool {
	if meta == nil {
		return false
	}
	cached, _ := meta.(cachedLogoProvider)
	missing := make([]int, 0, len(items))
	for i := range items {
		title := &items[i].Title
		if title.Logo != nil && strings.TrimSpace(title.Logo.URL) != "" {
			continue
		}
		if title.TMDBID <= 0 && title.TVDBID <= 0 {
			continue
		}
		if cached != nil {
			if url := cached.GetCachedLogoURL(title.MediaType, title.TMDBID, title.TVDBID); url != "" {
				title.Logo = &models.Image{URL: url, Type: "logo"}
				continue
			}
		}
		if title.TMDBID > 0 {
			missing = append(missing, i)
		}
	}
	fetcher, ok := meta.(shelfLogoProvider)
	if !ok || len(missing) == 0 {
		return false
	}
	if deferArtwork {
		return true
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
			title := &items[index].Title
			if logo := fetcher.ShelfLogo(ctx, title.MediaType, title.TMDBID); logo != nil {
				title.Logo = logo
			}
		}(index)
	}
	wg.Wait()
	return false
}
