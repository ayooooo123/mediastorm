package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"novastream/models"
)

func TestNormalizeStremioManifestInput(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantManifest string
		wantBase     string
	}{
		{
			name:         "base URL",
			input:        "https://addon.example/config/abc/",
			wantManifest: "https://addon.example/config/abc/manifest.json",
			wantBase:     "https://addon.example/config/abc",
		},
		{
			name:         "stremio install URL",
			input:        "stremio://addon.example/config/abc/manifest.json",
			wantManifest: "https://addon.example/config/abc/manifest.json",
			wantBase:     "https://addon.example/config/abc",
		},
		{
			name:         "Stremio Web URL",
			input:        "https://web.stremio.com/#/addons?addon=https%3A%2F%2Faddon.example%2Fmanifest.json",
			wantManifest: "https://addon.example/manifest.json",
			wantBase:     "https://addon.example",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, base, err := normalizeStremioManifestInput(test.input)
			if err != nil {
				t.Fatalf("normalizeStremioManifestInput: %v", err)
			}
			if manifest != test.wantManifest || base != test.wantBase {
				t.Fatalf("got manifest=%q base=%q, want manifest=%q base=%q", manifest, base, test.wantManifest, test.wantBase)
			}
		})
	}
}

func TestStremioManifestDiscoversMovieAndSeriesCatalogs(t *testing.T) {
	server := newStremioShelfTestServer(t)
	defer server.Close()

	handler := NewMetadataHandler(&fakeMetadataService{}, nil)
	handler.stremioHTTPClient = server.Client()
	request := httptest.NewRequest(http.MethodGet, "/manifest?url="+url.QueryEscape(server.URL+"/configured/manifest.json"), nil)
	response := httptest.NewRecorder()
	handler.StremioManifest(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload stremioManifestIngestionResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Name != "Shelf Add-on" || payload.ManifestURL != server.URL+"/configured/manifest.json" {
		t.Fatalf("unexpected manifest payload: %+v", payload)
	}
	if len(payload.Catalogs) != 2 {
		t.Fatalf("catalog count=%d, want 2: %+v", len(payload.Catalogs), payload.Catalogs)
	}
	if payload.Catalogs[0].Type != "movie" || payload.Catalogs[1].Type != "series" {
		t.Fatalf("catalog types not normalized: %+v", payload.Catalogs)
	}
}

func TestStremioManifestResolvesAddonDirectoryPage(t *testing.T) {
	handler := NewMetadataHandler(&fakeMetadataService{}, nil)
	handler.stremioHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := ""
		switch {
		case req.URL.Host == "stremio-addons.net" && req.URL.Path == "/addons/bharat-binge":
			body = `<script>self.__next_f.push([1,"{\"manifestUrl\":\"https://addon.example/configured/manifest.json\"}"])</script>`
		case req.URL.Host == "addon.example" && req.URL.Path == "/configured/manifest.json":
			body = `{"id":"test.directory","name":"Directory Add-on","catalogs":[{"type":"movie","id":"movies","name":"Movies"}]}`
		default:
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	request := httptest.NewRequest(
		http.MethodGet,
		"/manifest?url="+url.QueryEscape("https://stremio-addons.net/addons/bharat-binge"),
		nil,
	)
	response := httptest.NewRecorder()
	handler.StremioManifest(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload stremioManifestIngestionResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ManifestURL != "https://addon.example/configured/manifest.json" {
		t.Fatalf("manifest URL=%q, want resolved add-on manifest", payload.ManifestURL)
	}
	if payload.Name != "Directory Add-on" || len(payload.Catalogs) != 1 {
		t.Fatalf("unexpected directory payload: %+v", payload)
	}
}

func TestStremioListIngestsAllPagesIntoCuratedShelf(t *testing.T) {
	server := newStremioShelfTestServer(t)
	defer server.Close()

	service := &fakeMetadataService{
		curatedResp: []models.TrendingItem{
			{Rank: 0, Title: models.Title{ID: "imdb:tt0000001", Name: "One", MediaType: "movie"}},
			{Rank: 1, Title: models.Title{ID: "tmdb:movie:42", Name: "Two", MediaType: "movie"}},
			{Rank: 2, Title: models.Title{ID: "title:three", Name: "Three", MediaType: "movie"}},
		},
	}
	handler := NewMetadataHandler(service, nil)
	handler.stremioHTTPClient = server.Client()
	query := url.Values{
		"manifestUrl": {server.URL + "/configured/manifest.json"},
		"catalogType": {"movie"},
		"catalogId":   {"ranked"},
		"name":        {"Ranked"},
	}
	request := httptest.NewRequest(http.MethodGet, "/list?"+query.Encode(), nil)
	response := httptest.NewRecorder()
	handler.StremioList(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(service.lastCuratedItems) != 3 {
		t.Fatalf("curated items=%d, want 3: %+v", len(service.lastCuratedItems), service.lastCuratedItems)
	}
	if service.lastCuratedItems[0].IMDBID != "tt0000001" {
		t.Fatalf("first identity=%+v, want IMDb", service.lastCuratedItems[0])
	}
	if service.lastCuratedItems[1].TMDBID != 42 {
		t.Fatalf("second identity=%+v, want TMDB 42", service.lastCuratedItems[1])
	}
	if service.lastCuratedItems[2].Year != 2024 {
		t.Fatalf("third year=%d, want 2024", service.lastCuratedItems[2].Year)
	}
}

func newStremioShelfTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/configured/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"test.shelves","name":"Shelf Add-on","version":"1.2.3",
			"catalogs":[
				{"type":"movie","id":"ranked","name":"Ranked","pageSize":2,"extra":[{"name":"skip"}]},
				{"type":"tv","id":"shows","name":"Shows"},
				{"type":"channel","id":"live","name":"Live"}
			]
		}`))
	})
	mux.HandleFunc("/configured/catalog/movie/ranked.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"metas":[
			{"id":"tt0000001","type":"movie","name":"One","releaseInfo":"2025"},
			{"id":"tmdb:42","type":"movie","name":"Two","releaseInfo":"2023"}
		]}`))
	})
	mux.HandleFunc("/configured/catalog/movie/ranked/skip=2.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"metas":[
			{"id":"custom:three","type":"movie","name":"Three","releaseInfo":"Released 2024"}
		]}`))
	})
	return httptest.NewServer(mux)
}
