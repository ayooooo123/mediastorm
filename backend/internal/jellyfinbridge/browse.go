package jellyfinbridge

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

const (
	refGenre  = "genre"
	refStudio = "studio"
)

// genreID registers a genre name and returns its GUID. Genres are keyed by
// lowercased name: libraries mix TMDB and watchlist spellings.
func (s *Server) genreID(name string) string {
	return s.ids.put(refGenre, "genre:"+strings.ToLower(strings.TrimSpace(name)), refContext{Name: strings.TrimSpace(name)})
}

func (s *Server) genreItems(names []string) []dto.NameGuidPair {
	if len(names) == 0 {
		return nil
	}
	out := make([]dto.NameGuidPair, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			out = append(out, dto.NameGuidPair{Name: name, ID: s.genreID(name)})
		}
	}
	return out
}

func (s *Server) genreItem(id string, ref itemRef) dto.BaseItemDto {
	return dto.BaseItemDto{
		Name: ref.Ctx.Name, ServerID: s.serverID, ID: normalizeID(id), Type: "Genre",
		IsFolder: boolPtr(true), LocationType: dto.LocationFileSystem, SortName: sortName(ref.Ctx.Name),
	}
}

// studioItems links a series network as its studio.
func (s *Server) studioItems(network string) []dto.NameGuidPair {
	if network = strings.TrimSpace(network); network == "" {
		return nil
	}
	id := s.ids.put(refStudio, "studio:"+strings.ToLower(network), refContext{Name: network})
	return []dto.NameGuidPair{{Name: network, ID: id}}
}

func (s *Server) studioItem(id string, ref itemRef) dto.BaseItemDto {
	return dto.BaseItemDto{
		Name: ref.Ctx.Name, ServerID: s.serverID, ID: normalizeID(id), Type: "Studio",
		IsFolder: boolPtr(true), LocationType: dto.LocationFileSystem, SortName: sortName(ref.Ctx.Name),
	}
}

// resolveStudioIDs turns StudioIds into studio names.
func (s *Server) resolveStudioIDs(ids []string) []string {
	var names []string
	for _, id := range ids {
		if ref, ok := s.ids.get(normalizeID(id)); ok && ref.Kind == refStudio && ref.Ctx.Name != "" {
			names = append(names, ref.Ctx.Name)
		}
	}
	return names
}

// handleStudios serves /Studios from the studios the listed items carry
// (networks of series whose metadata is known).
func (s *Server) handleStudios(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	q := parseItemQuery(r)
	source := itemQuery{ParentID: q.ParentID, IncludeTypes: q.IncludeTypes, ExcludeTypes: q.ExcludeTypes, Recursive: true}
	if source.ParentID == "" && len(source.IncludeTypes) == 0 {
		source.IncludeTypes = []string{dto.KindMovie, dto.KindSeries}
	}
	seen := map[string]bool{}
	var items []dto.BaseItemDto
	for _, item := range s.queryItems(r.Context(), p, source) {
		for _, studio := range item.Studios {
			if !seen[studio.ID] {
				seen[studio.ID] = true
				if ref, ok := s.ids.get(studio.ID); ok {
					items = append(items, s.studioItem(studio.ID, ref))
				}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SortName < items[j].SortName })
	writeJSON(w, http.StatusOK, s.page(items, itemQuery{StartIndex: q.StartIndex, Limit: q.Limit}))
}

// resolveGenreIDs turns GenreIds into the genre names items carry.
func (s *Server) resolveGenreIDs(ids []string) []string {
	var names []string
	for _, id := range ids {
		if ref, ok := s.ids.get(normalizeID(id)); ok && ref.Kind == refGenre && ref.Ctx.Name != "" {
			names = append(names, ref.Ctx.Name)
		}
	}
	return names
}

// handleGenres serves /Genres: the genres of the items a library (or, with
// no ParentId, the profile's movies and shows) holds.
func (s *Server) handleGenres(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	q := parseItemQuery(r)
	paging := itemQuery{StartIndex: q.StartIndex, Limit: q.Limit, SearchTerm: q.SearchTerm}
	source := itemQuery{ParentID: q.ParentID, IncludeTypes: q.IncludeTypes, ExcludeTypes: q.ExcludeTypes, Recursive: true}
	if source.ParentID == "" && len(source.IncludeTypes) == 0 {
		source.IncludeTypes = []string{dto.KindMovie, dto.KindSeries}
	}
	counts := map[string]int{}
	names := map[string]string{}
	for _, item := range s.queryItems(r.Context(), p, source) {
		for _, name := range item.Genres {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			if _, ok := names[key]; !ok {
				names[key] = strings.TrimSpace(name)
			}
			counts[key]++
		}
	}
	items := make([]dto.BaseItemDto, 0, len(names))
	for key, name := range names {
		if paging.SearchTerm != "" && !strings.Contains(key, strings.ToLower(paging.SearchTerm)) {
			continue
		}
		id := s.genreID(name)
		item := s.genreItem(id, itemRef{Kind: refGenre, Ctx: refContext{Name: name}})
		item.ChildCount = intPtr(counts[key])
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SortName < items[j].SortName })
	writeJSON(w, http.StatusOK, s.page(items, paging))
}

