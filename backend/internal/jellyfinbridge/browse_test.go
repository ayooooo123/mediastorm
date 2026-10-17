package jellyfinbridge

import (
	"net/http"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

func TestGenreBrowsing(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.Watchlist.(*fakeWatchlist).items[0].Genres = []string{"Science Fiction", "Action"}
	tok := h.login("Living Room", "alice-pass")
	matrix := movieID(t, h, tok)

	listed := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?IncludeItemTypes=Movie&Recursive=true", tok, nil))
	if len(listed.Items) != 1 || len(listed.Items[0].GenreItems) != 2 {
		t.Fatalf("genre links missing: %+v", listed.Items)
	}
	genres := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Genres?IncludeItemTypes=Movie", tok, nil))
	if genres.TotalRecordCount != 2 || genres.Items[0].Name != "Action" || genres.Items[0].Type != "Genre" {
		t.Fatalf("genres = %+v", genres.Items)
	}
	action := genres.Items[0].ID
	if g := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+action, tok, nil)); g.Name != "Action" || g.Type != "Genre" {
		t.Fatalf("genre item = %+v", g)
	}
	movies := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?GenreIds="+action+"&IncludeItemTypes=Movie&Recursive=true", tok, nil))
	if movies.TotalRecordCount != 1 || movies.Items[0].ID != matrix {
		t.Fatalf("movies in genre = %+v", movies.Items)
	}
	if none := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?GenreIds=0123456789abcdef0123456789abcdef&IncludeItemTypes=Movie", tok, nil)); none.TotalRecordCount != 0 {
		t.Fatalf("unknown genre = %+v", none.Items)
	}
}

func TestPersonFilmography(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	person := h.srv.ids.put(refPerson, "tmdb:person:6384", refContext{Name: "Keanu Reeves"})

	p := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+person, tok, nil))
	if p.Type != "Person" || p.Overview != "Actor." || p.PremiereDate == nil || len(p.ProductionLocations) != 1 {
		t.Fatalf("person = %+v", p)
	}
	films := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?PersonIds="+person+"&IncludeItemTypes=Movie,Series&Recursive=true&SortBy=ProductionYear", tok, nil))
	if films.TotalRecordCount != 2 || films.Items[0].Name != "The Matrix" || films.Items[1].Name != "John Wick" {
		t.Fatalf("filmography = %+v", films.Items)
	}
	if shows := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?PersonIds="+person+"&IncludeItemTypes=Series", tok, nil)); shows.TotalRecordCount != 0 {
		t.Fatalf("series filmography = %+v", shows.Items)
	}
}

func TestStudiosFromSeriesNetworks(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	got := h.srv.studioItems(" HBO ")
	if len(got) != 1 || got[0].Name != "HBO" {
		t.Fatalf("studio items = %+v", got)
	}
	if studio := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+got[0].ID, tok, nil)); studio.Type != "Studio" || studio.Name != "HBO" {
		t.Fatalf("studio item = %+v", studio)
	}
	if h.srv.studioItems("") != nil {
		t.Fatal("empty network linked as studio")
	}
	if names := h.srv.resolveStudioIDs([]string{got[0].ID, "0123456789abcdef0123456789abcdef"}); len(names) != 1 || names[0] != "HBO" {
		t.Fatalf("resolved studios = %v", names)
	}
}

func TestEpisodeSeriesForLocalEpisodes(t *testing.T) {
	local := itemRef{Kind: refLocal, Ctx: refContext{ViewKey: "show", Season: 1, Episode: 2, TitleID: "tmdb:tv:1399", ExternalIDs: map[string]string{"imdb": "tt0944947"}}}
	series, ok := episodeSeries(local)
	if !ok || series.ExternalIDs["imdb"] != "tt0944947" {
		t.Fatalf("local episode series = %+v %v", series, ok)
	}
	if _, ok := episodeSeries(itemRef{Kind: refLocal, Ctx: refContext{Season: 1, Episode: 2}}); ok {
		t.Fatal("unmatched local file treated as an episode")
	}
	if !playableVOD(refLocal) || playableVOD(refSeries) {
		t.Fatal("playableVOD")
	}
}