// personTitles answers /Items?PersonIds=: the people's TMDB filmography,
// with kids-profile rules applied.
func (s *Server) personTitles(ctx context.Context, p *principal, u *userState, q itemQuery) []dto.BaseItemDto {
	if s.deps.Metadata == nil || isCuratedKidsProfile(p.Profile) {
		return nil
	}
	seen := map[string]bool{}
	var titles []titleInfo
	for _, id := range q.PersonIDs {
		ref, ok := s.ids.get(normalizeID(id))
		if !ok || ref.Kind != refPerson {
			continue
		}
		personID, err := strconv.ParseInt(strings.TrimPrefix(ref.Source, "tmdb:person:"), 10, 64)
		if err != nil || personID <= 0 {
			continue
		}
		details, err := s.deps.Metadata.PersonDetails(ctx, personID)
		if err != nil || details == nil {
			continue
		}
		film := details.Filmography
		if p.Profile.IsKidsProfile {
			film = append([]models.Title(nil), film...)
			s.deps.Metadata.EnrichTitleCertifications(ctx, film)
		}
		for _, title := range film {
			t := titleFromMetadata(title)
			if t.MediaType != "movie" && t.MediaType != "series" || !q.wants(kindForMediaType(t.MediaType)) {
				continue
			}
			if key := t.key(); !seen[key] {
				seen[key] = true
				titles = append(titles, t)
			}
		}
	}
	out := make([]dto.BaseItemDto, 0, len(titles))
	for _, t := range s.filterForProfile(ctx, p.Profile, titles) {
		out = append(out, s.titleItem(u, t))
	}
	return out
}

// personItem builds a person; detailed adds the TMDB biography.
func (s *Server) personItem(ctx context.Context, id string, ref itemRef, detailed bool) dto.BaseItemDto {
	item := dto.BaseItemDto{
		Name: ref.Ctx.Name, ServerID: s.serverID, ID: normalizeID(id), Type: "Person",
		ImageTags: imageTagsFor(ref.Ctx.Poster), LocationType: dto.LocationFileSystem,
	}
	if !detailed || s.deps.Metadata == nil {
		return item
	}
	personID, err := strconv.ParseInt(strings.TrimPrefix(ref.Source, "tmdb:person:"), 10, 64)
	if err != nil || personID <= 0 {
		return item
	}
	if details, err := s.deps.Metadata.PersonDetails(ctx, personID); err == nil && details != nil {
		item.Overview = details.Person.Biography
		item.PremiereDate = parseDate(details.Person.Birthday)
		item.EndDate = parseDate(details.Person.Deathday)
		if place := strings.TrimSpace(details.Person.PlaceOfBirth); place != "" {
			item.ProductionLocations = []string{place}
		}
		item.ProviderIDs = map[string]string{"Tmdb": strconv.FormatInt(personID, 10)}
	}
	return item
}
